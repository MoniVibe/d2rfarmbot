package game

import (
	"math"
	"math/rand"
	"time"

	"github.com/lxn/win"
)

const (
	RightButton MouseButton = win.MK_RBUTTON
	LeftButton  MouseButton = win.MK_LBUTTON

	ShiftKey ModifierKey = win.VK_SHIFT
	CtrlKey  ModifierKey = win.VK_CONTROL
)

type MouseButton uint
type ModifierKey byte

// MovePointer moves the mouse to the requested position, x and y should be the final position based on
// pixels shown in the screen. Top-left corner is 0,0
func (hid *HID) MovePointer(x, y int) {
	hid.gr.updateWindowPositionData()
	// WORLD aim: the game's world hit-test works in PHYSICAL render pixels (MEASURED on the
	// laptop 2026-07-17 by -aimprobe: hover fires at client*displayScale, never at client*1),
	// while gameToScreen computes in the LOGICAL window rect a DPI-unaware process sees. Scale
	// the client offset by worldScale before the physical conversion. Panels are DIFFERENT —
	// their hit-test recovers the UNSCALED offset (measured on the desktop) — so AimPhysical
	// below stays raw; both measurements stand, they're separate game-side coordinate spaces.
	wx, wy := worldAimClient(hid.gr, x, y)
	ppx, ppy := clientToPhysical(hid.gr.WindowLeftX, hid.gr.WindowTopY, wx, wy)
	x = hid.gr.WindowLeftX + x
	y = hid.gr.WindowTopY + y

	// THE in-game cursor read on this D2R build is GetPhysicalCursorPos (physical/DPI-actual
	// pixels), NOT the classic GetCursorPos — proven 4/4 by -inputtest. Patch it with the
	// DPI-scaled point so in-game force-move/aim follows our target, BACKGROUNDED, WITHOUT
	// moving the real system cursor (the "aquarium": no input hijack, no focus, no driver).
	// NOTE: GetDpiForWindow returns 96 for a DPI-unaware process, so we use an explicit
	// physicalScale (display scaling, e.g. 1.5 at 150%) rather than trusting the API.
	hid.gi.OverridePhysicalCursorPos(ppx, ppy)

	// Optional driver-level move (default OFF; only when -hwmove rel/abs) — intrusive, moves
	// the real cursor. Off by default so normal runs never disturb the user's input.
	iMoveScreen(x, y)

	// The legacy GetCursorPos patch is NOT "harmless belt-and-braces" on this build: GetCursorPos
	// and GetPhysicalCursorPos resolve to THE SAME FUNCTION (verified by -hookscan), so this used
	// to write a different 21-byte stub straight over the physical stub installed above — with the
	// UNSCALED point, which is what the "competing cursor / drift" in AimPhysical's comment
	// actually was. Worse, the clobber left physStubActive set, so the next AimPhysical spliced
	// coordinate bytes into the middle of the wrong stub: garbage code in a function D2R calls
	// every frame = the random access violations. When the exports alias, patching the physical
	// cursor above ALREADY patches GetCursorPos. Only write it on a build where they differ.
	if !hid.gi.AliasesPhysicalCursorPos() {
		hid.gi.CursorPos(x, y)
	}
	lParam := calculateLparam(x, y)
	win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
	win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
	win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
}

// physicalScale converts logical screen pixels to the physical (DPI-actual) pixels that
// GetPhysicalCursorPos deals in. Defaults to 1.5 (150% Windows display scaling, this rig);
// override with -dpiscale if the display scaling differs. GetDpiForWindow can't be trusted
// here because a DPI-unaware process is told 96 regardless of the real scaling.
var physicalScale = 1.5

// SetPhysicalScale sets the logical→physical pixel scale used for the in-game cursor patch.
func SetPhysicalScale(s float64) {
	if s > 0 {
		physicalScale = s
	}
}

