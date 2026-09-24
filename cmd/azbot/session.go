package main

// Layer 0 in the executive (docs/AZBOT_V2.md step 8): the Session FSM
// (internal/azbot/exec) decides; this file feeds it the tick's evidence, turns
// its one act into motor input through Relog.Perform, and books a finished
// relog into Relog's backoff. While the session owns the tick nothing below it
// acts — no arbitration, no gate, no monitors. The live executive and the
// -relogtest drill drive it through the same code.

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// relogNowPath: the owner's on-demand relog — create this file while the bot
// runs (engaged, in game) and the next ready tick requests a relog.
const relogNowPath = "logs/relog.now"

// stopNowPath: the owner's graceful stop — create this file while the bot runs
// and the session winds down (exec.Session WindDown): to town or a quiet field,
// then a normal exit. A stale one is removed at startup.
const stopNowPath = "logs/stop.now"

type sessionDriver struct {
	logger *slog.Logger
	m      *motor.Motor
	gr     *game.MemoryReader
	sh     *shadow
	ses    *exec.Session
	relog  *activity.Relog
	recall *activity.Recall // the wind-down's town road: Spent is the empty-tome rung trigger (nil: never spent)
	// onChange runs after every layer-0 transition with the new ses= value
	// (the -relogtest drill photographs each phase). nil live.
	onChange func(ses string)
	// onEnd hears every finished relog after Relog's backoff booked it. nil live.
	onEnd func(e exec.RelogEnd)

	refusal   string // the last refused request's reason (one log line per change)
	refusalAt time.Time

	out exec.SessionOut // the last step's answer (the executive reads Wind)
}

func newSessionDriver(logger *slog.Logger, m *motor.Motor, gr *game.MemoryReader, sh *shadow, relog *activity.Relog) *sessionDriver {
	ses := exec.NewSession()
	ses.Trace = emit
	ses.FleeFloor = activity.FleeFloor // the wind-down pauses under Flee's own floor
	ses.Rung = func(line string) { logger.Warn(line) }
	return &sessionDriver{logger: logger, m: m, gr: gr, sh: sh, ses: ses, relog: relog}
}

func (d *sessionDriver) in(s *percept.Snapshot) exec.SessionIn {
	return exec.SessionIn{Now: time.Now(), Engaged: d.m.Engage.Engaged(), Focused: d.m.GameFocused(),
		Valid: s.Valid, Seed: uint64(d.gr.MapSeed()), Seen: d.sh.Eye.Latest(),
		InTown: s.Valid && s.Me.InTown, Hot: s.Valid && hot(s), HPPct: s.Me.HPPct,
		RecallSpent: d.recall != nil && d.recall.Spent()}
}

// hot: a living monster (percept drops the dying and the dead) within
// exec.WindRadius tiles — the field is not safe to end the run in.
func hot(s *percept.Snapshot) bool {
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= exec.WindRadius {
			return true
		}
	}
	return false
}

// stop asks the session for the safe end of the run; logged once.
func (d *sessionDriver) stop(tick uint64, why string) {
	d.ses.Tick = tick
	if d.ses.Stop(time.Now(), why) {
		d.logger.Warn("SESSION: wind-down requested — to town or a quiet field, then exit", "why", why,
			"budget", d.ses.WindCap, "pausefailsafe", d.ses.PauseFailsafe, "fleefloor", d.ses.FleeFloor)
	}
}

// request asks the session for a relog; true when it took it. A refusal (the
// screen is not ready yet) is logged once per change of reason, or every 15s.
func (d *sessionDriver) request(why string) bool {
	ok, refusal := d.ses.Request(time.Now(), why, uint64(d.gr.MapSeed()), d.sh.Eye.Latest())
	if ok {
		d.refusal = ""
		d.logger.Warn("SESSION: relog begins", "why", why, "seed", d.gr.MapSeed())
		return true
	}
	if refusal != d.refusal || time.Since(d.refusalAt) > 15*time.Second {
		d.logger.Info("session: relog wanted, not yet", "why", why, "waiting", refusal)
		d.refusal, d.refusalAt = refusal, time.Now()
	}
	return false
}

// step runs layer 0 for this tick; true when the session owns it.
func (d *sessionDriver) step(tick uint64, s *percept.Snapshot) bool {
	d.ses.Tick = tick
	before := d.ses.String()
	out := d.ses.Step(d.in(s))
	d.out = out
	if out.Say != "" {
		d.logger.Warn(out.Say)
	}
	if out.Act != exec.SesNoAct {
		ok := d.relog.Perform(d.m, out.Act, out.At.X, out.At.Y)
		d.ses.Acted(time.Now(), ok)
		d.sh.Kick() // judge the act on the next tick's photograph
		act := out.Act.String()
		if out.Act == exec.SesClick {
			act = fmt.Sprintf("click %d,%d", out.At.X, out.At.Y)
		}
		if !ok {
			act += " (refused: no foreground)"
		}
		d.logger.Info("session: act", "ses", d.ses.String(), "act", act)
	}
	if e := out.Ended; e != nil {
		d.relog.Outcome(*e, time.Now())
		if e.OK() {
			d.logger.Warn("SESSION: relog done", "took", e.Took.Round(100*time.Millisecond), "detail", e.Detail)
		} else {
			d.logger.Warn("SESSION: relog failed", "why", e.Why.String(), "phase", e.Phase.String(),
				"churned", e.Churned, "took", e.Took.Round(100*time.Millisecond), "detail", e.Detail)
		}
		if d.onEnd != nil {
			d.onEnd(*e)
		}
	}
	if now := d.ses.String(); now != before && d.onChange != nil {
		d.onChange(now)
	}
	return out.Owns
}
