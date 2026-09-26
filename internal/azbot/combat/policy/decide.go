package policy

import (
	"fmt"
	"math"
	"sort"

	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
)

// THE DYNAMIC STRIKE (the owner, 2026-09-24): at every strike decision two
// options compete — LEAP (Leap Attack at the ground point covering the most
// enemies within the learned splash radius) and MELEE (Carnage on the target;
// Double Swing / plain attack when Carnage is out) — on
//
//	U(skill) = E[kills/sec] − λ·E[hpLoss/sec] − μ·E[mana/action]
//
// read from the estimator cell (skill, cluster bucket at the impact point,
// distance bucket). Carnage's walk-in to melee range is priced into its
// distance buckets (learn.PriorFor adds the walk time to the action time;
// measured cells include it because time-to-next-action does).
const (
	// Lambda: kills/sec one HP-percent-per-second of damage taken is worth at
	// full life; scaled by 100/HP% — the lower the life, the dearer the blood.
	Lambda = 0.1
	// Mu: kills/sec one mana-percent spent per action is worth at a full
	// pool; scaled by 100/MP% — a dry pool makes the leap expensive.
	Mu = 0.004
	// LeapRange: the farthest landing (Chebyshev tiles) the aim considers.
	LeapRange = 9
	// LeapMinHop: a landing nearer than this is not a leap.
	LeapMinHop = 2
	// ExploreHalf: ε halves when the less-sampled option's cell holds this
	// many samples (ε = Explore·H/(H+n)).
	ExploreHalf = 30
	// ExploreMinHP: never explore below this life percent.
	ExploreMinHP = 50
	// aimPool: the nearest enemies the landing search considers (pairwise
	// midpoints make it quadratic).
	aimPool = 24
)

// Config is the learning switchboard (-combatlearn, -explore).
type Config struct {
	Learn   bool    // false: deterministic priors, no exploration
	Explore float64 // ε at zero samples (0.1 by default)
	// LeapFirst: a feasible leap always wins (owner, 2026-09-25, Leap-only spec:
	// "it should refrain from regular attacking and use leap attack, it damages
	// more and does aoe").
	LeapFirst bool
}

// Estimator is what Decide reads (a *learn.Model; nil = pure priors).
type Estimator interface {
	Estimate(skill string, c, d int) learn.Estimate
	AoERadius() int
}

// Option is one candidate strike with its bucket and utility.
type Option struct {
	Kind  Kind
	Skill string   // learn skill name
	Aim   learn.Pt // impact point
	C, D  int      // cluster / distance bucket
	Cover int      // leap: enemies within the splash radius of Aim
	U     float64
	N     int    // samples in the cell
	OK    bool   // feasible
	Veto  string // why not (OK=false)
}

// Decision is Decide's answer.
type Decision struct {
	Kind   Kind
	Skill  string
	Aim    learn.Pt
	Leap   Option
	Melee  Option
	Radius int    // splash radius the leap aim used
	Why    string // explore | model | prior | forced
}

// Chosen is the option Decide picked.
func (d Decision) Chosen() Option {
	if d.Kind == Leap {
		return d.Leap
	}
	return d.Melee
}

// Line is the decision's log form:
//
//	skill=leap bucket=4-6/4-5 u_leap=1.31 u_carn=0.72 n=3 why=model aim=(x,y) cover=6 r=3
func (d Decision) Line() string {
	c := d.Chosen()
	ul := "-"
	if d.Leap.OK {
		ul = fmt.Sprintf("%.2f", d.Leap.U)
	}
	s := fmt.Sprintf("skill=%s bucket=%s u_leap=%s u_carn=%.2f n=%d why=%s",
		d.Skill, learn.BucketLabel(c.C, c.D), ul, d.Melee.U, c.N, d.Why)
	if d.Melee.Skill != learn.Carnage {
		s += " melee=" + d.Melee.Skill
	}
	if d.Kind == Approach {
		s += " act=approach"
	}
	if d.Leap.OK || d.Kind == Leap {
		s += fmt.Sprintf(" aim=(%d,%d) cover=%d r=%d", d.Leap.Aim.X, d.Leap.Aim.Y, d.Leap.Cover, d.Radius)
	} else if d.Leap.Veto != "" {
		s += " veto=" + quoteless(d.Leap.Veto)
	}
	return s
}

func quoteless(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c == ' ' {
			out[i] = '_'
		}
	}
	return string(out)
}

// Utility is U for one estimate at the given pools.
func Utility(e learn.Estimate, hpPct, mpPct int) float64 {
	hp := hpPct
	if hp <= 0 {
		hp = 100 // unknown: full-life weighting
	}
	lam := Lambda * 100 / math.Max(float64(hp), 20)
	mu := Mu * 100 / math.Max(float64(mpPct), 10)
	return e.KillsPerSec - lam*e.HPLossPerSec - mu*e.ManaPerAction
}

// ExploreRate is ε for a pair of cells holding n samples at the least.
func ExploreRate(eps float64, n int) float64 {
	if eps <= 0 {
		return 0
	}
	return eps * ExploreHalf / float64(ExploreHalf+n)
}

// CountWithin counts the points within r (Euclidean, learn.Within) of c.
func CountWithin(c learn.Pt, pts []learn.Pt, r int) int {
	n := 0
	for _, p := range pts {
		if learn.Within(c, p, r) {
			n++
		}
	}
	return n
}

