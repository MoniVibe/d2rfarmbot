package activity

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/policy"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// THE DYNAMIC STRIKE, wired (combat/learn + combat/policy.Decide): every
// strike decision is written —
//
//	verb=choose  ev="skill=leap bucket=4-6/4-5 u_leap=1.31 u_carn=0.72 n=3 why=model aim=(x,y) cover=6 r=3"
//
// every issued strike opens a 1.5s telemetry window whose close writes the
// strike line (the per-target verdict plus the splash account) —
//
//	verb=strike  ev="skill=.. mouse=.. target=.. d=.. result=hit aim=(x,y) n1=.. n3=.. n5=.. hits=K kills=J hitd=.. killd=.. hpLoss=.. mp=.. next=0.42s"
//
// and every engagement (a pack fought until nobody is within 12) ends with
//
//	verb=fight   ev="engage summary: size=N cleared=M dur=Xs hpLoss=Y mp=Z leaps=a carnage=b ds=c basic=d"
//	verb=fight   ev="leap aoe: r_est=3 measured=false prior=3 d0=h/x d1=h/x ..."   (when it leapt)
//
// The model persists per character + skill set (memory, ScopeForever,
// provenance measured) every learn.SaveEvery samples and at every
// engagement end; the Scribe fsyncs within a second.

// combatCfg is -combatlearn / -explore.
var combatCfg = policy.Config{Learn: true, Explore: 0.1}

// SetCombatLearn applies -combatlearn (off: deterministic priors, no
// exploration — telemetry is still measured and stored) and -explore.
func SetCombatLearn(on bool, explore float64) {
	combatCfg = policy.Config{Learn: on, Explore: explore}
}

// strikeRand is the exploration draw (a var so tests can pin it).
var strikeRand = rand.Float64

// learner is the session's estimator (one per character + skill set).
var learner *learn.Learner

// skillSetOf names the kit for the store key.
func skillSetOf(c *combat.Capability) string {
	var left, leap, swing int
	if c != nil {
		if c.Left != nil && c.Left.Primary() {
			left = int(c.Left.Skill)
		}
		if c.LeapAttack != nil {
			leap = int(c.LeapAttack.Skill)
		}
		if c.DoubleSwing != nil {
			swing = int(c.DoubleSwing.Skill)
		}
	}
	return learn.SkillSet(left, leap, swing)
}

// ensureLearner loads (or switches to) the model for this character and kit.
func ensureLearner(ctx *Ctx) *learn.Learner {
	char := ""
	if ctx.GR != nil {
		char = ctx.GR.GetData().PlayerUnit.Name
	}
	key := learn.StoreKey(char, skillSetOf(ctx.Cap))
	if learner != nil && learner.Key() == key {
		return learner
	}
	if learner != nil {
		learner.Save()
	}
	var kv learn.KV
	if ctx.Mem != nil {
		kv = ctx.Mem
	}
	learner = learn.NewLearner(kv, key)
	if ctx.Led != nil {
		ctx.Led.Append(verbs.Outcome{Verb: "combatlearn", Holder: "fight", Result: verbs.ResDone,
			Evidence: fmt.Sprintf("combat model %s loaded: learn=%t explore=%.2f leap r=%d", key, combatCfg.Learn, combatCfg.Explore, learner.M.AoERadius())})
	}
	return learner
}

// learnSkill maps a strike key onto the estimator's skill name.
func learnSkill(c *combat.Capability, key byte) string {
	if key == 0 {
		if c != nil && c.Left.Primary() && !leftAudit.Benched(time.Now()) {
			return learn.Carnage
		}
		return learn.Basic
	}
	if c != nil {
		if c.LeapAttack != nil && key == c.LeapAttack.Key {
			return learn.Leap
		}
		if c.DoubleSwing != nil && key == c.DoubleSwing.Key {
			return learn.Swing
		}
	}
	return learn.Basic
}

func lpt(p data.Position) learn.Pt { return learn.Pt{X: p.X, Y: p.Y} }