// worldScale converts gameToScreen's LOGICAL client coords into the game's WORLD cursor space.
// MEASURED (laptop, 2026-07-17, -aimprobe): the world hit-test lives in PHYSICAL render pixels —
// hover fires at client*displayScale, never at client*1 — and world DIRECTION is
// (cursor - charCenter) in that same space, so unscaled aim is biased toward the window's
// top-left (the "east returned 0" / self-drift anomalies). Panels are the opposite (offset
// recovered UNSCALED — desktop-measured); aimPanel has its own formula and is NOT affected.
var worldScale = 1.0

// SetWorldScale sets the logical→world-space scale for world aiming (MovePointer/AimPhysical).
func SetWorldScale(s float64) {
	if s > 0 {
		worldScale = s
	}
}

// worldAimClient scales a logical client point into world cursor space and CLAMPS it inside the
// window. Clamping is load-bearing: a far target's carrot can scale past the physical window
// edge (e.g. logical 1540 * 1.25 = 1925 > 1920), and the game DISCARDS an out-of-window cursor —
// force-move then does nothing at all (measured: 20s of walkToHold with zero net movement).
func worldAimClient(gr *MemoryReader, x, y int) (int, int) {
	wx := float64(x) * worldScale
	wy := float64(y) * worldScale
	maxX := float64(gr.GameAreaSizeX)*worldScale - 8
	maxY := float64(gr.GameAreaSizeY)*worldScale - 8
	wx = math.Min(math.Max(wx, 8), maxX)
	wy = math.Min(math.Max(wy, 8), maxY)
	return int(wx), int(wy)
}

// AimPhysical points the in-game cursor at client (x,y) using ONLY the GetPhysicalCursorPos
// patch — the real in-game cursor read on this build. Deliberately does NOT send the
// WM_MOUSEMOVE / WM_SETCURSOR messages or patch GetCursorPos that MovePointer does: those
// feed the game a COMPETING cursor position that fights the physical read and makes
// force-move drift. Use this for movement/aim; use MovePointer for menu/click interactions.
func (hid *HID) AimPhysical(x, y int) {
	hid.gr.updateWindowPositionData()
	wx, wy := worldAimClient(hid.gr, x, y)
	px, py := clientToPhysical(hid.gr.WindowLeftX, hid.gr.WindowTopY, wx, wy)
	hid.gi.OverridePhysicalCursorPos(px, py)
	iMoveScreen(hid.gr.WindowLeftX+x, hid.gr.WindowTopY+y) // no-op unless -hwmove rel/abs
}

// clientToPhysical converts a client point to the physical cursor value the GAME will read back as
// that same client point. MEASURED 2026-07-17, not derived: D2R recovers its client point as
// (physicalCursor - physicalWindowOrigin), so the client offset is added UNSCALED to the SCALED
// origin.
//
// The old math scaled the whole sum — (origin+client)*scale — which made the game see client*scale.
// Nothing caught it for the entire life of the project because nothing here tests ABSOLUTE aim:
// force-move needs only a direction, and hoverPickClick sweeps +-80px until the game reports a
// hover, so it succeeds THROUGH the error. It surfaced only when a UI panel — which has no sweep to
// hide behind — ignored every row click. Proof: clicking a WP row at (250,260) did nothing (it was
// landing on row 0, the current area, the one no-op row), while clicking (250,260)/1.5=(167,173)
// travelled Lut Gholein -> Dry Hills; with this formula the natural coords travel both ways.
//
// Note the old error was NOT a harmless constant offset: it scaled about the desktop origin, so it
// injected a growing bias across the window (~+213px in x at screen centre at 1.5) — i.e. aim drift
// that worsens the further from the origin she points.
func clientToPhysical(winLeft, winTop, x, y int) (int, int) {
	return int(float64(winLeft)*physicalScale) + x, int(float64(winTop)*physicalScale) + y
}

