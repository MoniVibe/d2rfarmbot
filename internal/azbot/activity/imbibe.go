// Imbibe: THE ROADSIDE RITES (P-5R) — shrines and wells taken intelligently.
// A shrine is judged by its READ type, never the sprite; a well is drunk by
// need; Selectable is the freshness oracle (a spent rite reads false); the
// rites yield to blood (no sip within reach of an unwalled enemy — Fight owns
// that moment by class anyway); an unreachable rite is banned, never besieged.
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

type Imbibe struct {
	ban      map[data.UnitID]time.Time
	walkAt   time.Time
	walkFor  data.UnitID
	clickAt  time.Time
	clickFor data.UnitID
	tries    int
}

func NewImbibe() *Imbibe { return &Imbibe{ban: map[data.UnitID]time.Time{}} }

func (im *Imbibe) Name() string { return "imbibe" }

// rite scores one object; zero means walk past.
func (im *Imbibe) rite(s *percept.Snapshot, ob data.Object) float64 {
	if !ob.Selectable { // the freshness oracle: spent rites read false
		return 0
	}
	switch {
	case ob.IsShrine() && percept.GivingShrines[ob.Shrine.ShrineType]:
		if ob.Shrine.ShrineType == object.ExperienceShrine {
			return 0.6 // experience outranks the rest of the rites
		}
		return 0.45
	case percept.HealthWells[ob.Name] && s.Me.HPPct <= 65:
		return 0.65
	case percept.ManaWells[ob.Name] && s.Me.MPPct < 50:
		return 0.5 // the skill-law is thirsty (P-1.13)
	}
	return 0
}

func (im *Imbibe) find(ctx *Ctx) (data.Object, float64, bool) {
	s := ctx.Snap
	var best data.Object
	bestW := 0.0
	for _, ob := range ctx.GR.GetData().Objects {
		if ob.ID == 0 || chebyshev(s.Me.Pos, ob.Position) > 25 {
			continue
		}
		if until, banned := im.ban[ob.ID]; banned && time.Now().Before(until) {
			continue
		}
		if w := im.rite(s, ob); w > bestW {
			best, bestW = ob, w
		}
	}
	return best, bestW, bestW > 0
}

func (im *Imbibe) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	// The rites yield to blood (P-5R.4): no sip within reach of a live tooth.
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 8 {
			return nil
		}
	}
	// Demand has no Ctx: bid on the snapshot's cheap signal (any candidate is
	// re-verified in Step against the live object list before a single step).
	if !s.RiteNearby {
		return nil
	}
	return &arbiter.Demand{Who: im.Name(), Class: arbiter.ClassLoot,
		Urgency: 0.45,
		Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
}

func (im *Imbibe) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	ob, _, ok := im.find(ctx)
	if !ok {
		return Done // nothing worth a detour (or all spent/banned)
	}
	d := chebyshev(s.Me.Pos, ob.Position)
	if d > 5 {
		// Bounded pilgrimage (P-5R.5): 30 s without arrival bans the rite.
		if im.walkFor != ob.ID {
			im.walkFor, im.walkAt = ob.ID, time.Now()
		}
		if time.Since(im.walkAt) > 30*time.Second {
			im.ban[ob.ID] = time.Now().Add(5 * time.Minute)
			im.walkFor = 0
			return Running
		}
		slideStride(ctx, ob.Position, 1200*time.Millisecond, 1, im.Name())
		return Running
	}
	if time.Since(im.clickAt) < 1500*time.Millisecond {
		return Running // the rite animation gets its moment; Selectable settles
	}
	// Hover-confirmed click — the corpse/waypoint recipe: aim the projection,
	// believe only a hover that names THIS object, then one click.
	ctx.M.MoveStop()
	dd := ctx.GR.GetData()
	me := dd.PlayerUnit.Position
	bx := int(float32((ob.Position.X-me.X)-(ob.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((ob.Position.X-me.X)+(ob.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	for dy := -40; dy <= 12; dy += 8 {
		for _, dx := range []int{0, -10, 10, -20, 20} {
			cx, cy := bx+dx, by+dy
			if cx < 20 || cy < 20 || cx > ctx.GR.GameAreaSizeX-20 || cy > ctx.GR.GameAreaSizeY-20 {
				continue
			}
			ctx.M.AimPhysical(cx, cy)
			time.Sleep(45 * time.Millisecond)
			hd := ctx.GR.GetData().HoverData
			if !hd.IsHovered || hd.UnitID != ob.ID {
				continue
			}
			ctx.M.BareClick(cx, cy)
			im.clickAt, im.clickFor = time.Now(), ob.ID
			ctx.Led.Append(verbs.Outcome{Verb: "imbibe", Holder: im.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("rite clicked: obj=%d type=%d at (%d,%d)", int(ob.Name), int(ob.Shrine.ShrineType), ob.Position.X, ob.Position.Y)})
			return Running
		}
	}
	im.tries++
	if im.tries >= 3 {
		im.tries = 0
		im.ban[ob.ID] = time.Now().Add(5 * time.Minute) // hover never confirmed: buried rite
	}
	return Running
}
