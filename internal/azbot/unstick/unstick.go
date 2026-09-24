// Package unstick is the pure brain of the Unstick activity (docs/AZBOT_V2.md
// step 9): a watchdog prescription carried out under the arbiter like any other
// holder. The machine decides; the activity performs the one verb it names
// (Stride, CastSelf/TP, EnterPortal) and feeds back only what it observes —
// every phase is judged by the world, never by a verb's say-so.
//
//	stride remedy: Stride → VerifyDisplacement → (Stride …) → Done | Abandoned
//	portal remedy: CastTP → AwaitPortal → EnterPortal → AwaitArea → Done | Abandoned
//
// PURE: data, nav and phase only — tested natively on Linux.
package unstick

import (
	"fmt"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
)

// Phase is Unstick's typed phase.
type Phase uint8

const (
	Idle Phase = iota
	Stride
	VerifyDisplacement
	CastTP
	AwaitPortal
	EnterPortal
	AwaitArea
	Done
	Abandoned
)

func (p Phase) String() string {
	switch p {
	case Idle:
		return "Idle"
	case Stride:
		return "Stride"
	case VerifyDisplacement:
		return "VerifyDisplacement"
	case CastTP:
		return "CastTP"
	case AwaitPortal:
		return "AwaitPortal"
	case EnterPortal:
		return "EnterPortal"
	case AwaitArea:
		return "AwaitArea"
	case Done:
		return "Done"
	case Abandoned:
		return "Abandoned"
	}
	return "?"
}

// Rx is an open prescription: the watchdog's verdict turned into work.
type Rx struct {
	Kind     string // the pathology ("stuck", "pinned", ...)
	Portal   bool   // the portal remedy; false = footwork (a novel-bearing stride)
	Site     data.Position
	Area     int
	Opened   time.Time
	Culprit  string
	Evidence string
}

// Portal is a live town portal (dead doors already filtered out by the caller).
type Portal struct {
	ID  data.UnitID
	Pos data.Position
}

// Obs is this tick's world as the machine needs it.
type Obs struct {
	Pos       data.Position
	Area      int
	InTown    bool
	Held      time.Duration // Unstick's held time (the arbiter's ledger) — phase budgets spend it
	Portals   []Portal
	CanPortal bool      // a town-portal binding exists
	Grid      *nav.Grid // nil = blind bearings
}

type ActKind uint8

const (
	ActNone ActKind = iota
	ActStride
	ActCast  // cast the town portal at her feet
	ActEnter // click through Portal
)

func (k ActKind) String() string {
	switch k {
	case ActStride:
		return "stride"
	case ActCast:
		return "cast"
	case ActEnter:
		return "enter"
	}
	return "none"
}

// Act is the one verb the activity performs this Step.
type Act struct {
	Kind    ActKind
	To      data.Position
	Hold    time.Duration
	MinGain int
	Planned bool // the bearing was checked clear on the grid (logged "clear" vs "blind")
	Portal  Portal
	First   bool // ActCast: the episode's first cast (mark the breaker site)
}

// Decision is one Step's outcome.
type Decision struct {
	Act      Act
	V        phase.Verdict
	Why      phase.Reason
	Evidence string
	WakeAt   time.Time
}

// Tunables.
const (
	MaxStrides  = 3
	Displaced   = 6 // tiles from the site: out of the watchdog's 5-box
	StrideHold  = 2 * time.Second
	StrideGain  = 3
	PortalRange = 20
	PortalWait  = 3 * time.Second
	MaxCasts    = 2
	AreaWait    = 8 * time.Second
	MaxEnters   = 2
	siteRadius  = 10 // a new prescription this near the last remembers its tried bearings
	nBearings   = 16
)

// strideRanges: the longest clear shot wins; the old fling was 20 tiles.
var strideRanges = []int{20, 14, 9, 6}

// Machine is one Unstick episode at a time.
type Machine struct {
	Ph    phase.Phaser[Phase]
	Clock phase.Clock // nil = wall clock

	rx        Rx
	tried     []int // bearing indices tried near triedSite
	triedSite data.Position
	rot       int // the blind ladder's rotation, advanced every episode
	strides   int
	casts     int
	enters    int
	portal    Portal
}

// New builds a machine whose phase lines go to log (nil = silent).
func New(clock phase.Clock, log func(string)) *Machine {
	m := &Machine{Clock: clock}
	m.Ph = phase.Phaser[Phase]{Act: "unstick", Log: log, Clock: clock}
	// Held-time budgets per phase entry. A verb blocks inside the Step that
	// entered the next phase, so each wait phase carries its verb's hold too.
	m.Ph.Budget(Stride, 2*time.Second)
	m.Ph.Budget(VerifyDisplacement, StrideHold+2*time.Second)
	m.Ph.Budget(CastTP, 2*time.Second)
	m.Ph.Budget(AwaitPortal, PortalWait+2*time.Second)
	m.Ph.Budget(EnterPortal, 2*time.Second)
	m.Ph.Budget(AwaitArea, AreaWait+8*time.Second) // EnterPortal's own window rides inside
	return m
}

