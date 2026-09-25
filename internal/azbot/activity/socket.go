package activity

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Socket
//
// OWNER (2026-09-25): "for the sake of reusability since act 3 also has it, we
// need the bot to be able to place the mcguffin in the slot". A socket is a
// quest object whose click opens the one-slot "anvil" panel (the Horadric
// Orifice; later acts' altars take theirs the same way): stand at it, click it
// (hover-confirmed by unit), lift the artifact from the bag, drop it in the
// slot, press the panel's button, and prove the artifact left the bag.
//
// Geometry: upstream koolo's 1280x720 anvil points (slot 272,333; button
// 272,450), scaled by 1050/720 — the same factor maps upstream's vendor grid
// onto our measured shop cell (183,238) — and then by the client's height.

// socketJobs: which object takes which artifact (mod item codes).
var socketJobs = []struct {
	obj  object.Name
	item string
}{
	{object.HoradricOrifice, "hst"}, // Act 2: the Horadric Staff opens Duriel's lair
}

const (
	anvilRef     = 1050.0 / 720.0
	anvilSlotX   = 272.0
	anvilSlotY   = 333.0
	anvilButtonX = 272.0
	anvilButtonY = 450.0
)

func anvilPx(ctx *Ctx, x, y float64) (int, int) {
	k := shopScale(ctx) * anvilRef
	return int(x * k), int(y * k)
}

// The live socket Advance saw (it holds the live object list; Demand does not).
var socketSeen = struct {
	sync.Mutex
	ar area.ID
	ob data.Object
}{}

func noteSocket(ar area.ID, ob data.Object) {
	socketSeen.Lock()
	socketSeen.ar, socketSeen.ob = ar, ob
	socketSeen.Unlock()
}

func liveSocket(ar area.ID) (data.Object, bool) {
	socketSeen.Lock()
	defer socketSeen.Unlock()
	return socketSeen.ob, socketSeen.ar == ar && socketSeen.ob.ID != 0
}

type skPhase uint8

const (
	skClean skPhase = iota
	skWalk          // to the object
	skOpen          // click the object until the panel (with the bag) stands
	skLift          // lift the artifact from the bag
	skPlace         // drop it in the slot
	skPress         // press the panel's button
	skClose
)

func (p skPhase) String() string {
	return [...]string{"Clean", "Walk", "Open", "Lift", "Place", "Press", "Close"}[p]
}

func skClaims(p skPhase) screen.Panel {
	if p >= skOpen && p < skClose {
		// The socket's own panel (the anvil) reads as an unknown left panel: it is
		// ours, not foreign (owner: "the loop stuck guard thing messes with you").
		return claimsBag | screen.LeftPanel | screen.SubPanel
	}
	return 0
}

type Socket struct {
	life   svcLife[skPhase]
	item   string
	unit   data.UnitID
	gx, gy int
	clickT time.Time
	tries  int
	coolAt time.Time
	done   map[area.ID]bool
}

var (
	_ Life   = (*Socket)(nil)
	_ Phased = (*Socket)(nil)
)

func NewSocket() *Socket {
	k := &Socket{done: map[area.ID]bool{}}
	k.life.init(k.Name(), skClose, skClaims)
	k.life.ph.Budget(skWalk, 60*time.Second)
	k.life.ph.Budget(skOpen, 15*time.Second)
	k.life.ph.Budget(skLift, 6*time.Second)
	k.life.ph.Budget(skPlace, 6*time.Second)
	k.life.ph.Budget(skPress, 8*time.Second)
	k.life.ph.Budget(skClose, 6*time.Second)
	return k
}

func (k *Socket) Name() string      { return "socket" }
func (k *Socket) PhaseName() string { return k.life.ph.Phase().String() }

// artifact: the bag item a live socket here takes, if carried.
func (k *Socket) artifact(s *percept.Snapshot) (string, percept.BagItem, bool) {
	ob, ok := liveSocket(s.Me.Area)
	if !ok || k.done[s.Me.Area] {
		return "", percept.BagItem{}, false
	}
	db := gamedata.Get()
	for _, j := range socketJobs {
		if j.obj != ob.Name {
			continue
		}
		for _, b := range s.Bag {
			if row := db.Item(b.ID); row != nil && row.Code == j.item {
				return j.item, b, true
			}
		}
	}
	return "", percept.BagItem{}, false
}

func (k *Socket) Demand(s *percept.Snapshot) *arbiter.Demand {
	return k.life.keepBid(k.demand(s, time.Now()), s)
}