// monsOf is the tick's monsters for the telemetry: the live list plus the
// corpses the raw monster list still shows.
func monsOf(ctx *Ctx, s *percept.Snapshot) []learn.Mon {
	out := make([]learn.Mon, 0, len(s.Enemies))
	for _, e := range s.Enemies {
		out = append(out, learn.Mon{ID: uint32(e.ID), P: lpt(e.Pos), Mode: e.Mode})
	}
	for id := range corpses(ctx) {
		out = append(out, learn.Mon{ID: uint32(id), Corpse: true})
	}
	return out
}

// nearCount is the engagement's size measure: enemies within EngageRadius.
func nearCount(s *percept.Snapshot) int {
	n := 0
	for _, e := range s.Enemies {
		if sighted(s, e) && chebyshev(s.Me.Pos, e.Pos) <= learn.EngageRadius {
			n++
		}
	}
	return n
}

// meleeDecide runs the dynamic strike policy on the proven capability and
// maps the answer onto a key: 0 for the left hand (and the plain attack),
// the proven right binding's key otherwise. to is the struck position when
// the caller knows it (nil: the nearest sighted enemy). explore=false pins
// the decision (no ε draw) — the blind strike sites and the pre-computed
// ring key use it.
func meleeDecide(ctx *Ctx, s *percept.Snapshot, dist int, to *data.Position, hoverOK, leapOK, explore bool) (policy.Decision, byte) {
	if ctx == nil || ctx.Cap == nil || s == nil {
		return policy.Decision{Kind: policy.Basic, Skill: learn.Basic}, 0
	}
	c := ctx.Cap
	combatIsLeap := c.Combat != nil && c.LeapAttack != nil && c.Combat.Skill == c.LeapAttack.Skill
	combatB := c.Combat
	if combatB == nil {
		combatB = c.Contact
	}
	in := policy.Inputs{
		LeftProven:   c.Left.Primary(),
		LeftBenched:  leftAudit.Benched(time.Now()),
		LeapProven:   c.LeapAttack != nil,
		SwingProven:  c.DoubleSwing != nil,
		CombatProven: combatB != nil,
		CombatIsLeap: combatIsLeap,
		Dist:         dist,
		MPPct:        s.Me.MPPct,
		HPPct:        s.Me.HPPct,
		LeapReady:    leapAttackReady(s),
		LeapBlocked:  !leapOK || time.Now().Before(vaultHungerUntil),
		HoverOK:      hoverOK,
		LeapCooling:  leapClock.Cooling(time.Now()),
		Me:           lpt(s.Me.Pos),
	}
	nearest := 1 << 30
	for _, e := range s.Enemies {
		if !sighted(s, e) {
			continue
		}
		in.Enemies = append(in.Enemies, lpt(e.Pos))
		if d := chebyshev(s.Me.Pos, e.Pos); to == nil && d < nearest {
			nearest, in.Target = d, lpt(e.Pos)
		}
	}
	switch {
	case to != nil:
		in.Target = lpt(*to)
	case nearest == 1<<30:
		in.Target = learn.Pt{X: s.Me.Pos.X + dist, Y: s.Me.Pos.Y}
	}
	if ctx.Grid != nil {
		g := ctx.Grid
		in.Walled = func(a, b learn.Pt) bool {
			return !losClear(g, data.Position{X: a.X, Y: a.Y}, data.Position{X: b.X, Y: b.Y})
		}
	}
	if len(s.Missiles) > 0 {
		ms := s.Missiles
		in.Hazard = func(p learn.Pt) bool {
			for _, m := range ms {
				if learn.Cheb(p, lpt(m.Pos)) <= 1 {
					return true
				}
			}
			return false
		}
	}
	cfg := combatCfg
	if !explore {
		cfg.Explore = 0
	}
	var est policy.Estimator
	if learner != nil {
		est = learner.M
	}
	dec := policy.Decide(in, est, cfg, strikeRand())
	switch dec.Kind {
	case policy.Leap:
		return dec, c.LeapAttack.Key
	case policy.Swing:
		return dec, c.DoubleSwing.Key
	case policy.Combat:
		return dec, combatB.Key
	}
	return dec, 0
}

