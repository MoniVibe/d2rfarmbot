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

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// Menu button positions in SCREENSHOT pixels (1920x1050 physical client), measured
// from the relog drill's calibration screenshots 2026-07-19.
const (
	relogExitBtnX, relogExitBtnY = 958, 822 // pause menu: Save and Exit
	relogPlayBtnX, relogPlayBtnY = 922, 822 // char select: Normal (launches the selected char)
)

type Relog struct {
	nextAt time.Time // earliest next attempt — short after a missed click, long after churn
	fails  int
	failAt time.Time // when the cap filled — it thaws after 5 min (a capped relog
	// left her permanently naked in a live process, run 47)
}

func NewRelog() *Relog { return &Relog{} }

func (rl *Relog) Name() string { return "relog" }

func (rl *Relog) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	// P-4.8a DEFANGED (12:56): the "poisoned world" relog was built on the
	// wrong diagnosis — the real cause was the shop-detection bug (WARNING 8a),
	// now fixed. It false-fires on intermittent fence-sell ghosts, outranks the
	// march by class, and cannot even cure (relog's Save+Exit coords are wrong
	// for this 1536x840 client — a REAL handoff item). Ghost logging stays for
	// diagnostics; the relog no longer bids on it. An armed girl marches.
	if s.Me.Armed {
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
	if rl.fails >= 3 && time.Since(rl.failAt) > 5*time.Minute {
		rl.fails = 0 // the cap thaws: naked-forever is worse than another try
	}
	if time.Now().Before(rl.nextAt) || rl.fails >= 3 {
		return nil // rate limit: a relog loop would churn worlds forever
	}
	return &arbiter.Demand{Who: rl.Name(), Class: arbiter.ClassRecover,
		Urgency: 0.95, // under Respawn (1.0), over Reclaim (0.9)
		Commit:  arbiter.Commitment{MinHold: 30 * time.Second}}
}

// worldFrozen is WARNING 4's shadow test, MULTI-BEARING: a fence refuses one
// direction (measured 08:50: +3,+3 at the spawn nook failed every probe and
// the false "frozen" skipped relog's ESC through two whole grants); the pause
// menu refuses ALL of them. Three bearings 120° apart; frozen only when every
// one is refused.
func (rl *Relog) worldFrozen(ctx *Ctx) bool {
	s := ctx.Snap
	if s == nil || !s.Valid {
		return false
	}
	for _, d := range []data.Position{{X: 4, Y: 4}, {X: -5, Y: 1}, {X: 1, Y: -5}} {
		o := verbs.Stride{To: data.Position{X: s.Me.Pos.X + d.X, Y: s.Me.Pos.Y + d.Y},
			Hold: 400 * time.Millisecond, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, "relog/shadow")
		if o.Result == verbs.ResDone {
			return false // the world moves — no menu owns it
		}
	}
	return true
}

func (rl *Relog) Step(ctx *Ctx) Verdict {
	// Phase 1: pause menu → Save and Exit. TWO tries per grant (run 24: one missed
	// click abandoned the whole recovery, the 4-minute rate limit left her naked in
	// town, and Return nearly portaled her back into the swarm bare-fisted).
	gone := false
	for attempt := 0; attempt < 2 && !gone; attempt++ {
		// WARNING 4: a byte-blind menu may ALREADY be up (a swap mid-relog left
		// one, measured 08:41→08:46: the blind ESC then CLOSED it, the Save+Exit
		// click fell into the world, and the between-attempt "cleanup" ESC
		// re-raised it — the parity stuck inverted through three straight
		// abandons). Test the world first; ESC only when it still moves.
		if !rl.worldFrozen(ctx) {
			ctx.M.RealEsc()
			time.Sleep(900 * time.Millisecond)
		}
		ctx.M.RealMenuClick(relogExitBtnX, relogExitBtnY)
		// Phase 2: wait for the world to unload.
		for i := 0; i < 20 && ctx.M.Engage.Engaged(); i++ {
			time.Sleep(500 * time.Millisecond)
			if !ctx.P.Capture().Valid {
				gone = true
				break
			}
		}
		if !gone {
			time.Sleep(600 * time.Millisecond) // no blind ESC — the next attempt's shadow test decides
		}
	}
	if !gone {
		// Both clicks missed — the WORLD IS INTACT (no churn happened): retry soon,
		// not in four minutes. The naked-march guards hold everyone else meanwhile.
		rl.fails++
		rl.nextAt = time.Now().Add(45 * time.Second)
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
			rl.nextAt = time.Now().Add(4 * time.Minute)
			return Done // new world; the executive refetches the map, Reclaim takes the town corpse
		}
	}
	// The world churned but never gated back — the expensive failure: full cooldown.
	rl.fails++
	rl.nextAt = time.Now().Add(4 * time.Minute)
	return Abandoned
}
