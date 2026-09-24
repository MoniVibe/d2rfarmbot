package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/policy"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// STRIKE TELEMETRY (the owner's 2026-09-24 Carnage build, first live run):
// one ledger outcome per strike —
//
//	verb=strike  ev="skill=<name> mouse=left|right target=<id> d=<dist> result=hit|whiff|deaf|overkill [telemetry]"
//
// An issued strike's line is written when its 1.5s telemetry window closes
// (strikelearn.go) and carries the splash account after the verdict:
// aim=(x,y) n1..n6 hits= kills= hitd= killd= hpLoss= mp= next=. A whiff is
// written at once, without telemetry.
//
// and one per target lock when it ends —
//
//	verb=fight   ev="fight summary: target=<id> strikes=N hits=M duration=Xs killed=true|false"
//
// The verdict is policy.Judge: "hit" = the struck unit's Mode ENTERS
// GettingHit or KnockedBack after the click (a rising edge — one edge credits
// one strike), or the unit turns up as a corpse, or it vanished from the live
// list after an in-reach strike. "overkill" = its death was already credited
// to an earlier strike (not counted by the left audit). "whiff" = no click
// reached a monster (hover never confirmed / no clickable world); "deaf" = the
// click went out and nothing answered inside strikeEvidenceWindow.

// strikeEvidenceWindow bounds how long a strike waits for its evidence.
const strikeEvidenceWindow = time.Second

// strikeRec is one issued strike awaiting its evidence.
type strikeRec struct {
	target   data.UnitID
	lock     data.UnitID // the Fight lock it was thrown under (summary attribution)
	skill    string
	mouse    string
	d        int
	at       time.Time
	lastMode uint32 // the target's Mode last seen (edge detection)
	left     bool   // a primary-left strike: feeds the left audit
	// tel: the strike's telemetry window (strikelearn.go); its close writes
	// the strike line together with this verdict. nil: a whiff, written at once.
	tel *learn.Strike
}

// fightTally is the running account of one target lock.
type fightTally struct {
	target        data.UnitID
	start         time.Time
	strikes, hits int
}

// handOf names the hand a strike key fires: key 0 is the LEFT button (the
// owner's left skill, or the basic Attack), any other key a proven RIGHT
// binding.
func handOf(c *combat.Capability, key byte) (name, mouse string, leftPrimary bool) {
	if key == 0 {
		if c != nil && c.Left != nil {
			return c.Left.Name, "left", c.Left.Primary()
		}
		return "Attack", "left", false
	}
	if c != nil {
		for _, b := range []*combat.Binding{c.LeapAttack, c.DoubleSwing, c.Combat, c.Contact, c.Reach, c.Throw} {
			if b != nil && b.Key == key && b.Skill != 0 {
				return combat.SkillName(b.Skill), "right", false
			}
		}
		for _, b := range c.Proven {
			if b.Key == key {
				return combat.SkillName(b.Skill), "right", false
			}
		}
	}
	return fmt.Sprintf("key#%d", int(key)), "right", false
}

// noteStrike records one strike attempt. issued=false is a whiff (resolved at
// once); an issued strike waits in f.open for resolveStrikes.
func (f *Fight) noteStrike(ctx *Ctx, target data.UnitID, key byte, issued bool) {
	f.noteStrikeAt(ctx, target, key, issued, nil)
}

// noteStrikeAt is noteStrike with the strike's impact point (a leap's ground
// aim; nil = the target's position).
func (f *Fight) noteStrikeAt(ctx *Ctx, target data.UnitID, key byte, issued bool, aim *data.Position) {
	name, mouse, left := handOf(ctx.Cap, key)
	r := strikeRec{target: target, lock: f.tally.target, skill: name, mouse: mouse, d: -1, at: time.Now(), left: left}
	if s := ctx.Snap; s != nil {
		for _, e := range s.Enemies {
			if e.ID == target {
				r.d, r.lastMode = chebyshev(s.Me.Pos, e.Pos), e.Mode
				break
			}
		}
	}
	if f.tally.target != 0 {
		f.tally.strikes++
	}
	if !issued {
		f.closeStrike(ctx, r, "whiff")
		return
	}
	f.telemetryBegin(ctx, &r, key, aim)
	f.open = append(f.open, r)
	if len(f.open) > 8 { // never an unbounded queue: the oldest is out of its window anyway
		f.closeStrike(ctx, f.open[0], "deaf")
		f.open = f.open[1:]
	}
}

