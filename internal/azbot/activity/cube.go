package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Transmute (the Horadric Cube)
//
// The campaign's two required cube recipes (the owner did both by hand for
// Fableboi): the Horadric Staff (shaft + amulet: Act 2's Orifice) and
// Khalim's Will (flail + eye + brain + heart: Act 3's Compelling Orb). With
// every input and the cube in the bag and a calm moment: open the bag, right-
// click the cube, CTRL+CLICK each input INTO it, press Transmute, ctrl+click
// the result back to the bag.
//
// SAFETY: with only the bag open a ctrl+click DROPS the item (the shed's own
// gesture). An input is ctrl-clicked only while the cube panel is SEEN open —
// the bag plus an unidentified left panel (the anvil's reading) — and every
// move is judged by memory (the unit's location), never assumed.
//
// Geometry: upstream koolo's 1280x720 cube points (grid corner 222,247, cell
// 33; Transmute 273,411) scaled like the anvil (anvilPx: proven on the
// Orifice).

type cubeRecipe struct {
	inputs []string // mod item codes
	output string
	label  string
}

var cubeRecipes = []cubeRecipe{
	{inputs: []string{"msf", "vip"}, output: "hst", label: "the Horadric Staff"},
	{inputs: []string{"qf1", "qey", "qbr", "qhr"}, output: "qf2", label: "Khalim's Will"},
}

const (
	cubeCornerX  = 222.0
	cubeCornerY  = 247.0
	cubeCell     = 33.0
	cubeButtonX  = 273.0
	cubeButtonY  = 411.0
	cubeCalm     = 15
	cubeMaxTries = 3
)

type cuPhase uint8

const (
	cuClean cuPhase = iota
	cuBag           // the bag on screen
	cuOpen          // right-click the cube: the cube panel stands
	cuPut           // ctrl+click the inputs in
	cuPress         // Transmute
	cuTake          // ctrl+click the result out
	cuClose
)

func (p cuPhase) String() string {
	return [...]string{"Clean", "Bag", "Open", "Put", "Press", "Take", "Close"}[p]
}

func cuClaims(p cuPhase) screen.Panel {
	if p >= cuBag && p < cuClose {
		return claimsBag | screen.LeftPanel
	}
	return 0
}

type Transmute struct {
	life   svcLife[cuPhase]
	recipe cubeRecipe
	keyT   time.Time
	clickT time.Time
	tries  int
	coolAt time.Time
	done   map[string]bool
}

var (
	_ Life   = (*Transmute)(nil)
	_ Phased = (*Transmute)(nil)
)

func NewTransmute() *Transmute {
	t := &Transmute{done: map[string]bool{}}
	t.life.init(t.Name(), cuClose, cuClaims)
	for p, d := range map[cuPhase]time.Duration{cuBag: 6 * time.Second, cuOpen: 10 * time.Second, cuPut: 20 * time.Second,
		cuPress: 6 * time.Second, cuTake: 8 * time.Second, cuClose: 6 * time.Second} {
		t.life.ph.Budget(p, d)
	}
	return t
}

func (t *Transmute) Name() string      { return "transmute" }
func (t *Transmute) PhaseName() string { return t.life.ph.Phase().String() }

// bagCode: the first bag item carrying a mod item code.
func bagCode(s *percept.Snapshot, code string) (percept.BagItem, bool) {
	db := gamedata.Get()
	if db == nil {
		return percept.BagItem{}, false
	}
	for _, b := range s.Bag {
		if row := db.Item(b.ID); row != nil && row.Code == code {
			return b, true
		}
	}
	return percept.BagItem{}, false
}

// ready: a recipe whose inputs and the cube all ride the bag, output not held.
func (t *Transmute) ready(s *percept.Snapshot) (cubeRecipe, bool) {
	if _, ok := bagCode(s, "box"); !ok {
		return cubeRecipe{}, false
	}
	for _, r := range cubeRecipes {
		if t.done[r.output] {
			continue
		}
		if _, held := bagCode(s, r.output); held {
			continue
		}
		all := true
		for _, in := range r.inputs {
			if _, ok := bagCode(s, in); !ok {
				all = false
				break
			}
		}
		if all {
			return r, true
		}
	}
	return cubeRecipe{}, false
}

func (t *Transmute) Demand(s *percept.Snapshot) *arbiter.Demand {
	return t.life.keepBid(t.demand(s, time.Now()), s)
}

func (t *Transmute) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if !s.Valid || s.Me.HPPct < 40 || now.Before(t.coolAt) || s.Me.CursorItem {
		return nil
	}
	r, ok := t.ready(s)
	if !ok {
		return nil
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= cubeCalm {
			return nil // panels open only in a calm moment
		}
	}
	t.recipe = r
	return &arbiter.Demand{Who: t.Name(), Class: arbiter.ClassLoot, Urgency: 0.95,
		Commit: arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (t *Transmute) Needs(*percept.Snapshot) Needs { return t.life.needs() }

func (t *Transmute) Begin(ctx *Ctx, resumed bool) {
	t.life.begin(resumed)
	if resumed && t.life.ph.Phase() != cuClose {
		t.life.to(cuClean, "resumed: re-read the bag")
	}
	if !resumed {
		t.tries = 0
	}
}

func (t *Transmute) Suspend(ctx *Ctx, _ phase.Reason) { t.life.suspend(ctx) }

func (t *Transmute) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	t.life.end(ctx, v, why)
	if v != phase.Done {
		t.coolAt = time.Now().Add(30 * time.Second)
	}
}

// cubeOpen: the cube panel is SEEN — the bag and an unidentified left panel
// (never the stash, which the oracle names).
func cubeOpen(ctx *Ctx) bool {
	seen, ok := seenPanels(ctx)
	return ok && seen&screen.Inventory != 0 && seen&screen.LeftPanel != 0 && seen&screen.Stash == 0
}

