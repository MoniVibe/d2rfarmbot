package game

import (
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Driver-level input via the Interception driver (oblitum). D2R's front-end menu reads
// DirectInput/RawInput straight from the HID stack, which user-mode PostMessage/SendInput
// cannot reach. Interception injects at the keyboard/mouse device level, so the menu sees
// it as real hardware. Requires the Interception driver installed (install-interception.exe
// /install + reboot) and interception.dll next to koolo.exe. Falls back to SendInput if
// the driver/dll is unavailable.

var (
	interceptionDLL     = windows.NewLazyDLL("interception.dll")
	procICreateContext  = interceptionDLL.NewProc("interception_create_context")
	procIDestroyContext = interceptionDLL.NewProc("interception_destroy_context")
	procISend           = interceptionDLL.NewProc("interception_send")
	procMapVirtualKeyW  = user32SI.NewProc("MapVirtualKeyW")

	interceptionCtx   uintptr
	interceptionOnce  sync.Once
	interceptionReady bool
)

const (
	iKeyDown = 0x00
	iKeyUp   = 0x01

	iMouseLeftDown = 0x001
	iMouseLeftUp   = 0x002
	iMouseMoveRel  = 0x000
	iMouseMoveAbs  = 0x001

	iKeyboardDevice = 1  // INTERCEPTION_KEYBOARD(0)
	iMouseDevice    = 11 // INTERCEPTION_MOUSE(0) = MAX_KEYBOARD(10)+1
)

// hwMoveMode selects how MovePointer drives the hardware-level cursor through the
// Interception driver: "off" (DEFAULT — no driver strokes at all, so the real mouse and
// user clicks are never disturbed; the aquarium requirement), "rel" (closed-loop relative
// strokes), or "abs" (absolute stroke). rel/abs move the REAL system cursor and flood the
// HID stream, which locks the user out of their own input — diagnostics only, never the
// default runtime path.
var hwMoveMode = "off"

// SetHWMoveMode selects the Interception mouse-move strategy ("abs"|"rel"|"off").
func SetHWMoveMode(mode string) { hwMoveMode = mode }

// hwMoveDevice pins rel-mode strokes to one Interception mouse device slot (11-20).
// 0 = default (slot 11). D2R may read input per-device (GameInput/DirectInput), in which
// case only strokes injected on the REAL mouse's slot steer the game — -devscan finds it.
var hwMoveDevice = 0

// SetHWMoveDevice pins rel-mode strokes to a specific device slot (0 = default).
func SetHWMoveDevice(dev int) { hwMoveDevice = dev }

type iKeyStroke struct {
	code        uint16
	state       uint16
	information uint32
}

type iMouseStroke struct {
	state       uint16
	flags       uint16
	rolling     int16
	x           int32
	y           int32
	information uint32
}

func initInterception() {
	interceptionOnce.Do(func() {
		if err := interceptionDLL.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "[interception] dll not found: %v (using SendInput fallback)\n", err)
			return
		}
		ctx, _, _ := procICreateContext.Call()
		if ctx == 0 {
			fmt.Fprintf(os.Stderr, "[interception] create_context failed — driver not installed? (using SendInput fallback)\n")
			return
		}
		interceptionCtx = ctx
		interceptionReady = true
		fmt.Fprintf(os.Stderr, "[interception] READY — driver-level input active\n")
	})
}

func interceptionEnabled() bool {
	initInterception()
	return interceptionReady
}

// iSendKey presses+releases a virtual-key at the driver level. Sends to all keyboard
// device slots (1..10) since the real keyboard may be on any of them. Returns false if unavailable.
func iSendKey(vk uint16) bool {
	if !interceptionEnabled() {
		return false
	}
	sc, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0) // MAPVK_VK_TO_VSC
	down := iKeyStroke{code: uint16(sc), state: iKeyDown}
	up := iKeyStroke{code: uint16(sc), state: iKeyUp}
	okDevs := ""
	for dev := 1; dev <= 10; dev++ {
		r, _, _ := procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&down)), 1)
		if r != 0 {
			okDevs += fmt.Sprintf("%d ", dev)
		}
	}
	time.Sleep(40 * time.Millisecond)
	for dev := 1; dev <= 10; dev++ {
		procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&up)), 1)
	}
	fmt.Fprintf(os.Stderr, "[interception] sent key vk=0x%x scan=0x%x okDevs=[%s]\n", vk, sc, okDevs)
	return true
}

