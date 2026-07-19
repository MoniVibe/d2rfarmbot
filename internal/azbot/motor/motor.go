// Package motor owns ALL actuation: LAW 2 — the cursor is a scheduled instrument — and
// the owner's kill-switch. There is no code path in azbot that can touch the mouse or
// keyboard except through this package, which is why one Engagement gate is sufficient
// to hand Diablo back to the human, input patches healed, in one keypress.
package motor

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/koolo/internal/game"
)

// Engagement is the kill-switch state. Disengaged = the human owns Diablo natively.
type Engagement struct {
	engaged atomic.Bool
}

func (e *Engagement) Engaged() bool { return e.engaged.Load() }

// CursorRole tags what the cursor is being used AS. Mutually exclusive by lease.
type CursorRole uint8

const (
	RoleSteer CursorRole = iota // movement direction sample
	RoleAim                     // strike/click effector
	RoleSense                   // parked to read HoverData
)

// CursorLease is exclusive, time-boxed cursor ownership. The dead-man timer force-
// releases (and moveStop()s) at the deadline — a crashed holder cannot wedge a key down.
type CursorLease struct {
	Role     CursorRole
	Holder   string
	Deadline time.Time
	m        *Motor
	released atomic.Bool
}

func (l *CursorLease) Release() {
	if l.released.CompareAndSwap(false, true) {
		l.m.mu.Lock()
		if l.m.lease == l {
			l.m.lease = nil
		}
		l.m.mu.Unlock()
		l.m.MoveStop()
	}
}

// Motor is the single actuation authority.
type Motor struct {
	log *slog.Logger
	hid *game.HID
	gi  *game.MemoryInjector

	Engage Engagement
	panelScale float64

	mu       sync.Mutex
	lease    *CursorLease
	moveKey  byte
	moveHeld bool
	deadman  *time.Timer
}

func New(log *slog.Logger, hid *game.HID, gi *game.MemoryInjector, moveKey byte) *Motor {
	m := &Motor{log: log, hid: hid, gi: gi, moveKey: moveKey}
	m.Engage.engaged.Store(true)
	return m
}

// Disengage: the kill-switch. Releases held keys, cancels the lease, and HEALS every
// input patch live (gi.Unload restores D2R's original bytes) so the human's mouse and
// keyboard work natively immediately. Reads continue elsewhere; actuation is dead here.
func (m *Motor) Disengage() {
	if !m.Engage.engaged.CompareAndSwap(true, false) {
		return
	}
	m.mu.Lock()
	if m.lease != nil {
		m.lease.released.Store(true)
		m.lease = nil
	}
	m.mu.Unlock()
	m.MoveStop()
	// Modifier amnesty: hand the game back with NO latched modifiers — the owner's
	// stuck-shift ("regular clicks become shift-clicks") dies here whoever leaked it.
	m.ModifierAmnesty()
	if err := m.gi.Unload(); err != nil {
		m.log.Error("motor: disengage heal failed", "err", err)
	}
	m.log.Warn("MOTOR DISENGAGED — Diablo is yours; input patches healed")
}

// Reengage: re-arms actuation. The injector reloads its stubs; override pokes are
// idempotent and re-applied per action, so nothing else is needed.
func (m *Motor) Reengage() {
	if !m.Engage.engaged.CompareAndSwap(false, true) {
		return
	}
	if err := m.gi.Load(); err != nil {
		m.log.Error("motor: reengage injector load failed", "err", err)
		m.Engage.engaged.Store(false)
		return
	}
	m.log.Warn("MOTOR ENGAGED — azbot has the controls")
}

// PanelScale must be set once at startup (the display's DPI scale) before UIClick is
// used — panel clicks compute physical pixels from it.
func (m *Motor) SetPanelScale(s float64) { m.panelScale = s }

// BareClick is the NPC-talk click: a plain message click with NO key-state override.
// The override reads as a HELD button when the game polls mid-window, and a held click
// on an NPC is ATTACK semantics (the shift-click defect) — NPCs accept the bare click.
func (m *Motor) BareClick(x, y int) {
	if !m.Engage.Engaged() {
		return
	}
	m.hid.Click(game.LeftButton, x, y)
}