func (m *Machine) now() time.Time {
	if m.Clock == nil {
		return time.Now()
	}
	return m.Clock.Now()
}

// Rx is the prescription being worked.
func (m *Machine) Rx() Rx { return m.rx }

// Phase is the current phase.
func (m *Machine) Phase() Phase { return m.Ph.Phase() }

// Tried is the bearing indices tried near the current site.
func (m *Machine) Tried() []int { return append([]int(nil), m.tried...) }

// Start begins an episode for rx.
func (m *Machine) Start(rx Rx) {
	if cheb(rx.Site, m.triedSite) > siteRadius || len(m.tried) >= nBearings {
		m.tried = nil
	}
	m.triedSite = rx.Site
	m.rx = rx
	m.strides, m.casts, m.enters = 0, 0, 0
	m.portal = Portal{}
	m.rot = (m.rot + 3) % 8
	m.Ph.Reset()
	why := fmt.Sprintf("rx %s: %s", rx.Kind, rx.Evidence)
	if rx.Portal {
		m.Ph.To(CastTP, why)
	} else {
		m.Ph.To(Stride, why)
	}
}

// Upgrade moves a footwork episode to the portal when a portal prescription
// arrives mid-episode. ok is false when the episode is already on the portal.
func (m *Machine) Upgrade(rx Rx) bool {
	switch m.Ph.Phase() {
	case Stride, VerifyDisplacement:
		m.rx.Portal, m.rx.Kind, m.rx.Evidence = true, rx.Kind, rx.Evidence
		m.Ph.To(CastTP, fmt.Sprintf("upgraded by rx %s: %s", rx.Kind, rx.Evidence))
		return true
	}
	return false
}

func (m *Machine) done(ev string) Decision {
	m.Ph.To(Done, ev)
	return Decision{V: phase.Done, Why: phase.Completed, Evidence: ev}
}

func (m *Machine) abandon(why phase.Reason, ev string) Decision {
	m.Ph.To(Abandoned, ev)
	return Decision{V: phase.Abandoned, Why: why, Evidence: ev}
}

func (m *Machine) wait(d time.Duration) Decision {
	return Decision{V: phase.Wait, WakeAt: m.now().Add(d)}
}

func (m *Machine) arrived(o Obs) bool {
	return o.InTown || (o.Area != 0 && o.Area != m.rx.Area)
}

// Step decides this tick.
func (m *Machine) Step(o Obs) Decision {
	if over, ph := m.Ph.Overrun(o.Held); over {
		return m.abandon(phase.Timebox, fmt.Sprintf("%s overran its budget", ph))
	}
	switch m.Ph.Phase() {
	case Idle:
		m.Start(m.rx) // Begin never ran: start the prescription we hold
		return Decision{V: phase.Running}

	case Stride:
		if m.strides >= MaxStrides {
			return m.abandon(phase.Unreachable, fmt.Sprintf("footwork refuted: %d strides, still within %d of (%d,%d)",
				m.strides, Displaced, m.rx.Site.X, m.rx.Site.Y))
		}
		to, b, planned := m.bearing(o)
		if b < 0 {
			return m.abandon(phase.Unreachable, "every bearing tried from this pocket")
		}
		m.tried = append(m.tried, b)
		m.strides++
		how := "blind"
		if planned {
			how = "clear"
		}
		m.Ph.To(VerifyDisplacement, fmt.Sprintf("stride %d/%d bearing %d (%s) to (%d,%d)",
			m.strides, MaxStrides, b, how, to.X, to.Y))
		return Decision{V: phase.Running, Act: Act{Kind: ActStride, To: to, Hold: StrideHold, MinGain: StrideGain, Planned: planned}}

	case VerifyDisplacement:
		if m.arrived(o) {
			return m.done(fmt.Sprintf("left area %d", m.rx.Area))
		}
		d := cheb(o.Pos, m.rx.Site)
		if d >= Displaced {
			return m.done(fmt.Sprintf("displaced %d from (%d,%d)", d, m.rx.Site.X, m.rx.Site.Y))
		}
		m.Ph.To(Stride, fmt.Sprintf("displacement %d < %d", d, Displaced))
		return Decision{V: phase.Running}

	case CastTP:
		if o.InTown {
			return m.done("already in town")
		}
		if p, ok := m.nearestPortal(o); ok {
			m.portal = p
			m.Ph.To(EnterPortal, fmt.Sprintf("portal %d standing at (%d,%d)", p.ID, p.Pos.X, p.Pos.Y))
			return Decision{V: phase.Running}
		}
		if !o.CanPortal {
			return m.abandon(phase.Precondition, "no town-portal binding")
		}
		m.casts++
		m.Ph.To(AwaitPortal, fmt.Sprintf("cast %d/%d", m.casts, MaxCasts))
		return Decision{V: phase.Running, Act: Act{Kind: ActCast, First: m.casts == 1}}

	case AwaitPortal:
		if p, ok := m.nearestPortal(o); ok {
			m.portal = p
			m.Ph.To(EnterPortal, fmt.Sprintf("portal %d at (%d,%d)", p.ID, p.Pos.X, p.Pos.Y))
			return Decision{V: phase.Running}
		}
		if m.Ph.InPhase() >= PortalWait {
			if m.casts < MaxCasts {
				m.Ph.To(CastTP, fmt.Sprintf("no portal %s after cast %d", PortalWait, m.casts))
				return Decision{V: phase.Running}
			}
			return m.abandon(phase.Refused, fmt.Sprintf("no portal after %d casts (empty tome?)", m.casts))
		}
		return m.wait(200 * time.Millisecond)

	case EnterPortal:
		if m.arrived(o) {
			return m.done(fmt.Sprintf("area %d -> %d", m.rx.Area, o.Area))
		}
		if m.enters >= MaxEnters {
			return m.abandon(phase.Deaf, fmt.Sprintf("portal %d never took (%d clicks)", m.portal.ID, m.enters))
		}
		live := false
		for _, p := range o.Portals {
			if p.ID == m.portal.ID {
				m.portal, live = p, true
				break
			}
		}
		if !live {
			m.Ph.To(CastTP, fmt.Sprintf("portal %d gone", m.portal.ID))
			return Decision{V: phase.Running}
		}
		m.enters++
		m.Ph.To(AwaitArea, fmt.Sprintf("click %d/%d portal %d", m.enters, MaxEnters, m.portal.ID))
		return Decision{V: phase.Running, Act: Act{Kind: ActEnter, Portal: m.portal}}

	case AwaitArea:
		if m.arrived(o) {
			return m.done(fmt.Sprintf("area %d -> %d", m.rx.Area, o.Area))
		}
		if m.Ph.InPhase() >= AreaWait {
			m.Ph.To(EnterPortal, fmt.Sprintf("no transition %s after click %d", AreaWait, m.enters))
			return Decision{V: phase.Running}
		}
		return m.wait(250 * time.Millisecond)

	case Done:
		return Decision{V: phase.Done, Why: phase.Completed}
	}
	return Decision{V: phase.Abandoned, Why: phase.NoReason}
}

