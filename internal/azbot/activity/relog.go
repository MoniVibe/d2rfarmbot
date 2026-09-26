// Relog: the owner's play, verbatim — "exit game, and relog to retake corpse". A death
// leaves the body deep in hostile ground; exiting to the menu and re-entering makes the
// game MATERIALIZE the corpse in town (proven live 2026-07-19: corpse at (6022,4932),
// one tile from spawn, 3 seconds after the Play click). No naked suicide runs, ever.
//
// SESSION-OWNED since docs/AZBOT_V2.md step 8. Relog is no longer an arbiter
// activity: its old Demand is the trigger (Wants), the executive hands a
// granted request to exec.Session, and the session walks the ritual by sight
// in bounded phases (OpenPause → ClickSaveExit → AwaitMenu → ClickPlay →
// AwaitWorld) while nothing below it acts. What stays here is the trigger, the
// backoff that keeps a relog loop from churning worlds forever, and the motor
// half of the session's acts — the one ESC and the menu clicks.
//
// Retired with the synchronous ritual: up to three ESCs per attempt waiting on
// OpenMenus.QuitMenu (dead — it reads false with the pause menu standing, relay
// R2), two attempts per grant with a closing ESC between, ~60s of sleeps inside
// one Step, the MenuSanctionUntil timer, and the unused worldFrozen stride test.
// Costs of a relog are unchanged: the map seed re-rolls (the executive
// refetches map data and the cartographer's seed-namespaced facts start a
// fresh page), and all monsters respawn.
package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
)

type Relog struct {
	nextAt time.Time // earliest next attempt — short after a missed click, long after churn
	fails  int
	failAt time.Time // when the cap filled — it thaws after 5 min (a capped relog
	// left her permanently naked in a live process, run 47)
}

func NewRelog() *Relog { return &Relog{} }

func (rl *Relog) Name() string { return "relog" }

// Wants is the trigger (the old Demand's conditions, verbatim): a naked, living
// character in town whose body is far away or unseen. The reason names why.
func (rl *Relog) Wants(s *percept.Snapshot, now time.Time) (string, bool) {
	if s == nil || !s.Valid || !s.Me.InTown || s.Me.HPPct <= 0 {
		return "", false
	}
	// P-4.8a DEFANGED (12:56): the "poisoned world" relog was built on the
	// wrong diagnosis — the real cause was the shop-detection bug (WARNING 8a),
	// now fixed. It false-fires on intermittent fence-sell ghosts. Ghost logging
	// stays for diagnostics; the relog does not fire on it. An armed girl marches.
	if s.Me.Armed {
		return "", false
	}
	// If the body is HERE in town, Reclaim's walk handles it — relog is for the body
	// that is far away or invisible (its rooms unloaded).
	if s.Me.CorpseFound && chebyshev(s.Me.Pos, s.Me.CorpsePos) <= 150 {
		return "", false
	}
	if s.Me.Level < 3 {
		return "", false // a fresh fist-fighter has no gear to fetch — punching IS her life
	}
	if rl.fails >= 3 && now.Sub(rl.failAt) > 5*time.Minute {
		rl.fails = 0 // the cap thaws: naked-forever is worse than another try
	}
	if now.Before(rl.nextAt) || rl.fails >= 3 {
		return "", false // rate limit: a relog loop would churn worlds forever
	}
	if s.Me.CorpseFound {
		return "naked in town, corpse far", true
	}
	return "naked in town, corpse unseen", true
}

// Outcome books a finished relog into the backoff.
func (rl *Relog) Outcome(e exec.RelogEnd, now time.Time) {
	switch {
	case e.OK():
		rl.fails = 0
		rl.nextAt = now.Add(4 * time.Minute)
		return
	case e.Why == phase.Preempted || e.Why == phase.Refused:
		// F10, or the owner holds the desktop: not the ritual's fault. Wait
		// politely and retry when the game is theirs to give back.
		rl.nextAt = now.Add(2 * time.Minute)
		return
	case e.Why == phase.Precondition && !e.Churned:
		// The screen was not clear for the ESC: the janitor clears it; soon.
		rl.nextAt = now.Add(15 * time.Second)
		return
	case e.Churned:
		// The world churned but never came back — the expensive failure.
		rl.nextAt = now.Add(4 * time.Minute)
	case e.Phase == exec.OpenPause:
		// The pause menu never rose on a clear screen: a deaf game — back off.
		rl.nextAt = now.Add(3 * time.Minute)
	default:
		// Save and Exit missed — the WORLD IS INTACT: retry soon, not in four
		// minutes. The naked-march guards hold everyone else meanwhile.
		rl.nextAt = now.Add(45 * time.Second)
	}
	rl.fails++
	if rl.fails >= 3 {
		rl.failAt = now
	}
}

// Perform carries out one session act with hardware input. False: the motor
// sent nothing (disengaged, or no verified foreground — the owner holds the
// desktop and never gets a stray ESC or click in their window).
func (rl *Relog) Perform(m *motor.Motor, act exec.SessionAct, x, y int) bool {
	switch act {
	case exec.SesEsc:
		// The session's single OpenPause ESC — Relogging's and the WindDown
		// pause rung's, both judged by exec.Session.pauseStep: sent only on a
		// stable Reading that is World with nothing blocking.
		return m.RealEsc()
	case exec.SesClick:
		return m.RealMenuClick(x, y)
	}
	return false
}