// MenuKey drives NPC-menu navigation: menus poll GetKeyState, so the override is held
// around a raw down/up (the force-move lesson applied to menus).
func (m *Motor) MenuKey(vk byte) {
	if !m.Engage.Engaged() {
		return
	}
	_ = m.gi.OverrideGetKeyState(vk)
	_ = m.gi.OverrideGetAsyncKeyState(vk)
	m.hid.RawKeyDown(vk)
	time.Sleep(220 * time.Millisecond)
	m.hid.RawKeyUp(vk)
	_ = m.gi.RestoreGetKeyState()
	_ = m.gi.RestoreGetAsyncKeyState()
	time.Sleep(200 * time.Millisecond)
}

// UIClick clicks a panel button/cell at client pixels — farmbot's proven recipe
// (patched cursor exports + LBUTTON override + client-lParam click). On vendor stock
// cells this IS an instant purchase (measured 2026-07-19).
func (m *Motor) UIClick(sx, sy int) {
	if !m.Engage.Engaged() {
		return
	}
	m.MoveStop()
	px := int(float64(m.hid.WindowLeftX())*m.panelScale) + sx
	py := int(float64(m.hid.WindowTopY())*m.panelScale) + sy
	_ = m.gi.OverridePhysicalCursorPos(px, py)
	m.hid.MouseMoveClient(sx, sy)
	time.Sleep(150 * time.Millisecond)
	_ = m.gi.OverrideGetKeyState(0x01)
	_ = m.gi.OverrideGetAsyncKeyState(0x01)
	m.hid.LeftClickNoMoveClient(sx, sy)
	_ = m.gi.RestoreGetKeyState()
	_ = m.gi.RestoreGetAsyncKeyState()
	time.Sleep(120 * time.Millisecond)
	_ = m.gi.RestorePhysicalCursorPos()
	_ = m.gi.RestoreGetCursorInfo()
	_ = m.gi.RestoreGetCursorPosAddr()
}

// SellClick is UIClick with CTRL held in the override — the quick-sell gesture on an
// inventory cell while a vendor trade is open. The ctrl override is restored in the
// same call; the amnesty machinery guards against any latch.
func (m *Motor) SellClick(sx, sy int) {
	if !m.Engage.Engaged() {
		return
	}
	m.MoveStop()
	px := int(float64(m.hid.WindowLeftX())*m.panelScale) + sx
	py := int(float64(m.hid.WindowTopY())*m.panelScale) + sy
	_ = m.gi.OverridePhysicalCursorPos(px, py)
	m.hid.MouseMoveClient(sx, sy)
	time.Sleep(150 * time.Millisecond)
	_ = m.gi.OverrideGetKeyState(0x11) // CTRL: the quick-sell modifier
	_ = m.gi.OverrideGetAsyncKeyState(0x11)
	m.hid.LeftClickNoMoveClient(sx, sy)
	_ = m.gi.RestoreGetKeyState()
	_ = m.gi.RestoreGetAsyncKeyState()
	time.Sleep(120 * time.Millisecond)
	_ = m.gi.RestorePhysicalCursorPos()
	_ = m.gi.RestoreGetCursorInfo()
	_ = m.gi.RestoreGetCursorPosAddr()
	m.ModifierAmnesty() // a latched ctrl is quick-sell on every later click — never risk it
}

// ModifierAmnesty releases every modifier (shift/ctrl/alt) at both the game-window and
// OS level. Alt-tab eats key releases (they go to the newly focused window), so a
// modifier held across a focus switch stays latched in D2R until this fresh release.
func (m *Motor) ModifierAmnesty() {
	m.hid.ModifierAmnesty()
	game.SendModifierUpReal()
}

// RealMenuClick clicks a SCREENSHOT-pixel position with OS-level real input, game
// foregrounded — the only input the pause/main menus accept. Screenshot px are physical
// client px; SendClickRealScreen wants logical screen coords (divide by display scale,
// add the logical client origin — the relog drill's law, 2026-07-19).
func (m *Motor) RealMenuClick(shotX, shotY int) {
	if !m.Engage.Engaged() {
		return
	}
	m.hid.FocusGame()
	time.Sleep(300 * time.Millisecond)
	sx := int(float64(shotX)/m.panelScale) + m.hid.WindowLeftX()
	sy := int(float64(shotY)/m.panelScale) + m.hid.WindowTopY()
	game.SendClickRealScreen(sx, sy)
}

