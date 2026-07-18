package game

import (
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Real OS-level input injection. Needed for D2R's front-end MENU screens, which ignore
// koolo's PostMessage/SendMessage window messages (the in-game engine honors those, but
// the menu UI reads hardware-level input only). SendInput delivers to the foreground
// window, so callers must ensure D2R is foreground first (see Manager.forceForeground).

var (
	user32SI             = windows.NewLazySystemDLL("user32.dll")
	procSendInput        = user32SI.NewProc("SendInput")
	procClientToScreen   = user32SI.NewProc("ClientToScreen")
	procGetSystemMetrics = user32SI.NewProc("GetSystemMetrics")
	procGetCursorPos     = user32SI.NewProc("GetCursorPos")
	procGetFocus         = user32SI.NewProc("GetFocus")
	procGetForegroundWin = user32SI.NewProc("GetForegroundWindow")
)

type winPoint struct{ X, Y int32 }

// SendClickClient converts a client-area coordinate of hwnd to screen space and does a
// real OS-level left click there (foreground window). Used for menu screens that ignore
// koolo's window-message clicks.
func SendClickClient(hwnd uintptr, clientX, clientY int) {
	p := winPoint{int32(clientX), int32(clientY)}
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&p)))
	cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	if cx == 0 || cy == 0 {
		return
	}
	nx := uint32(float64(p.X) * 65535.0 / float64(cx))
	ny := uint32(float64(p.Y) * 65535.0 / float64(cy))
	fmt.Fprintf(os.Stderr, "[click] client(%d,%d)->screen(%d,%d) screen=%dx%d norm=(%d,%d)\n", clientX, clientY, p.X, p.Y, cx, cy, nx, ny)
	// Prefer driver-level Interception (reaches DirectInput menus); else SendInput fallback.
	if iSendClickAbs(int32(nx), int32(ny)) {
		return
	}
	move := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfMove | mouseeventfAbsolute}
	down := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfLeftDown | mouseeventfAbsolute}
	up := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfLeftUp | mouseeventfAbsolute}
	sendInputs([]hwInput{move})
	time.Sleep(80 * time.Millisecond)
	var cur winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	fg, _, _ := procGetForegroundWin.Call()
	fmt.Fprintf(os.Stderr, "[click] cursor-after-move=(%d,%d) foreground=0x%x\n", cur.X, cur.Y, fg)
	sendInputs([]hwInput{down})
	time.Sleep(80 * time.Millisecond)
	sendInputs([]hwInput{up})
}

const (
	inputMouse       = 0
	inputKeyboard    = 1
	keyeventfKeyUp   = 0x0002
	keyeventfScancode = 0x0008

	mouseeventfMove     = 0x0001
	mouseeventfAbsolute = 0x8000
	mouseeventfLeftDown = 0x0002
	mouseeventfLeftUp   = 0x0004
	mouseeventfVirtualDesk = 0x4000
)

// hwInput mirrors Win32 INPUT (x64: 4-byte type + 4 pad + 32-byte union). The fields
// below overlay the union as KEYBDINPUT for keyboard, or MOUSEINPUT for mouse; both fit.
type hwInput struct {
	inputType uint32
	_         uint32
	// union (32 bytes). Interpreted as MOUSEINPUT (dx,dy,mouseData,dwFlags,time,extra)
	// or KEYBDINPUT (wVk,wScan,dwFlags,time,extra) depending on inputType.
	a         uint32 // MOUSEINPUT.dx  | KEYBDINPUT{wVk,wScan}
	b         uint32 // MOUSEINPUT.dy  | KEYBDINPUT.dwFlags
	c         uint32 // MOUSEINPUT.mouseData | KEYBDINPUT.time
	d         uint32 // MOUSEINPUT.dwFlags
	e         uint32 // MOUSEINPUT.time
	extra     uintptr
}

func sendInputs(inputs []hwInput) {
	if len(inputs) == 0 {
		return
	}
	r, _, err := procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	fmt.Fprintf(os.Stderr, "[sendinput] structSize=%d n=%d injected=%d err=%v\n", unsafe.Sizeof(inputs[0]), len(inputs), r, err)
}

// SendKeyReal presses and releases a virtual-key. Prefers driver-level Interception
// (reaches DirectInput menus); falls back to OS-level SendInput.
func SendKeyReal(vk uint16) {
	if iSendKey(vk) {
		return
	}
	down := hwInput{inputType: inputKeyboard, a: uint32(vk)}                  // wVk=vk, wScan=0
	up := hwInput{inputType: inputKeyboard, a: uint32(vk), b: keyeventfKeyUp} // dwFlags=KEYUP
	sendInputs([]hwInput{down})
	sendInputs([]hwInput{up})
}

// SendClickRealScreen is SendClickReal against the primary display's own bounds — the
// aquarium is a single-screen laptop, so the virtual desktop IS the screen. Takes the
// same LOGICAL screen coords as SetCursorPos (WindowLeft + client), keeping units
// consistent with the non-DPI-aware process.
func SendClickRealScreen(screenX, screenY int) {
	cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	SendClickReal(screenX, screenY, 0, 0, int(cx), int(cy))
}

// SendClickReal moves the cursor to absolute screen (x,y) and left-clicks, at the OS
// level. Coordinates are normalized to the 0..65535 virtual-desktop space.
func SendClickReal(screenX, screenY, virtualLeft, virtualTop, virtualW, virtualH int) {
	if virtualW <= 0 || virtualH <= 0 {
		return
	}
	nx := uint32(float64(screenX-virtualLeft) * 65535.0 / float64(virtualW))
	ny := uint32(float64(screenY-virtualTop) * 65535.0 / float64(virtualH))
	move := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk}
	down := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfLeftDown | mouseeventfAbsolute | mouseeventfVirtualDesk}
	up := hwInput{inputType: inputMouse, a: nx, b: ny, d: mouseeventfLeftUp | mouseeventfAbsolute | mouseeventfVirtualDesk}
	sendInputs([]hwInput{move})
	sendInputs([]hwInput{down})
	sendInputs([]hwInput{up})
}