// iSendKeyUp sends ONLY the up-stroke for a virtual-key to every keyboard slot — the
// shift-amnesty half: releases a leaked driver-level modifier without ever pressing it.
func iSendKeyUp(vk uint16) bool {
	if !interceptionEnabled() {
		return false
	}
	sc, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0)
	up := iKeyStroke{code: uint16(sc), state: iKeyUp}
	for dev := 1; dev <= 10; dev++ {
		procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&up)), 1)
	}
	return true
}

// iSendClickAbs moves (absolute 0..65535) and left-clicks at the driver level. Returns false if unavailable.
func iSendClickAbs(nx, ny int32) bool {
	if !interceptionEnabled() {
		return false
	}
	move := iMouseStroke{flags: iMouseMoveAbs, x: nx, y: ny}
	down := iMouseStroke{state: iMouseLeftDown, flags: iMouseMoveAbs, x: nx, y: ny}
	up := iMouseStroke{state: iMouseLeftUp, flags: iMouseMoveAbs, x: nx, y: ny}
	okDevs := ""
	for dev := 11; dev <= 20; dev++ {
		r, _, _ := procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&move)), 1)
		if r != 0 {
			okDevs += fmt.Sprintf("%d ", dev)
		}
	}
	time.Sleep(60 * time.Millisecond)
	for dev := 11; dev <= 20; dev++ {
		procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&down)), 1)
	}
	time.Sleep(60 * time.Millisecond)
	for dev := 11; dev <= 20; dev++ {
		procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&up)), 1)
	}
	fmt.Fprintf(os.Stderr, "[interception] sent click abs=(%d,%d) okDevs=[%s]\n", nx, ny, okDevs)
	return true
}

var hwMoveLoggedOnce sync.Once

// HWMoveProbe drives the hardware cursor to screen (px,py) via Interception and reads
// back the resulting cursor position — validates the driver stroke path end-to-end
// without touching D2R. Returns (gotX, gotY, sent).
func HWMoveProbe(px, py int) (int32, int32, bool) {
	sent := iMoveScreen(px, py)
	time.Sleep(50 * time.Millisecond)
	var cur winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	return cur.X, cur.Y, sent
}

// iMoveScreen moves the hardware-level cursor to SCREEN pixel (px,py) through the
// Interception driver, honoring hwMoveMode. This is what actually steers D2R in-game:
// force-move follows the mouse via RawInput/HID, which only sees driver-level strokes —
// the user-mode GetCursorPos patch and WM_MOUSEMOVE are ignored (HANDOFF §2). Returns
// false if the driver is unavailable or mode is "off" (caller keeps legacy behavior).
func iMoveScreen(px, py int) bool {
	if hwMoveMode == "off" || !interceptionEnabled() {
		return false
	}
	ok := false
	switch hwMoveMode {
	case "rel":
		// Relative strokes are what a physical mouse produces, so D2R's RawInput reader
		// cannot tell them apart from real hand movement. But a single big delta lands
		// unpredictably (Windows pointer acceleration is nonlinear, DPI scaling remaps it,
		// and fanning one stroke to all 10 device slots multiplies it) — so run a CLOSED
		// LOOP: capped micro-steps on ONE device, reading the cursor back each iteration.
		for i := 0; i < 150; i++ {
			var cur winPoint
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
			dx, dy := int32(px)-cur.X, int32(py)-cur.Y
			if dx == 0 && dy == 0 {
				ok = true
				break
			}
			cap := func(v int32) int32 {
				if v > 25 {
					return 25
				}
				if v < -25 {
					return -25
				}
				return v
			}
			move := iMouseStroke{flags: iMouseMoveRel, x: cap(dx), y: cap(dy)}
			r, _, _ := procISend.Call(interceptionCtx, uintptr(iMouseDevice), uintptr(unsafe.Pointer(&move)), 1)
			if r == 0 {
				break
			}
			ok = true
			time.Sleep(3 * time.Millisecond)
		}
	default: // "abs" — normalized 0..65535 over the primary display, same space as iSendClickAbs
		cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
		cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
		if cx == 0 || cy == 0 {
			return false
		}
		move := iMouseStroke{flags: iMouseMoveAbs,
			x: int32(float64(px) * 65535.0 / float64(cx)),
			y: int32(float64(py) * 65535.0 / float64(cy))}
		for dev := 11; dev <= 20; dev++ {
			r, _, _ := procISend.Call(interceptionCtx, uintptr(dev), uintptr(unsafe.Pointer(&move)), 1)
			if r != 0 {
				ok = true
			}
		}
	}
	if ok {
		hwMoveLoggedOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "[interception] hardware mouse-move active (mode=%s) — aiming drives the REAL cursor\n", hwMoveMode)
		})
	}
	return ok
}