func (m *Machine) nearestPortal(o Obs) (Portal, bool) {
	best, bd := Portal{}, PortalRange+1
	for _, p := range o.Portals {
		if d := cheb(o.Pos, p.Pos); d < bd {
			best, bd = p, d
		}
	}
	return best, bd <= PortalRange
}

// novel: bearing k is neither tried nor a neighbour of a tried bearing.
func (m *Machine) novel(k int) bool {
	for _, t := range m.tried {
		d := (k - t + nBearings) % nBearings
		if d == 0 || d == 1 || d == nBearings-1 {
			return false
		}
	}
	return true
}

func dir(k int) (float64, float64) {
	a := float64(k) * 2 * math.Pi / nBearings
	return math.Cos(a), math.Sin(a)
}

func along(me data.Position, k, r int) data.Position {
	cx, cy := dir(k)
	return data.Position{X: me.X + int(math.Round(float64(r)*cx)), Y: me.Y + int(math.Round(float64(r)*cy))}
}

// bearing picks a NOVEL heading: on the grid, the untried bearing with the
// longest clear shot and the most room at its end (the nav clearance field);
// without one (or when the grid refuses every bearing — it is not always the
// truth), the old rotating 8-heading ladder, never a tried heading.
func (m *Machine) bearing(o Obs) (data.Position, int, bool) {
	if g := o.Grid; g != nil {
		best, bestK, bestScore := data.Position{}, -1, math.Inf(-1)
		for i := 0; i < nBearings; i++ {
			k := (2*m.rot + i) % nBearings
			if !m.novel(k) {
				continue
			}
			for _, r := range strideRanges {
				end := along(o.Pos, k, r)
				if !g.Walkable(end) || !lineClear(g, o.Pos, end) {
					continue
				}
				score := float64(r) + 2*float64(min(g.Clearance(end), 6))
				if score > bestScore {
					best, bestK, bestScore = end, k, score
				}
				break // the longest clear range speaks for this bearing
			}
		}
		if bestK >= 0 {
			return best, bestK, true
		}
	}
	for i := 0; i < 8; i++ {
		k := 2 * ((m.rot + 3*i) % 8)
		taken := false
		for _, t := range m.tried {
			if t == k {
				taken = true
				break
			}
		}
		if !taken {
			return along(o.Pos, k, 20), k, false
		}
	}
	return data.Position{}, -1, false
}

// lineClear: every cell on the segment after the start is walkable.
func lineClear(g *nav.Grid, a, b data.Position) bool {
	steps := cheb(a, b)
	for i := 1; i <= steps; i++ {
		p := data.Position{X: a.X + (b.X-a.X)*i/steps, Y: a.Y + (b.Y-a.Y)*i/steps}
		if !g.Walkable(p) {
			return false
		}
	}
	return true
}

func cheb(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return max(dx, dy)
}