func (k *Socket) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct < 40 || now.Before(k.coolAt) {
		return nil
	}
	if _, _, ok := k.artifact(s); !ok {
		return nil
	}
	// The calm gate binds only AT the socket (R70: applied on the whole walk, every
	// monster on a 176-tile route cancelled the errand within a second). On the
	// way, Fight preempts by class as it does any march.
	if ob, ok := liveSocket(s.Me.Area); ok && chebyshev(s.Me.Pos, ob.Position) <= 8 {
		for _, e := range s.Enemies {
			if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 10 {
				return nil // the panel opens only in a calm moment: Fight clears first
			}
		}
	}
	return &arbiter.Demand{Who: k.Name(), Class: arbiter.ClassLoot, Urgency: 0.96,
		Commit: arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (k *Socket) Needs(*percept.Snapshot) Needs { return k.life.needs() }

func (k *Socket) Begin(ctx *Ctx, resumed bool) {
	k.life.begin(resumed)
	if resumed && k.life.ph.Phase() != skClose {
		k.life.to(skClean, "resumed: re-read the bag")
	}
	if !resumed {
		k.tries = 0
	}
}

func (k *Socket) Suspend(ctx *Ctx, _ phase.Reason) { k.life.suspend(ctx) }

func (k *Socket) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	k.life.end(ctx, v, why)
	if v != phase.Done {
		k.coolAt = time.Now().Add(20 * time.Second)
	}
}

func (k *Socket) Step(ctx *Ctx) Status {
	l := &k.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == skClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	ob, live := liveSocket(s.Me.Area)
	switch l.ph.Phase() {
	case skClean:
		item, b, ok := k.artifact(s)
		if !ok || !live {
			return l.finish(ctx, phase.Done, phase.Completed, "no artifact or no live socket")
		}
		k.item, k.unit, k.gx, k.gy = item, b.Unit, b.GX, b.GY
		l.to(skWalk, fmt.Sprintf("carry %s (unit %d) to obj %d at (%d,%d)", item, b.Unit, int(ob.Name), ob.Position.X, ob.Position.Y))
		return l.running()
	case skWalk:
		if chebyshev(s.Me.Pos, ob.Position) > 4 {
			moveTo(ctx, ob.Position, marchOpts(ctx, k.Name(), 1200*time.Millisecond))
			return l.wait(200 * time.Millisecond)
		}
		l.to(skOpen, "at the socket")
		return l.running()
	case skOpen:
		if bagSeen(ctx) {
			l.to(skLift, "the socket panel stands (bag on screen)")
			return l.running()
		}
		if time.Since(k.clickT) > 2*time.Second {
			if clickObject(ctx, ob) {
				k.tries++
				ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: k.Name(), Result: verbs.ResDone,
					Evidence: fmt.Sprintf("socket: clicked obj %d (try %d)", int(ob.Name), k.tries)})
				snapPNG(ctx, "logs/socket_open.png")
			}
			k.clickT = time.Now()
		}
		return l.wait(250 * time.Millisecond)
	case skLift:
		if s.Me.CursorItem {
			l.to(skPlace, "artifact on the cursor")
			return l.running()
		}
		if time.Since(k.clickT) > 900*time.Millisecond {
			cx, cy := invCellPx(ctx, k.gx, k.gy)
			ctx.M.RealMenuClick(cx, cy)
			k.clickT = time.Now()
		}
		return l.wait(200 * time.Millisecond)
	case skPlace:
		if !s.Me.CursorItem {
			if inBag(ctx, k.unit) {
				l.to(skLift, "the artifact fell back into the bag")
				return l.running()
			}
			snapPNG(ctx, "logs/socket_placed.png")
			l.to(skPress, "artifact in the slot")
			return l.running()
		}
		if time.Since(k.clickT) > 900*time.Millisecond {
			cx, cy := anvilPx(ctx, anvilSlotX, anvilSlotY)
			ctx.M.RealMenuClick(cx, cy)
			k.clickT = time.Now()
		}
		return l.wait(200 * time.Millisecond)
	case skPress:
		if time.Since(k.clickT) < 700*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		bx, by := anvilPx(ctx, anvilButtonX, anvilButtonY)
		ctx.M.RealMenuClick(bx, by)
		k.clickT = time.Now()
		k.done[s.Me.Area] = true
		socketDone.Store(true)
		ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: k.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("socket: %s placed in obj %d and the button pressed at (%d,%d)", k.item, int(ob.Name), bx, by)})
		snapPNG(ctx, "logs/socket_pressed.png")
		return l.finish(ctx, phase.Done, phase.Completed, "artifact socketed")
	}
	return l.running()
}

// socketDone: an artifact was socketed this run (Advance then marches on).
var socketDone atomic.Bool
