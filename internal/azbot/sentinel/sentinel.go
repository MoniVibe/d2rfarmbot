// Package sentinel is the survival tier: a fixed-cadence goroutine that NEVER blocks
// on anything the Executive does. It drinks through the key lane (no cursor), detects
// death by Mode, polls the owner's kill-switch on the REAL keyboard, and writes the
// heartbeat + death facts. LAW 1: survival is not a queue position.
package sentinel

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"golang.org/x/sys/windows"
)

var procGetAsyncKeyState = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")

// realKeyDown polls OUR process's true global key state — the human's physical
// keyboard, unaffected by the patches applied inside D2R.
func realKeyDown(vk int) bool {
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

// realKeyTapped is realKeyDown that also honors the SINCE-LAST-POLL bit (LSB), so a
// quick tap BETWEEN 100ms ticks still registers — the owner pressed F10 and nothing
// happened (measured 2026-07-19: the press fell between polls). For the kill-switch
// only; the LSB is per-process shared state, fine when we are its only reader.
func realKeyTapped(vk int) bool {
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8001 != 0
}

type Config struct {
	KillVK      int    // virtual key of the kill-switch (default VK_PAUSE 0x13)
	BeltKeys    []byte // belt column keys, left to right
	DrinkAtHP   int    // drink at or below this HP%
	DrinkAtMana int    // drink a blue at or below this mana% (0 = default 25)
	DrinkCD     time.Duration
}

type Sentinel struct {
	log  *slog.Logger
	p    *percept.Perceptor
	m    *motor.Motor
	mem  *memory.Store
	cfg  Config
	Dead chan struct{} // pulsed on death detection (level-triggered consumers re-read)
}

func New(log *slog.Logger, p *percept.Perceptor, m *motor.Motor, mem *memory.Store, cfg Config) *Sentinel {
	if cfg.KillVK == 0 {
		cfg.KillVK = 0x13 // VK_PAUSE
	}
	if cfg.DrinkCD == 0 {
		cfg.DrinkCD = 1500 * time.Millisecond
	}
	if cfg.DrinkAtMana == 0 {
		cfg.DrinkAtMana = 25
	}
	return &Sentinel{log: log, p: p, m: m, mem: mem, cfg: cfg, Dead: make(chan struct{}, 1)}
}

// Run is the 100ms survival loop. It must never call anything that can block beyond
// a bounded memory read + a key press.
func (s *Sentinel) Run(stop <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	var lastDrink time.Time
	var killHeld bool
	var wasDead bool
	var lastShift bool
	lastFocus := true
	beltIdx := 0
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}

		// KILL-SWITCH: one physical resource the human always owns — their keyboard.
		if realKeyTapped(s.cfg.KillVK) {
			if !killHeld {
				killHeld = true
				if s.m.Engage.Engaged() {
					s.m.Disengage()
				} else {
					s.m.Reengage()
				}
			}
		} else {
			killHeld = false
		}

		// Shift forensics: the OS-truth shift state, every tick, into the WAL. When the
		// owner's stuck-shift recurs, this trace says whether the OS ever saw a phantom
		// shift-down (driver/SendInput leak) or never did (a D2R-internal message latch).
		shiftDown := realKeyDown(0x10)
		if shiftDown != lastShift {
			s.log.Info("sentinel: OS shift state changed", "down", shiftDown, "engaged", s.m.Engage.Engaged())
			lastShift = shiftDown
		}

		// FOCUS WATCH: alt-tab eats key releases — a modifier held across the switch
		// stays latched in D2R until a fresh release arrives. Fire the amnesty on every
		// return of focus so the owner's alt-tabbing can never leave shift/ctrl/alt stuck.
		focused := s.m.GameFocused()
		if focused != lastFocus {
			if focused {
				s.m.ModifierAmnesty()
				s.log.Info("sentinel: game refocused — modifier amnesty fired")
			}
			lastFocus = focused
		}
		md, hp, mp, ar, valid := s.p.SurvivalRead()
		s.mem.PutJSON("heartbeat", memory.ScopeTick,
			memory.Provenance{Source: "measured"},
			map[string]any{"t": time.Now().UnixMilli(), "valid": valid, "hp": hp, "area": int(ar), "engaged": s.m.Engage.Engaged(), "shift": shiftDown})
		if !valid {
			continue
		}

		dead := md == mode.Death || md == mode.Dead
		if dead && !wasDead {
			s.log.Warn("sentinel: DEATH detected", "area", int(ar))
			s.mem.PutJSON("death_state", memory.ScopeGame,
				memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("mode=%d", md)},
				map[string]any{"area": int(ar), "at": time.Now().UnixMilli()})
			select {
			case s.Dead <- struct{}{}:
			default:
			}
		}
		wasDead = dead

		// Belt drinking: key lane only, COLUMN-AWARE (the old blind rotation gulped
		// mana while bleeding and never found the blue when the pool was dry). The
		// snapshot names which columns hold red and which hold blue; the reflex presses
		// exactly the right one. Gated on the self-model KNOWING it has potions —
		// pressing keys into an empty belt was the 25s-bleed death's accomplice.
		last := s.p.Last()
		if !dead && hp > 0 && last != nil && time.Since(lastDrink) > s.cfg.DrinkCD &&
			len(s.cfg.BeltKeys) > 0 && s.m.Engage.Engaged() {
			var cols []int
			label := ""
			if hp <= s.cfg.DrinkAtHP && len(last.Me.HPCols) > 0 {
				cols, label = last.Me.HPCols, "hp"
			} else if mp <= s.cfg.DrinkAtMana && len(last.Me.ManaCols) > 0 {
				// Blood before blue: mana drinks only when HP needs nothing this tick.
				cols, label = last.Me.ManaCols, "mana"
			}
			if len(cols) > 0 {
				col := cols[beltIdx%len(cols)]
				beltIdx++
				if col < len(s.cfg.BeltKeys) && s.m.KeyLane().Press(s.cfg.BeltKeys[col]) {
					lastDrink = time.Now()
					s.log.Info("sentinel: drink", "what", label, "hp", hp, "mp", mp, "col", col)
				}
			}
		}
	}
}