// cubeCellPx: the screen point of a cube grid cell.
func cubeCellPx(ctx *Ctx, gx, gy int) (int, int) {
	return anvilPx(ctx, cubeCornerX+float64(gx)*cubeCell+cubeCell/2, cubeCornerY+float64(gy)*cubeCell+cubeCell/2)
}

// inCube: the unit (or an item of code) currently rides the cube.
func inCubeCode(ctx *Ctx, code string) (data.Item, bool) {
	db := gamedata.Get()
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationCube) {
		if row := db.Item(int(it.ID)); row != nil && row.Code == code {
			return it, true
		}
	}
	return data.Item{}, false
}

func (t *Transmute) Step(ctx *Ctx) Status {
	l := &t.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == cuClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	if s.Me.CursorItem && l.ph.Phase() != cuTake {
		return l.wait(150 * time.Millisecond) // WARNING 9: the cursor owns every click
	}
	switch l.ph.Phase() {
	case cuClean:
		r, ok := t.ready(s)
		if !ok {
			// Already transmuted (the result rides the cube, or a restart lost the thread).
			if _, inCube := inCubeCode(ctx, t.recipe.output); inCube && t.recipe.output != "" {
				l.to(cuTake, "the result waits in the cube")
				return l.running()
			}
			return l.finish(ctx, phase.Done, phase.Completed, "no recipe ready")
		}
		t.recipe = r
		if ok2, st := l.cleanScreen(ctx, claimsBag|screen.LeftPanel); !ok2 {
			return st
		}
		l.to(cuBag, "transmuting "+r.label)
		return l.running()
	case cuBag:
		if bagSeen(ctx) {
			l.to(cuOpen, "bag on screen")
			return l.running()
		}
		if ctx.InvKey != 0 && time.Since(t.keyT) > 1500*time.Millisecond {
			ctx.M.RealKey(uint16(ctx.InvKey))
			t.keyT = time.Now()
		}
		return l.wait(150 * time.Millisecond)
	case cuOpen:
		if cubeOpen(ctx) {
			l.to(cuPut, "the cube panel stands")
			return l.running()
		}
		if time.Since(t.clickT) > 1500*time.Millisecond {
			if t.tries >= cubeMaxTries {
				return l.finish(ctx, phase.Abandoned, phase.Deaf, "the cube never opened")
			}
			b, ok := bagCode(s, "box")
			if !ok {
				return l.finish(ctx, phase.Abandoned, phase.Refused, "the cube left the bag")
			}
			cx, cy := bagItemCenterPx(ctx, b)
			ctx.M.RealMenuRightClick(cx, cy)
			t.clickT = time.Now()
			t.tries++
		}
		return l.wait(200 * time.Millisecond)
	case cuPut:
		if !cubeOpen(ctx) {
			l.to(cuOpen, "the cube panel closed")
			return l.running()
		}
		if time.Since(t.clickT) < 600*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		for _, in := range t.recipe.inputs {
			b, inBagNow := bagCode(s, in)
			if !inBagNow {
				continue // already in the cube (memory says so next tick)
			}
			cx, cy := bagItemCenterPx(ctx, b)
			ctx.M.RealMenuCtrlClick(cx, cy)
			t.clickT = time.Now()
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: t.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("cube: %s into the cube (unit %d)", in, b.Unit)})
			return l.wait(300 * time.Millisecond)
		}
		for _, in := range t.recipe.inputs {
			if _, ok := inCubeCode(ctx, in); !ok {
				return l.finish(ctx, phase.Abandoned, phase.Refused, fmt.Sprintf("%s is neither in the bag nor the cube", in))
			}
		}
		t.clickT = time.Time{}
		l.to(cuPress, "every input in the cube")
		return l.running()
	case cuPress:
		if _, ok := inCubeCode(ctx, t.recipe.output); ok {
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: t.Name(), Result: verbs.ResDone,
				Evidence: "cube: transmuted " + t.recipe.label})
			snapPNG(ctx, "logs/cube_transmuted.png")
			l.to(cuTake, "the result is in the cube")
			return l.running()
		}
		if time.Since(t.clickT) > 1500*time.Millisecond {
			bx, by := anvilPx(ctx, cubeButtonX, cubeButtonY)
			ctx.M.RealMenuClick(bx, by)
			t.clickT = time.Now()
		}
		return l.wait(200 * time.Millisecond)
	case cuTake:
		if _, ok := bagCode(s, t.recipe.output); ok {
			t.done[t.recipe.output] = true
			return l.finish(ctx, phase.Done, phase.Completed, t.recipe.label+" in the bag")
		}
		if !cubeOpen(ctx) {
			l.to(cuBag, "reopen the cube for the result")
			return l.running()
		}
		if time.Since(t.clickT) > 1200*time.Millisecond {
			if out, ok := inCubeCode(ctx, t.recipe.output); ok {
				cx, cy := cubeCellPx(ctx, out.Position.X, out.Position.Y)
				ctx.M.RealMenuCtrlClick(cx, cy) // cube -> bag
				t.clickT = time.Now()
			}
		}
		return l.wait(200 * time.Millisecond)
	}
	return l.running()
}

// bagItemCenterPx: the centre of a bag item's footprint (the mod tables' W×H).
func bagItemCenterPx(ctx *Ctx, b percept.BagItem) (int, int) {
	w, h := 1, 1
	if cl := loot.Classify(b.ID); cl.W > 0 && cl.H > 0 {
		w, h = cl.W, cl.H
	}
	return invFootPx(ctx, b.GX, b.GY, w, h)
}
