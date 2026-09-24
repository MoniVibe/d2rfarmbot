package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Identify (ClassService)

// identifyWorks: the live belief that the ID-tome SKILL path identifies items on this
// mod (select tome skill -> right-click casts -> inventory opens with the identify
// hand -> click the item). Every step has a readable postcondition; three attempts
// with no Identified flip retire the belief so she can never grind a dead ritual.
var identifyWorks atomic.Bool

func init() { identifyWorks.Store(true) }

// idPhase is Identify's phase (contract v2).
type idPhase uint8

const (
	idClean idPhase = iota // precondition: nothing on screen
	idCast                 // the tome key (the skill selection), then the right-click cast: the identify hand + the bag
	idTouch                // click the item cell; the count judges it 1.2s later
	idClose                // janitor OFF: the bag the cast opened is closed by sight
)

func (p idPhase) String() string {
	switch p {
	case idClean:
		return "Clean"
	case idCast:
		return "Cast"
	case idTouch:
		return "Touch"
	case idClose:
		return "Close"
	}
	return "?"
}

// bagClaims: Identify and Equip own the bag the ID-tome cast opens, from the
// cast on — never in Clean, so a leftover bag is closed before the ritual.
func bagClaims[P ~uint8](p P) screen.Panel {
	if p == 0 {
		return 0
	}
	return claimsBag
}

// bagResume: a preempted bag ritual comes back to Clean — with the janitor ON
// its bag was closed the moment the grant was lost, and the identify hand the
// cast raised is gone either way. Clean re-verifies the screen and the ritual
// starts again from the tome key. Close stays Close.
func bagResume[P ~uint8](p, closePh P) P {
	if p == closePh {
		return p
	}
	return 0
}

// Identify burns the ID tome's charges on the unidentified magic+ backlog — the gate
// between "inventory full of mystery" and the Fence's valuation (the owner: "it won't
// identify and sell anything... inventory is getting full of things she could use").
type Identify struct {
	life  svcLife[idPhase]
	tries int // attempts with no count progress — the disproof counter
	lastN int
	cast  bool // Cast: the tome key is pressed, the right-click is next
}

var (
	_ Life   = (*Identify)(nil)
	_ Phased = (*Identify)(nil)
)

func NewIdentify() *Identify {
	idn := &Identify{lastN: -1}
	idn.life.init(idn.Name(), idClose, bagClaims[idPhase])
	idn.life.ph.Budget(idClean, 10*time.Second)
	idn.life.ph.Budget(idCast, 3*time.Second)
	idn.life.ph.Budget(idTouch, 4*time.Second)
	idn.life.ph.Budget(idClose, 8*time.Second)
	return idn
}

func (idn *Identify) Name() string      { return "identify" }
func (idn *Identify) PhaseName() string { return idn.life.ph.Phase().String() }

func (idn *Identify) Demand(s *percept.Snapshot) *arbiter.Demand {
	return idn.life.keepBid(idn.demand(s), s)
}

func (idn *Identify) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	if !s.Valid || !s.Me.InTown || s.Me.UnidentCount == 0 || !identifyWorks.Load() {
		return nil
	}
	// AN EMPTY TOME IDENTIFIES NOTHING (the owner watched the loop, 09:50:
	// the cast fizzles, the follow-up click GRABS the item, the next click
	// throws it — WARNING 9's mess on repeat). Zero charges: stand down until
	// Restock refills the tome (P-4.5).
	if s.Me.IDScrolls <= 0 {
		return nil
	}
	return &arbiter.Demand{Who: idn.Name(), Class: arbiter.ClassService,
		Urgency: 0.55, // above a routine restock; below Heal and a deep-poverty Fence
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (idn *Identify) Needs(*percept.Snapshot) Needs { return idn.life.needs() }

func (idn *Identify) Begin(ctx *Ctx, resumed bool) {
	idn.life.begin(resumed)
	idn.cast = false
	if !resumed {
		idn.tries, idn.lastN = 0, -1
		return
	}
	if cur := idn.life.ph.Phase(); bagResume(cur, idClose) != cur {
		idn.life.to(bagResume(cur, idClose), "resumed: "+cur.String()+" re-verifies from a clean screen")
	}
}

func (idn *Identify) Suspend(ctx *Ctx, _ phase.Reason) { idn.life.suspend(ctx) }

func (idn *Identify) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	idn.life.end(ctx, v, why)
	idn.tries, idn.lastN, idn.cast = 0, -1, false
}