// resolveStrikes judges the open strikes against this tick's snapshot.
func (f *Fight) resolveStrikes(ctx *Ctx, now time.Time) {
	defer f.telemetryObserve(ctx, now) // the verdicts first, then the windows
	if len(f.open) == 0 {
		if len(f.deathTaken) > 64 {
			f.deathTaken = nil
		}
		return
	}
	live := map[data.UnitID]uint32{}
	if s := ctx.Snap; s != nil {
		for _, e := range s.Enemies {
			live[e.ID] = e.Mode
		}
	}
	flinched := map[data.UnitID]bool{} // one flinch edge credits ONE strike
	var dead map[data.UnitID]bool
	keep := f.open[:0]
	for _, r := range f.open {
		m, present := live[r.target]
		seen := policy.Seen{Present: present, Mode: m, PrevMode: r.lastMode,
			FlinchTaken: flinched[r.target], DeathTaken: f.deathTaken[r.target],
			InReach: r.d >= 0 && r.d <= policy.MeleeReach,
			Expired: now.Sub(r.at) > strikeEvidenceWindow}
		if !present && !seen.DeathTaken {
			if dead == nil {
				dead = corpses(ctx)
			}
			seen.Corpse = dead[r.target]
		}
		v := policy.Judge(seen)
		if present {
			r.lastMode = m
			if v == policy.Hit {
				flinched[r.target] = true
			}
		} else if v == policy.Hit {
			if f.deathTaken == nil {
				f.deathTaken = map[data.UnitID]bool{}
			}
			f.deathTaken[r.target] = true
		}
		if v == policy.Pending {
			keep = append(keep, r)
			continue
		}
		f.closeStrike(ctx, r, v.String())
	}
	f.open = keep
}

// corpses: the unit ids in Death/Dead mode (the snapshot drops them, so the
// raw monster list is the only place a kill shows).
func corpses(ctx *Ctx) map[data.UnitID]bool {
	out := map[data.UnitID]bool{}
	if ctx.GR == nil {
		return out
	}
	for _, m := range ctx.GR.GetData().Monsters {
		if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead {
			out[m.UnitID] = true
		}
	}
	return out
}

// closeStrike writes the strike's outcome and feeds the tally and the audit.
func (f *Fight) closeStrike(ctx *Ctx, r strikeRec, res string) {
	result := verbs.ResDone
	switch res {
	case "whiff":
		result = verbs.ResWhiff
	case "deaf":
		result = verbs.ResDeaf
	}
	if r.tel != nil {
		// The telemetry window writes the line (with the splash account)
		// once both it and this verdict are in.
		r.tel.Verdict = res
		f.emitStrike(ctx, r.tel)
	} else {
		ctx.Led.Append(verbs.Outcome{Verb: "strike", Holder: f.Name(), Target: fmt.Sprintf("unit=%d", r.target),
			Result: result, Evidence: fmt.Sprintf("skill=%s mouse=%s target=%d d=%d result=%s",
				r.skill, r.mouse, int(r.target), r.d, res)})
	}
	if res == "hit" && r.lock != 0 && r.lock == f.tally.target {
		f.tally.hits++
	}
	// A whiff never reached a monster and an overkill met a body another
	// strike had already killed: neither is the left skill's evidence or its
	// silence.
	if r.left && res != "whiff" && res != "overkill" {
		if leftAudit.Resolve(res == "hit", time.Now()) {
			ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("left skill %s benched: %d silent left strikes — Double Swing carries %s, then the left re-proves",
					r.skill, policy.DeafLimit, policy.BenchFor)})
		}
	}
}

// approach closes a gap on foot (policy.Approach): the left skill cannot
// strike beyond reach without a hover-confirmed monster under the cursor. A
// stride that made ground counts as liveness for the P-2.10 watchdog — the
// lock is being worked, not held hostage; a stride that gained nothing does
// not, so a walled approach still quarantines.
func (f *Fight) approach(ctx *Ctx) {
	o := verbs.Stride{To: f.targetPos, Hold: 700 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
	if o.Result == verbs.ResDone {
		f.watchSince = time.Now()
	}
}

// noteLock closes the previous lock's summary and opens the next when the
// Fight's target changed (or went to none).
func (f *Fight) noteLock(ctx *Ctx, now time.Time) {
	if f.tally.target == f.target {
		return
	}
	if t := f.tally; t.target != 0 && (t.strikes > 0 || now.Sub(t.start) > 2*time.Second) {
		killed := f.deathTaken[t.target]
		if !killed {
			killed = corpses(ctx)[t.target]
		}
		ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Target: fmt.Sprintf("unit=%d", t.target),
			Result: verbs.ResDone, Evidence: fmt.Sprintf("fight summary: target=%d strikes=%d hits=%d duration=%.1fs killed=%t",
				int(t.target), t.strikes, t.hits, now.Sub(t.start).Seconds(), killed)})
	}
	f.tally = fightTally{}
	if f.target != 0 {
		f.tally = fightTally{target: f.target, start: now}
	}
}
