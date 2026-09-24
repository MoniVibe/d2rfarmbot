package main

// The executive's gate and janitor (docs/AZBOT_V2.md step 6), behind -janitor /
// AZBOT_JANITOR=1. Each tick after arbitration the holder's Needs are judged
// against the stable screen Reading (exec.Janitor): a foreign panel or a
// foreign cursor item earns ONE janitor action, the holder's held clock pauses,
// and its Step is skipped. The policy is pure (internal/azbot/exec); this file
// only turns its answers into motor input and trace lines.

import (
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
	"github.com/hectorgimenez/koolo/internal/game"
)

type gatekeeper struct {
	logger *slog.Logger
	m      *motor.Motor
	gr     *game.MemoryReader
	sh     *shadow
	j      *exec.Janitor
	ses    *exec.Session // layer 0: the panels it claims (the pause menu while Relogging)

	said      string    // last gate line's why (a line per change, not per tick)
	blockedAt time.Time // when the current block began; zero while open
}

func newGatekeeper(logger *slog.Logger, m *motor.Motor, gr *game.MemoryReader, sh *shadow, ses *exec.Session) *gatekeeper {
	return &gatekeeper{logger: logger, m: m, gr: gr, sh: sh, j: exec.NewJanitor(), ses: ses}
}

// Stable is the reading the gate judges (nil before the first capture).
func (g *gatekeeper) Stable() *screen.Reading {
	seen := g.sh.Eye.Latest()
	if seen == nil {
		return nil
	}
	r := exec.Stable(*seen)
	return &r
}

// needs is the holder's gate view: the claims of its CURRENT phase (a town
// service claims nothing in its Clean phase, so every leftover is foreign),
// plus what the session claims: the pause menu while it is Relogging (the
// relog walks the pause menu on purpose; nothing else may keep it up, and the
// moment the relog ends it is foreign and clicked away by Return to Game).
func (g *gatekeeper) needs(who string, s *percept.Snapshot, roster *activity.Roster) exec.HolderNeeds {
	n := exec.NoHolder
	if a := roster.Get(who); a != nil {
		n = a.Needs(s).Holder()
	}
	if g.ses != nil {
		n.Claims |= g.ses.Claims()
	}
	return n
}

// perform executes one janitor action; the returned text is the trace's act=.
func (g *gatekeeper) perform(d exec.Decision) string {
	switch {
	case d.Drop:
		// The legacy cursor drop's own spot: her feet, below the HUD's reach.
		x, y := g.gr.GameAreaSizeX/2, g.gr.GameAreaSizeY/2+140
		g.m.BareClick(x, y)
		return fmt.Sprintf("drop %d,%d", x, y)
	case d.Action.Kind == screen.ActClick:
		act := fmt.Sprintf("click %d,%d", d.Action.X, d.Action.Y)
		if !g.m.RealMenuClick(d.Action.X, d.Action.Y) {
			act += " (refused: no foreground)"
		}
		return act
	case d.Action.Kind == screen.ActKey:
		act := fmt.Sprintf("key 0x%02X", d.Action.VK)
		if !g.m.RealKey(uint16(d.Action.VK)) {
			act += " (refused: no foreground)"
		}
		return act
	}
	return ""
}

// step gates this tick. ok: the holder may Step. unblocked: on the tick the
// gate reopens, how long it was shut (0 otherwise).
func (g *gatekeeper) step(tick uint64, s *percept.Snapshot, arb *arbiter.Arbiter, roster *activity.Roster) (ok bool, unblocked time.Duration) {
	who := holderWho(arb)
	now := time.Now()
	seen := g.sh.Eye.Latest()
	d := g.j.Decide(now, seen, g.needs(who, s, roster))
	g.sh.SetGate(d.Gate())
	if d.Open {
		arb.SetBlocked(false)
		if !g.blockedAt.IsZero() {
			unblocked = now.Sub(g.blockedAt)
			emit(trace.Gate(who, fmt.Sprintf("open after %.1fs: %s", unblocked.Seconds(), d.Why), "", tick))
			g.blockedAt, g.said = time.Time{}, ""
		}
		return true, unblocked
	}
	if g.blockedAt.IsZero() {
		g.blockedAt = now
	}
	arb.SetBlocked(true)
	if d.NewWedge {
		shot := fmt.Sprintf("logs/ui_wedge_%d.png", now.Unix())
		if f, err := os.Create(shot); err == nil {
			_ = png.Encode(f, g.gr.Screenshot())
			f.Close()
		}
		g.logger.Warn("UI WEDGE — janitor actions are not clearing the screen; gating only for 60s",
			"hold", who, "why", d.Why, "screen", shot)
		emit(trace.Gate(who, "ui_wedge: "+d.Why, "", tick))
		g.said = "ui_wedge"
	}
	if d.Act {
		act := g.perform(d)
		g.j.Acted(time.Now())
		g.sh.Kick() // judge the action on the next tick's photograph
		emit(trace.Gate(who, d.Why, act, tick))
		g.said = d.Why
		return false, 0
	}
	why := d.Why
	if d.Wait != "" && d.Wait != "rate" && d.Wait != "awaiting a fresh reading" {
		why += " [" + d.Wait + "]"
	}
	if why != g.said && d.Wait != "rate" && d.Wait != "awaiting a fresh reading" {
		emit(trace.Gate(who, why, "", tick))
		g.said = why
	}
	return false, 0
}

// settle is the startup hygiene by the janitor: before calibration presses its
// probe keys, observe until the screen belief is World with nothing foreign,
// clearing what is seen one action at a time. Bounded; returns actions taken.
func (g *gatekeeper) settle(p *percept.Perceptor) int {
	n := 0
	deadline := time.Now().Add(6 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		s := p.Capture()
		g.sh.Kick()
		g.sh.observe(0, s, "startup")
		seen := g.sh.Eye.Latest()
		d := g.j.Decide(time.Now(), seen, exec.NoHolder)
		if d.Open && seen != nil && exec.Stable(*seen).Mode == screen.World && i >= 3 {
			break
		}
		if d.Act {
			act := g.perform(d)
			g.j.Acted(time.Now())
			emit(trace.Gate("startup", d.Why, act, 0))
			n++
		}
		time.Sleep(150 * time.Millisecond)
	}
	return n
}