// Click just does a single mouse click at current pointer position
func (hid *HID) Click(btn MouseButton, x, y int) {
	hid.MovePointer(x, y)
	x = hid.gr.WindowLeftX + x
	y = hid.gr.WindowTopY + y

	lParam := calculateLparam(x, y)
	buttonDown := uint32(win.WM_LBUTTONDOWN)
	buttonUp := uint32(win.WM_LBUTTONUP)
	if btn == RightButton {
		buttonDown = win.WM_RBUTTONDOWN
		buttonUp = win.WM_RBUTTONUP
	}

	win.SendMessage(hid.gr.HWND, buttonDown, 1, lParam)
	sleepTime := rand.Intn(keyPressMaxTime-keyPressMinTime) + keyPressMinTime
	time.Sleep(time.Duration(sleepTime) * time.Millisecond)
	win.SendMessage(hid.gr.HWND, buttonUp, 1, lParam)
}

// LeftClickNoMove sends a discrete left button down+up at client (x,y) WITHOUT moving the
// pointer first. For interaction after a hover-confirm sweep: the physical cursor is already
// exactly on the target's label, and the extra MovePointer/WM_MOUSEMOVE that Click() does can
// nudge the game's notion of the cursor just enough that the click reads as a ground-move
// instead of a pickup.
func (hid *HID) LeftClickNoMove(x, y int) {
	sx := hid.gr.WindowLeftX + x
	sy := hid.gr.WindowTopY + y
	lParam := calculateLparam(sx, sy)
	win.SendMessage(hid.gr.HWND, win.WM_LBUTTONDOWN, 1, lParam)
	sleepTime := rand.Intn(keyPressMaxTime-keyPressMinTime) + keyPressMinTime
	time.Sleep(time.Duration(sleepTime) * time.Millisecond)
	win.SendMessage(hid.gr.HWND, win.WM_LBUTTONUP, 1, lParam)
}

// LeftClickNoMoveClient is like LeftClickNoMove but packs CLIENT coords into the WM lParam
// (WM_LBUTTONDOWN expects client, not screen). The WORLD ignores lParam — it hit-tests off the
// polled cursor — so LeftClickNoMove's screen-coord lParam is benign there. But UI PANELS may
// hit-test off lParam, so panel clicks use this correct-space variant.
func (hid *HID) LeftClickNoMoveClient(x, y int) {
	lParam := calculateLparam(x, y)
	win.SendMessage(hid.gr.HWND, win.WM_LBUTTONDOWN, 1, lParam)
	sleepTime := rand.Intn(keyPressMaxTime-keyPressMinTime) + keyPressMinTime
	time.Sleep(time.Duration(sleepTime) * time.Millisecond)
	win.SendMessage(hid.gr.HWND, win.WM_LBUTTONUP, 1, lParam)
}

// MouseMoveClient posts a WM_MOUSEMOVE at CLIENT (x,y) — some UI panels only make a row
// selectable after a hover/move event (the world interaction path hovers first; panel clicks
// otherwise send only button messages). Client-coord lParam, no cursor-export patching.
func (hid *HID) MouseMoveClient(x, y int) {
	lParam := calculateLparam(x, y)
	win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
}

// (MouseMoveClientSync removed 2026-07-17. It sent the panel hover via SendMessage on the theory
// that destination rows needed a synchronously-processed hover. The theory was wrong — rows ignored
// clicks because the absolute cursor mapping was off by a factor of the DPI scale, so they were
// landing on row 0, the current area, where a click is a no-op. Once clientToPhysical was fixed,
// travel worked with the plain async hover. Deleting it also removes a hazard: SendMessage BLOCKS
// against a frozen/crashing D2R window, which is how farmbot hangs mid-run holding patches.)

func (hid *HID) ClickWithModifier(btn MouseButton, x, y int, modifier ModifierKey) {
	hid.gi.OverrideGetKeyState(byte(modifier))
	hid.Click(btn, x, y)
	hid.gi.RestoreGetKeyState()
}

func calculateLparam(x, y int) uintptr {
	return uintptr(y<<16 | x)
}
