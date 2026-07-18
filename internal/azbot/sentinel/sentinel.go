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

type Config struct {
	KillVK    int    // virtual key of the kill-switch (default VK_PAUSE 0x13)
	BeltKeys  []byte // belt column keys, left to right
	DrinkAtHP int    // drink at or below this HP%
	DrinkCD   time.Duration
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
		if realKeyDown(s.cfg.KillVK) {
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
		md, hp, _, ar, valid := s.p.SurvivalRead()
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

		// Belt drinking: key lane only; rotates columns so an emptied slot doesn't
		// starve the reflex. Gated on the self-model KNOWING it has potions — pressing
		// keys into an empty belt was the 25s-bleed death's accomplice; when HealPots==0
		// the Executive's EscapeTP handles survival instead.
		last := s.p.Last()
		hasPots := last != nil && last.Me.HealPots > 0
		if !dead && hp > 0 && hp <= s.cfg.DrinkAtHP && hasPots && time.Since(lastDrink) > s.cfg.DrinkCD &&
			len(s.cfg.BeltKeys) > 0 && s.m.Engage.Engaged() {
			key := s.cfg.BeltKeys[beltIdx%len(s.cfg.BeltKeys)]
			beltIdx++
			if s.m.KeyLane().Press(key) {
				lastDrink = time.Now()
				s.log.Info("sentinel: drink", "hp", hp, "col", beltIdx%len(s.cfg.BeltKeys))
			}
		}
	}
}