// RealEsc presses ESC at the OS level with the game foregrounded (pause menu open/close).
func (m *Motor) RealEsc() {
	if !m.Engage.Engaged() {
		return
	}
	m.hid.FocusGame()
	time.Sleep(200 * time.Millisecond)
	game.SendKeyReal(0x1B)
}

// GameFocused reports whether D2R owns the foreground (Sentinel's focus watch).
func (m *Motor) GameFocused() bool { return m.hid.GameFocused() }

// ReserveCursor grants the exclusive cursor lease or refuses.
func (m *Motor) ReserveCursor(role CursorRole, holder string, d time.Duration) (*CursorLease, bool) {
	if !m.Engage.Engaged() {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lease != nil && time.Now().Before(m.lease.Deadline) {
		return nil, false
	}
	// RoleSense must never be converted into locomotion by a stale held key.
	if role == RoleSense && m.moveHeld {
		m.mu.Unlock()
		m.MoveStop()
		m.mu.Lock()
	}
	l := &CursorLease{Role: role, Holder: holder, Deadline: time.Now().Add(d), m: m}
	m.lease = l
	if m.deadman != nil {
		m.deadman.Stop()
	}
	m.deadman = time.AfterFunc(d, func() { l.Release() })
	return l, true
}

// MoveStop releases the force-move key and restores key-state overrides.
// Safe to call redundantly; the one true moveStop.
func (m *Motor) MoveStop() {
	m.mu.Lock()
	held := m.moveHeld
	m.moveHeld = false
	m.mu.Unlock()
	if held {
		m.hid.RawKeyUp(m.moveKey)
	}
	_ = m.gi.RestoreGetKeyState()
	_ = m.gi.RestoreGetAsyncKeyState()
}

// StrideEdge performs the measured key-edge ritual for one stride: release → aim →
// press. The direction is sampled by D2R at the key-down edge (LAW: measured
// 2026-07-18); callers own commitment and displacement verification (verbs, M2).
func (m *Motor) StrideEdge(aimX, aimY int) bool {
	if !m.Engage.Engaged() {
		return false
	}
	m.MoveStop()
	time.Sleep(60 * time.Millisecond) // edge separation; the only sleep class allowed here
	m.hid.AimPhysical(aimX, aimY)
	_ = m.gi.OverrideGetKeyState(m.moveKey)
	_ = m.gi.OverrideGetAsyncKeyState(m.moveKey)
	m.hid.RawKeyDown(m.moveKey)
	m.mu.Lock()
	m.moveHeld = true
	m.mu.Unlock()
	return true
}

// --- aimed-actuation pass-throughs: the verb layer's only input surface ---

func (m *Motor) AimPhysical(x, y int) {
	if m.Engage.Engaged() {
		m.hid.AimPhysical(x, y)
	}
}

func (m *Motor) PressKey(k byte) {
	if m.Engage.Engaged() {
		m.hid.PressKey(k)
	}
}

// ClickRight: world right-click (message path — proven reliable on this build).
func (m *Motor) ClickRight(x, y int) {
	if m.Engage.Engaged() {
		m.hid.Click(game.RightButton, x, y)
	}
}

// ClickLeft: the proven world left-click — VK_LBUTTON key-state overrides around the
// message click (farmbot's interactClick law: left needs the override treatment).
func (m *Motor) ClickLeft(x, y int) {
	if !m.Engage.Engaged() {
		return
	}
	_ = m.gi.OverrideGetKeyState(0x01)
	_ = m.gi.OverrideGetAsyncKeyState(0x01)
	m.hid.Click(game.LeftButton, x, y)
	_ = m.gi.RestoreGetKeyState()
	_ = m.gi.RestoreGetAsyncKeyState()
}

// KeyLane: cursor-free key presses (belt drinks, TP cast). The Sentinel's channel —
// survival never contends for the cursor.
type KeyLane struct{ m *Motor }

func (m *Motor) KeyLane() *KeyLane { return &KeyLane{m: m} }

func (k *KeyLane) Press(key byte) bool {
	if !k.m.Engage.Engaged() {
		return false
	}
	k.m.hid.PressKey(key)
	return true
}
