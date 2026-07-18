// Relog: the owner's play, verbatim — "exit game, and relog to retake corpse". A death
// leaves the body deep in hostile ground; exiting to the menu and re-entering makes the
// game MATERIALIZE the corpse in town (proven live 2026-07-19: corpse at (6022,4932),
// one tile from spawn, 3 seconds after the Play click). No naked suicide runs, ever.
//
// The ritual runs SYNCHRONOUSLY inside one Step: once Save+Exit is clicked the world
// unloads, snapshots go invalid, and the executive would stall anyway — there is
// nothing else to do until the new game gates in. The kill-switch is polled between
// waits. Costs: the map seed re-rolls (the executive refetches map data and the
// cartographer's seed-namespaced facts start a fresh page), and all monsters respawn.
package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Menu button positions in SCREENSHOT pixels (1920x1050 physical client), measured
// from the relog drill's calibration screenshots 2026-07-19.
const (
	relogExitBtnX, relogExitBtnY = 958, 822 // pause menu: Save and Exit
	relogPlayBtnX, relogPlayBtnY = 922, 822 // char select: Normal (launches the selected char)
)

type Relog struct {
	lastAt time.Time
	fails  int
}

func NewRelog() *Relog { return &Relog{} }

func (rl *Relog) Name() string { return "relog" }

func (rl *Relog) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.Armed || s.Me.HPPct <= 0 {
		return nil
	}
	// If the body is HERE in town, Reclaim's walk handles it — relog is for the body
	// that is far away or invisible (its rooms unloaded).
	if s.Me.CorpseFound && chebyshev(s.Me.Pos, s.Me.CorpsePos) <= 150 {
		return nil
	}
	if s.Me.Level < 3 {
		return nil // a fresh fist-fighter has no gear to fetch — punching IS her life
	}
	if time.Since(rl.lastAt) < 4*time.Minute || rl.fails >= 3 {
		return nil // rate limit: a relog loop would churn worlds forever
	}
	return &arbiter.Demand{Who: rl.Name(), Class: arbiter.ClassRecover,
		Urgency: 0.95, // under Respawn (1.0), over Reclaim (0.9)
		Commit:  arbiter.Commitment{MinHold: 30 * time.Second}}
}

func (rl *Relog) Step(ctx *Ctx) Verdict {
	rl.lastAt = time.Now()
	// Phase 1: pause menu → Save and Exit.
	ctx.M.RealEsc()
	time.Sleep(900 * time.Millisecond)
	ctx.M.RealMenuClick(relogExitBtnX, relogExitBtnY)
	// Phase 2: wait for the world to unload.
	gone := false
	for i := 0; i < 30 && ctx.M.Engage.Engaged(); i++ {
		time.Sleep(500 * time.Millisecond)
		if !ctx.P.Capture().Valid {
			gone = true
			break
		}
	}
	if !gone {
		// The click missed (or the menu was already open and ESC closed it) — close
		// any half-open menu and report honestly.
		ctx.M.RealEsc()
		rl.fails++
		return Abandoned
	}
	time.Sleep(3 * time.Second) // char select settles
	// Phase 3: Play (Normal difficulty launches the selected character).
	ctx.M.RealMenuClick(relogPlayBtnX, relogPlayBtnY)
	// Phase 4: gate back in.
	for i := 0; i < 45 && ctx.M.Engage.Engaged(); i++ {
		time.Sleep(1 * time.Second)
		if ctx.P.Gate().OK() {
			rl.fails = 0
			return Done // new world; the executive refetches the map, Reclaim takes the town corpse
		}
	}
	rl.fails++
	return Abandoned
}