// meleeStrike is meleeDecide pinned (no exploration), as a kind and a key.
func meleeStrike(ctx *Ctx, s *percept.Snapshot, dist int, to *data.Position, hoverOK, leapOK bool) (policy.Kind, byte) {
	d, key := meleeDecide(ctx, s, dist, to, hoverOK, leapOK, false)
	return d.Kind, key
}

// hungerOverride: the movement leap's mana hunger turns any right-hand
// strike into the left hand (key 0); the decision is rewritten to match so
// its log never names a skill that did not fire.
func hungerOverride(ctx *Ctx, d *policy.Decision, key byte) byte {
	if key == 0 || !time.Now().Before(vaultHungerUntil) {
		return key
	}
	d.Kind, d.Skill, d.Why = policy.Basic, learnSkill(ctx.Cap, 0), "forced"
	if d.Skill == learn.Carnage {
		d.Kind = policy.Left
	}
	return 0
}

// logChoice writes one strike decision.
func (f *Fight) logChoice(ctx *Ctx, d *policy.Decision) {
	if d == nil || ctx.Led == nil {
		return
	}
	ctx.Led.Append(verbs.Outcome{Verb: "choose", Holder: f.Name(), Result: verbs.ResDone, Evidence: d.Line()})
}

// telemetryBegin opens the strike's follow-up window (issued strikes only).
func (f *Fight) telemetryBegin(ctx *Ctx, r *strikeRec, key byte, aim *data.Position) {
	s := ctx.Snap
	if s == nil || !s.Valid {
		return
	}
	l := ensureLearner(ctx)
	impact := lpt(s.Me.Pos)
	switch {
	case aim != nil:
		impact = lpt(*aim)
	default:
		for _, e := range s.Enemies {
			if e.ID == r.target {
				impact = lpt(e.Pos)
				break
			}
		}
	}
	r.tel = l.Strike(&learn.Strike{Skill: learnSkill(ctx.Cap, key), At: r.at, Me: lpt(s.Me.Pos), Aim: impact,
		Target: uint32(r.target), HP0: s.Me.HPPct, MP0: s.Me.MPPct}, monsOf(ctx, s), nearCount(s))
	if f.telPrefix == nil || len(f.telPrefix) > 64 {
		// Bounded: a line whose verdict never came (a lost strike queue)
		// is dropped rather than held forever.
		f.telPrefix = map[*learn.Strike]string{}
	}
	f.telPrefix[r.tel] = fmt.Sprintf("skill=%s mouse=%s target=%d d=%d", r.skill, r.mouse, int(r.target), r.d)
}

// telemetryObserve feeds the tick to the learner, writes the strike lines
// whose window AND verdict are both in, and the engagement summary.
func (f *Fight) telemetryObserve(ctx *Ctx, now time.Time) {
	s := ctx.Snap
	if learner == nil || s == nil || !s.Valid || (learner.T.Open() == 0 && !learner.E.Active) {
		return
	}
	closed, sum := learner.Observe(now, s.Me.HPPct, s.Me.MPPct, monsOf(ctx, s), nearCount(s))
	for _, st := range closed {
		f.emitStrike(ctx, st)
	}
	if sum != nil {
		ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResDone, Evidence: sum.String()})
		if sum.Leaps > 0 {
			ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResDone, Evidence: learner.AoELine()})
		}
	}
}

// emitStrike writes a strike's one line once its telemetry window closed
// and its verdict is known (whichever comes second writes it).
func (f *Fight) emitStrike(ctx *Ctx, st *learn.Strike) {
	if st == nil || !st.Closed || st.Verdict == "" {
		return
	}
	prefix, ok := f.telPrefix[st]
	if !ok {
		return // already written
	}
	delete(f.telPrefix, st)
	result := verbs.ResDone
	if st.Verdict == "deaf" {
		result = verbs.ResDeaf
	}
	ctx.Led.Append(verbs.Outcome{Verb: "strike", Holder: f.Name(), Target: fmt.Sprintf("unit=%d", st.Target),
		Result: result, Evidence: fmt.Sprintf("%s result=%s %s", prefix, st.Verdict, st.Fields())})
}
