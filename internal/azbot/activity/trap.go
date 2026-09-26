package activity

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Traps (ClassFight)
//
// OWNER (2026-09-26, KillaryClinton, Snowlash build): "just summon 5 traps in
// a spread formation and continue on". A pack in reach gets sentries laid in
// a spread around it until five of hers stand (percept OwnTraps counts her
// live sentry units — not a timer); then the march carries on and the traps
// do the killing. The fight itself stays evasive: pressed = fight.

const (
	trapReach   = 14                     // tiles: packs further off are the march's business
	trapCluster = 4                      // tiles: bodies around the aim that count as one pack
	trapMinPack = 1                      // a lone monster gets traps too (bosses come alone)
	trapWant    = 5                      // live sentries wanted at a pack (the game caps five)
	trapWantOne = 2                      // at a lone monster
	trapCover   = 10                     // tiles: a sentry this close to the aim covers it
	trapGap     = 400 * time.Millisecond // one cast animation between sentries
	trapMinMP   = 15                     // below this the belt refills the pool first
	trapNear    = 8                      // the spread's center is at most this far from her
)

// trapSpread: the formation around the center — one sentry per slot.
var trapSpread = []data.Position{{X: 0, Y: 0}, {X: 3, Y: 0}, {X: -3, Y: 0}, {X: 0, Y: 3}, {X: 0, Y: -3}}

var trapBind = struct {
	sync.Mutex
	b *combat.Binding
}{}

// SetTrapBinding is called by the executive after every calibration.
func SetTrapBinding(b *combat.Binding) {
	trapBind.Lock()
	defer trapBind.Unlock()
	if b == nil {
		trapBind.b = nil
		return
	}
	v := *b
	trapBind.b = &v
}

func trapBinding() (combat.Binding, bool) {
	trapBind.Lock()
	defer trapBind.Unlock()
	if trapBind.b == nil {
		return combat.Binding{}, false
	}
	return *trapBind.b, true
}

// trapAim picks the pack: the unwalled enemy within trapReach with the most
// unwalled neighbours inside trapCluster (ties: nearer). ok=false when no
// pack reaches trapMinPack.
func trapAim(me data.Position, enemies []percept.EnemyRef) (data.Position, int, bool) {
	var aim data.Position
	best, bestD := 0, 1<<30
	for _, e := range enemies {
		if e.Walled {
			continue
		}
		d := chebyshev(me, e.Pos)
		if d > trapReach {
			continue
		}
		n := 0
		for _, o := range enemies {
			if !o.Walled && chebyshev(e.Pos, o.Pos) <= trapCluster {
				n++
			}
		}
		if n > best || (n == best && d < bestD) {
			aim, best, bestD = e.Pos, n, d
		}
	}
	return aim, best, best >= trapMinPack
}

// trapCenter pulls a far pack's aim back toward her: the spread stands
// between her and the pack, never out of her cast range.
func trapCenter(me, aim data.Position) data.Position {
	d := chebyshev(me, aim)
	if d <= trapNear {
		return aim
	}
	return data.Position{X: me.X + (aim.X-me.X)*trapNear/d, Y: me.Y + (aim.Y-me.Y)*trapNear/d}
}

type Traps struct {
	slot   int
	castAt time.Time
	aim    data.Position
	pack   int
}

func NewTraps() *Traps { return &Traps{} }

func (t *Traps) Name() string { return "traps" }

func (t *Traps) Demand(s *percept.Snapshot) *arbiter.Demand {
	if _, ok := trapBinding(); !ok || !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 || s.Me.MPPct < trapMinMP || s.Me.CursorItem {
		return nil
	}
	if time.Since(t.castAt) < trapGap {
		return nil
	}
	aim, pack, ok := trapAim(s.Me.Pos, s.Enemies)
	if !ok {
		return nil
	}
	center := trapCenter(s.Me.Pos, aim)
	// Sentries left at the last pack do not guard this one (owner, 2026-09-26:
	// "it doesnt use traps for some reason sometimes" — the old count took
	// every trap within 30 of HER, so a fresh pack 20 tiles on met "5
	// standing" and got none).
	if w := trapsWanted(pack); w <= 0 || trapsCovering(s.Me.OwnTrapPos, center) >= w {
		return nil
	}
	t.aim, t.pack = center, pack
	return &arbiter.Demand{Who: t.Name(), Class: arbiter.ClassFight, Urgency: 0.99, // over the contact law (0.98): the traps ARE her damage
		Commit: arbiter.Commitment{MinHold: 300 * time.Millisecond}}
}

func (t *Traps) Step(ctx *Ctx) Verdict {
	b, ok := trapBinding()
	if !ok {
		return Abandoned
	}
	off := trapSpread[t.slot%len(trapSpread)]
	at := data.Position{X: t.aim.X + off.X, Y: t.aim.Y + off.Y}
	t.slot++
	t.castAt = time.Now()
	if !groundAt(ctx, at, b.Key, false) {
		return Abandoned
	}
	ctx.Led.Append(verbs.Outcome{Verb: "trap", Holder: t.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("sentry at (%d,%d): pack of %d, %d of hers standing, mp %d%%", at.X, at.Y, t.pack, ctx.Snap.Me.OwnTraps, ctx.Snap.Me.MPPct)})
	return Done
}

// trapsWanted: sentries a target deserves — five at a pack, two at a lone one.
func trapsWanted(pack int) int {
	want := profileTraps(trapWant) // the profile's "traps:" (0 = never)
	if pack <= 1 && want > trapWantOne {
		return trapWantOne
	}
	return want
}

// trapsCovering: her sentries within trapCover of the aim.
func trapsCovering(traps []data.Position, aim data.Position) int {
	n := 0
	for _, p := range traps {
		if chebyshev(p, aim) <= trapCover {
			n++
		}
	}
	return n
}