// PickLeapAim is the landing search: among the candidate points — every
// enemy's position and every pairwise midpoint of enemies close enough to
// share a splash — within [LeapMinHop, LeapRange] of me, not walled, not a
// hazard, the one covering the most enemies within radius. Ties: the
// tighter cover (smaller summed distance), then the nearer landing, then
// the smaller coordinates (deterministic).
func PickLeapAim(me learn.Pt, enemies []learn.Pt, radius int, walled func(from, to learn.Pt) bool, hazard func(learn.Pt) bool) (aim learn.Pt, cover int, ok bool) {
	pool := append([]learn.Pt(nil), enemies...)
	sort.SliceStable(pool, func(i, j int) bool { return learn.Cheb(me, pool[i]) < learn.Cheb(me, pool[j]) })
	if len(pool) > aimPool {
		pool = pool[:aimPool]
	}
	cands := append([]learn.Pt(nil), pool...)
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			if learn.Dist(pool[i], pool[j]) <= float64(2*radius+1) {
				cands = append(cands, learn.Pt{X: (pool[i].X + pool[j].X) / 2, Y: (pool[i].Y + pool[j].Y) / 2})
			}
		}
	}
	bestSpread := math.Inf(1)
	bestHop := 1 << 30
	for _, c := range cands {
		hop := learn.Cheb(me, c)
		if hop < LeapMinHop || hop > LeapRange {
			continue
		}
		if (walled != nil && walled(me, c)) || (hazard != nil && hazard(c)) {
			continue
		}
		n, spread := 0, 0.0
		for _, e := range enemies {
			if learn.Within(c, e, radius) {
				n++
				spread += learn.Dist(c, e)
			}
		}
		better := n > cover ||
			(n == cover && ok && (spread < bestSpread-1e-9 ||
				(math.Abs(spread-bestSpread) <= 1e-9 && (hop < bestHop ||
					(hop == bestHop && (c.X < aim.X || (c.X == aim.X && c.Y < aim.Y)))))))
		if n > 0 && (!ok || better) {
			aim, cover, bestSpread, bestHop, ok = c, n, spread, hop, true
		}
	}
	return aim, cover, ok
}

// Decide is the strike policy: the melee option (Carnage; Approach when it
// is out of reach and the hover cannot confirm; Double Swing / the generic
// binding / plain attack when Carnage is out) against the leap option (hard
// gates, then a landing), by utility; ε-exploration keeps both producing
// data while life is above ExploreMinHP. rnd is a uniform [0,1) draw.
func Decide(in Inputs, est Estimator, cfg Config, rnd float64) Decision {
	if in.Me == (learn.Pt{}) && in.Target == (learn.Pt{}) && in.Dist > 0 {
		in.Target = learn.Pt{X: in.Dist}
	}
	enemies := in.Enemies
	if len(enemies) == 0 {
		enemies = []learn.Pt{in.Target}
	}
	var model Estimator = (*learn.Model)(nil)
	if cfg.Learn && est != nil {
		model = est
	}
	radius := model.AoERadius()

	// The melee option.
	far := in.Dist > MeleeReach
	m := Option{OK: true, Aim: in.Target}
	switch {
	case in.LeftProven && !in.LeftBenched:
		m.Kind, m.Skill = Left, learn.Carnage
		if far && !in.HoverOK {
			m.Kind = Approach
		}
	case in.SwingProven && in.MPPct > 10:
		m.Kind, m.Skill = Swing, learn.Swing
	case in.CombatProven && !in.CombatIsLeap:
		m.Kind, m.Skill = Combat, learn.Basic
	default:
		m.Kind, m.Skill = Basic, learn.Basic
	}
	m.C = learn.ClusterBucket(CountWithin(in.Target, enemies, learn.BucketRadius))
	m.D = learn.DistBucket(in.Dist)
	m.N = cellN(model, m.Skill, m.C, m.D)
	m.U = Utility(model.Estimate(m.Skill, m.C, m.D), in.HPPct, in.MPPct)

	// The leap option.
	l := Option{Kind: Leap, Skill: learn.Leap}
	if l.Veto = LeapVeto(in); l.Veto == "" {
		walled := in.Walled
		if in.LineWalled {
			inner := walled
			walled = func(a, b learn.Pt) bool { return b == in.Target || (inner != nil && inner(a, b)) }
		}
		if aim, cover, ok := PickLeapAim(in.Me, enemies, radius, walled, in.Hazard); ok {
			l.OK, l.Aim, l.Cover = true, aim, cover
			l.C = learn.ClusterBucket(CountWithin(aim, enemies, learn.BucketRadius))
			l.D = learn.DistBucket(learn.Cheb(in.Me, aim))
			l.N = cellN(model, l.Skill, l.C, l.D)
			l.U = Utility(model.Estimate(l.Skill, l.C, l.D), in.HPPct, in.MPPct)
		} else {
			l.Veto = "no landing (range, wall or hazard)"
		}
	}

	d := Decision{Leap: l, Melee: m, Radius: radius}
	pickLeap := false
	switch {
	case !l.OK:
		d.Why = "forced"
	case cfg.LeapFirst:
		pickLeap, d.Why = true, "leap-first"
	default:
		pickLeap = l.U > m.U
		d.Why = "prior"
		if cfg.Learn && est != nil && l.N+m.N > 0 {
			d.Why = "model"
		}
		if cfg.Learn && in.HPPct >= ExploreMinHP && rnd < ExploreRate(cfg.Explore, min(l.N, m.N)) {
			pickLeap = !pickLeap
			d.Why = "explore"
		}
	}
	if pickLeap {
		d.Kind, d.Skill, d.Aim = Leap, learn.Leap, l.Aim
	} else {
		d.Kind, d.Skill, d.Aim = m.Kind, m.Skill, m.Aim
	}
	return d
}

func cellN(e Estimator, skill string, c, d int) int {
	if m, ok := e.(*learn.Model); ok {
		return m.Count(skill, c, d)
	}
	return e.Estimate(skill, c, d).N
}
