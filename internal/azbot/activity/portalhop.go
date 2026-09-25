package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// portalHop is Advance's road for the legs whose way in is an OBJECT: the
// Arcane Sanctuary portal in Palace Cellar 3, and Horazon's red portal from the
// Sanctuary to the Canyon of the Magi. ok=false: not such a leg (the level-warp
// march takes it).
func (a *Advance) portalHop(ctx *Ctx, to area.ID) (Verdict, bool) {
	s := ctx.Snap
	switch {
	case s.Me.Area == area.PalaceCellarLevel3 && to == area.ArcaneSanctuary:
		return a.enterObjectPortal(ctx, object.ArcaneSanctuaryPortal, "the Arcane Sanctuary portal"), true
	case s.Me.Area == area.ArcaneSanctuary && to == area.CanyonOfTheMagi:
		// The red portal stands once the journal is read: enter it.
		if _, live := findObject(ctx.GR.GetData().Objects, object.PermanentTownPortal); live {
			return a.enterObjectPortal(ctx, object.PermanentTownPortal, "Horazon's portal to the Canyon"), true
		}
		// Otherwise walk to the journal: the Summoner guards it (Fight preempts
		// the march), and Imbibe reads it (a quest object, 60-tile reach).
		return a.approachObject(ctx, object.YetAnotherTome, "Horazon's Journal (the Summoner)"), true
	}
	return 0, false
}

func findObject(obs []data.Object, name object.Name) (data.Object, bool) {
	for _, ob := range obs {
		if ob.Name == name {
			return ob, true
		}
	}
	return data.Object{}, false
}

// objectPos: the live object, else the map oracle's preset for this area.
func (a *Advance) objectPos(ctx *Ctx, name object.Name) (data.Object, bool, bool) {
	dd := ctx.GR.GetData()
	if ob, ok := findObject(dd.Objects, name); ok {
		return ob, true, true
	}
	if ad, ok := dd.Areas[ctx.Snap.Me.Area]; ok {
		if ob, ok := findObject(ad.Objects, name); ok {
			return ob, false, true
		}
	}
	return data.Object{}, false, false
}

// approachObject walks to an object (live or on the map); unknown: sweep the level.
func (a *Advance) approachObject(ctx *Ctx, name object.Name, label string) Verdict {
	s := ctx.Snap
	ob, _, known := a.objectPos(ctx, name)
	a.notePortalHop(ctx, label, known, ob.Position)
	if !known {
		if st, ok := a.cov.step(ctx, coverage.Bias{}, a.Name()); ok && st == coverage.Exploring {
			return Running
		}
		return Abandoned
	}
	if chebyshev(s.Me.Pos, ob.Position) > 4 {
		moveTo(ctx, ob.Position, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
	}
	return Running
}

// enterObjectPortal walks to a portal object and clicks it (hover-confirmed by
// unit, the area change is the postcondition — EnterPortal's own recipe).
func (a *Advance) enterObjectPortal(ctx *Ctx, name object.Name, label string) Verdict {
	s := ctx.Snap
	ob, live, known := a.objectPos(ctx, name)
	if !live || chebyshev(s.Me.Pos, ob.Position) > 5 {
		return a.approachObject(ctx, name, label)
	}
	if time.Since(a.hopClickAt) < 2*time.Second {
		return Running
	}
	a.hopClickAt = time.Now()
	ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("portal hop: entering %s (obj %d) at (%d,%d) known=%v", label, int(name), ob.Position.X, ob.Position.Y, known)})
	verbs.EnterPortal{Target: ob.ID, TargetPos: ob.Position, Window: 5 * time.Second}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
	return Running
}

// notePortalHop: one log line per area and label.
func (a *Advance) notePortalHop(ctx *Ctx, label string, known bool, at data.Position) {
	key := fmt.Sprintf("%d:%s", int(ctx.Snap.Me.Area), label)
	if a.hopNoted == key {
		return
	}
	a.hopNoted = key
	ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("objective: %s (on the map=%v at (%d,%d))", label, known, at.X, at.Y)})
}

// talRashaTombs: the seven tombs off the Canyon; one holds the Horadric Orifice.
var talRashaTombs = []area.ID{area.TalRashasTomb1, area.TalRashasTomb2, area.TalRashasTomb3,
	area.TalRashasTomb4, area.TalRashasTomb5, area.TalRashasTomb6, area.TalRashasTomb7}

// noteRealTomb: in the Canyon (or a tomb), the map oracle names the true tomb —
// the one whose presets hold the Horadric Orifice (upstream's own recipe; the
// quest log's symbol is unreadable here) — and it joins the itinerary as the
// next leg. Idempotent.
func (a *Advance) noteRealTomb(ctx *Ctx) {
	s := ctx.Snap
	if s.Me.Area != area.CanyonOfTheMagi && !isTomb(s.Me.Area) {
		return
	}
	if len(a.Itinerary) == 0 || isTomb(a.Itinerary[len(a.Itinerary)-1].Area) {
		return
	}
	dd := ctx.GR.GetData()
	for _, t := range talRashaTombs {
		ad, ok := dd.Areas[t]
		if !ok {
			continue
		}
		if _, has := findObject(ad.Objects, object.HoradricOrifice); has {
			a.Itinerary = append(a.Itinerary, Leg{Area: t, MinLevel: 30})
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("the true tomb is area %d (the Horadric Orifice is on its map) — it is the next leg", int(t))})
			return
		}
	}
}

func isTomb(ar area.ID) bool {
	for _, t := range talRashaTombs {
		if t == ar {
			return true
		}
	}
	return false
}