func (idn *Identify) Step(ctx *Ctx) Status {
	l := &idn.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == idClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	if s.Me.CursorItem {
		return l.wait(150 * time.Millisecond) // WARNING 9: the tome ritual clicks cells — parking first
	}
	if s.Me.UnidentCount == 0 {
		// THE BAG IS BYTE-BLIND (00:19, the owner: "what the hell is he doing
		// chilling" — identify cast, released 8s later, and the inventory
		// STOOD because MenuOpen never reads it). The bag the cast opened is
		// ours: closed by sight (Close, janitor OFF) or by the janitor once
		// End drops the claim — never left standing.
		return l.finish(ctx, phase.Done, phase.Completed, "backlog identified")
	}
	// Progress audit: the count dropping IS the proof the ritual works.
	if idn.lastN >= 0 && s.Me.UnidentCount < idn.lastN {
		idn.tries = 0
	}
	idn.lastN = s.Me.UnidentCount
	switch l.ph.Phase() {
	case idClean:
		if ok, st := l.cleanScreen(ctx, 0); !ok {
			return st
		}
		if ctx.Cap == nil || ctx.Cap.Identify == nil {
			return idn.coolOff(ctx, phase.Precondition, "no proven ID-tome binding")
		}
		l.to(idCast, "screen clear")
		return l.running()
	case idCast:
		if f, bad := l.interlocked(ctx); bad {
			l.to(idClean, "interlock: "+f.String()+" up — no cast into it")
			return l.running()
		}
		if !idn.cast {
			idn.tries++
			if idn.tries > 3 {
				return idn.coolOff(ctx, phase.Deaf, fmt.Sprintf("3 attempts, count stuck at %d — ritual retired", s.Me.UnidentCount))
			}
			// 1. Select the tome skill — readback-verified (the selection flip is the proof).
			ctx.M.MoveStop()
			ctx.M.PressKey(ctx.Cap.Identify.Key)
			idn.cast = true
			return l.wait(150 * time.Millisecond)
		}
		// 2. Cast: a world right-click with the tome selected raises the identify hand
		//    and opens the inventory. Aim at open ground below her feet — never an NPC.
		ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
		idn.cast = false
		l.to(idTouch, "cast")
		return l.wait(500 * time.Millisecond)
	case idTouch:
		if len(s.Unid) == 0 {
			return l.wait(100 * time.Millisecond)
		}
		// 3. Touch the item: the proven inventory-cell click. The postcondition is the
		//    snapshot's Identified flag — judged by the audit above after 1.2s.
		it := s.Unid[0]
		cx, cy := invCell(it.GX, it.GY)
		ctx.M.UIClick(cx, cy)
		l.to(idCast, fmt.Sprintf("touched cell (%d,%d)", it.GX, it.GY))
		return l.wait(1200 * time.Millisecond)
	}
	return l.running()
}

// coolOff retires the identify belief and ends the episode.
func (idn *Identify) coolOff(ctx *Ctx, why phase.Reason, ev string) Status {
	identifyWorks.Store(false)
	ctx.Led.Append(verbs.Outcome{Verb: "identify", Holder: idn.Name(), Result: verbs.ResDeaf,
		Evidence: ev + " — identify belief retired (drill the ritual manually)"})
	return idn.life.finish(ctx, phase.Abandoned, why, ev)
}
