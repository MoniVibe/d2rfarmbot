// Package watchdog is azbot's self-observation: the bot watching its own position
// and grant streams for the pathologies the owner named — stuck, looping,
// thrashing, pinned, pacing — and PRESCRIBING a remedy. It never actuates
// (docs/AZBOT_V2.md, executive tick step 8: monitors write prescriptions for the
// next tick). The executive benches the culprit and the Unstick activity carries
// the remedy out under the arbiter like any other holder.
//
// PURE: stdlib and d2go/pkg/data only, with an injectable clock — every trigger
// is proven by synthetic position traces on Linux.
package watchdog

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

type Pathology uint8

const (
	Healthy Pathology = iota
	Stuck             // holder active, position pinned in a small box
	Orbit             // movement without displacement (the circle)
	Thrash            // grant flip-flopping between few holders
	Pinned            // the deadman box: long in a 6-box on hostile ground, whoever holds
	Pacer             // the pacer's deadman: feet moving, centroid not (shuttling between pockets)
)

func (p Pathology) String() string {
	switch p {
	case Stuck:
		return "stuck"
	case Orbit:
		return "orbit"
	case Thrash:
		return "thrash"
	case Pinned:
		return "pinned"
	case Pacer:
		return "pacer"
	default:
		return "healthy"
	}
}

// Remedy is the watchdog's recommendation. The watchdog only names it; the
// Unstick activity performs it.
type Remedy uint8

const (
	RemedyNone   Remedy = iota // bench only (thrash is a decision problem, not a place)
	RemedyStride               // Unstick: a novel-bearing stride, verified by displacement
	RemedyPortal               // Unstick: cast a town portal and ride it home
)

func (r Remedy) String() string {
	switch r {
	case RemedyStride:
		return "unstick-stride"
	case RemedyPortal:
		return "unstick-portal"
	default:
		return "none"
	}
}

// Clock is injectable so traces replay on recorded time.
type Clock interface{ Now() time.Time }

// Tunables — the measured bars the executive used before the watchdog went pure.
const (
	CheckEvery   = 5 * time.Second  // stuck/orbit/thrash cadence
	MaxGap       = 10 * time.Second // an observation gap this long restarts the monitors
	tenureMin    = 8 * time.Second  // a holder owns only its own box
	stuckWindow  = 20 * time.Second
	stuckSpan    = 15 * time.Second
	stuckBox     = 5
	orbitPath    = 60
	orbitNet     = 12
	thrashWindow = 30 * time.Second
	thrashMin    = 6
	thrashFew    = 3
	runRadius    = 10 // stuck/orbit verdicts within this of the run's anchor count as one pocket
	runPortal    = 4  // the pocket breaker: this many verdicts in one pocket refute footwork
	runPortalHot = 10 // ...or this many while the march holds a door (a long leash)
	deadmanBox   = 6
	deadmanAfter = 75 * time.Second
	pacerDrift   = 12
	pacerAfter   = 3 * time.Minute
	PortalEvery  = 120 * time.Second // at most one portal prescription per this
	StuckBench   = 6 * time.Second
	ThrashBench  = 8 * time.Second
)

type posSample struct {
	p  data.Position
	at time.Time
}

type grantSample struct {
	who string
	at  time.Time
}

// Sample is one tick's observation.
type Sample struct {
	Pos    data.Position
	Holder string // the grant holder; "" when idle
	InTown bool
	Dead   bool
}

// Context is what the executive vouches for at Check time.
type Context struct {
	Holder string
	// StationaryOK: standing still IS the work right now (a volley, a melee
	// stand). Suppresses Stuck/Orbit, never Thrash. See StationaryOK.
	StationaryOK bool
	// CrossingHot: the march holds a door (P-5.10). The watchdog prescribes no
	// footwork of its own there — the escape fling was the door pendulum — but
	// the pocket breaker still counts toward a portal on a long leash.
	CrossingHot bool
	// CanPortal: field ground, alive, and a town-portal binding exists.
	CanPortal bool
	// InService: the holder claims a panel this tick (an NPC menu, a shop,
	// the bag, the char sheet) — a town errand's Talk/Menu/Act, Equip's
	// Dress, Spend's Stats: standing still IS the work.
	InService bool
	// CursorItem: an item rides the cursor. Whoever holds, a conviction now
	// hands the grant to a holder that does not own the item — and R10's
	// did: "equip pinned in 0x0 box for 19s" benched the parker mid-Dress
	// and the next holder's gate dropped the item on the town floor.
	CursorItem bool
}

// standsStill: the context vouches that stillness is not a pathology this
// tick — no Stuck, Orbit, Pinned or Pacer conviction (Thrash is still watched).
func (c Context) standsStill() bool { return c.InService || c.CursorItem }

// Verdict is one conviction: what, who, where, the evidence, and the remedy.
type Verdict struct {
	Pathology Pathology
	Holder    string        // the grant holder at conviction
	Culprit   string        // who to bench ("" = the place is convicted, not a holder)
	BenchFor  time.Duration // how long the culprit may not win a grant
	Remedy    Remedy
	Evidence  string
	Site      data.Position
	At        time.Time
	// Involved: every holder in a thrash window, most handovers first. The
	// executive picks whom to bench by class — the latest winner may be the
	// fight itself (relay R4: 74 thrash verdicts, nearly all mid-fight).
	Involved []string
}

// Summary is "kind evidence" — the bench reason and the log's why.
func (v Verdict) Summary() string { return v.Pathology.String() + " " + v.Evidence }

// Watchdog holds every trigger's state. Build with New.
type Watchdog struct {
	Clock Clock // nil = wall clock
	// Blips: holders whose grants are reflexes, not decisions — they neither
	// count as handovers (dodge↔fight alternation IS the arrow dance) nor
	// start a new tenure.
	Blips map[string]bool
	// Immune: holders the watchdog never convicts, and whose tenure suspends
	// every check (the Unstick activity carrying out a prescription).
	Immune map[string]bool

	pos      []posSample
	grants   []grantSample
	holder   string
	holderAt time.Time
	lastObs  time.Time
	last     Sample
	checkAt  time.Time

	// The pocket breaker's run: stuck/orbit verdicts in one pocket.
	runPos data.Position
	runN   int
	// The deadman box.
	boxPos data.Position
	boxAt  time.Time
	boxOK  bool
	// The pacer's EMA centroid.
	emaX, emaY float64
	emaOK      bool
	emaRef     data.Position
	emaRefAt   time.Time

	portalAt time.Time // last portal prescription (survives Reset)
}

// New returns a watchdog with the standing policy: dodge is a blip, unstick is
// both a blip and immune.
func New() *Watchdog {
	return &Watchdog{
		Blips:  map[string]bool{"dodge": true, "unstick": true},
		Immune: map[string]bool{"unstick": true},
	}
}

func (w *Watchdog) now() time.Time {
	if w.Clock == nil {
		return time.Now()
	}
	return w.Clock.Now()
}

// Reset restarts every position/grant monitor (a refocus, a long gate block, an
// observation gap: a frozen history would read as pathology). The pocket
// breaker's run and the portal rate limit survive — a reset is not an escape.
func (w *Watchdog) Reset() {
	w.pos, w.grants = nil, nil
	w.holder, w.holderAt = "", time.Time{}
	w.lastObs, w.checkAt = time.Time{}, time.Time{}
	w.boxOK, w.emaOK = false, false
}

func chebyshev(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// Observe feeds one tick.
func (w *Watchdog) Observe(s Sample) {
	now := w.now()
	if !w.lastObs.IsZero() && now.Sub(w.lastObs) > MaxGap {
		w.Reset()
	}
	w.lastObs, w.last = now, s

	w.pos = append(w.pos, posSample{s.Pos, now})
	for len(w.pos) > 0 && now.Sub(w.pos[0].at) > 45*time.Second {
		w.pos = w.pos[1:]
	}
	h := s.Holder
	if w.Blips[h] {
		h = ""
	}
	if h != "" && (len(w.grants) == 0 || w.grants[len(w.grants)-1].who != h) {
		w.grants = append(w.grants, grantSample{h, now})
	}
	for len(w.grants) > 0 && now.Sub(w.grants[0].at) > 40*time.Second {
		w.grants = w.grants[1:]
	}
	if !w.Blips[s.Holder] && s.Holder != w.holder {
		w.holder, w.holderAt = s.Holder, now
	}

	// THE DEADMAN BOX (21:29: ten minutes pinned in a UP pocket while the pocket
	// breaker STARVED — it counted blocked strides, and a fight-class holder
	// never strides). Position truth needs no holder's cooperation.
	if !w.boxOK || chebyshev(s.Pos, w.boxPos) > deadmanBox || s.InTown || s.Dead {
		w.boxPos, w.boxAt, w.boxOK = s.Pos, now, true
	}
	// THE PACER'S DEADMAN (00:44: "going back and forth most of the time"):
	// shuttling between two pockets resets every box forever; the EMA of the
	// position barely moves while the feet never stop.
	if !w.emaOK {
		w.emaX, w.emaY, w.emaOK = float64(s.Pos.X), float64(s.Pos.Y), true
		w.emaRef, w.emaRefAt = s.Pos, now
	} else {
		w.emaX = w.emaX*0.98 + float64(s.Pos.X)*0.02
		w.emaY = w.emaY*0.98 + float64(s.Pos.Y)*0.02
	}
	c := data.Position{X: int(w.emaX), Y: int(w.emaY)}
	if s.InTown || s.Dead || chebyshev(c, w.emaRef) > pacerDrift {
		w.emaRef, w.emaRefAt = c, now
	}
}

// StationaryHolders hold ground by design: fight (a bow volley) and stand
// (convicting it mid-melee benched the survival holder while HP fell, R1 run u).
var StationaryHolders = map[string]bool{"fight": true, "stand": true}

// StationaryRange is how near an enemy must be to vouch for stillness.
const StationaryRange = 28

// StationaryOK: a stationary holder with a live enemy in range is working, not stuck.
func StationaryOK(holder string, me data.Position, enemies []data.Position) bool {
	if !StationaryHolders[holder] {
		return false
	}
	for _, e := range enemies {
		if chebyshev(me, e) <= StationaryRange {
			return true
		}
	}
	return false
}

func (w *Watchdog) portalReady(c Context, now time.Time) bool {
	return c.CanPortal && !w.last.InTown && !w.last.Dead &&
		(w.portalAt.IsZero() || now.Sub(w.portalAt) > PortalEvery)
}

// prescribedPortal records a portal prescription: the rate limit starts, and
// every position monitor restarts (the portal is the escape).
func (w *Watchdog) prescribedPortal(now time.Time) {
	w.portalAt = now
	w.runN = 0
	w.boxPos, w.boxAt = w.last.Pos, now
	w.emaRefAt = now
}

// Check runs the tests. Call it every tick: Pinned/Pacer are judged each call,
// Stuck/Orbit/Thrash every CheckEvery. At most one verdict per call.
func (w *Watchdog) Check(c Context) Verdict {
	now := w.now()
	if w.Immune[c.Holder] || w.lastObs.IsZero() {
		return Verdict{}
	}
	me := w.last.Pos
	if c.standsStill() {
		// Stillness vouched for (a service at its panel, an item on the
		// cursor): the position clocks restart, so the vouched time never
		// counts once it ends — the tenure, the deadman box, the pacer.
		w.holderAt = now
		w.boxPos, w.boxAt = me, now
		w.emaRef, w.emaRefAt = data.Position{X: int(w.emaX), Y: int(w.emaY)}, now
	}

	if w.portalReady(c, now) {
		if now.Sub(w.boxAt) > deadmanAfter {
			v := Verdict{Pathology: Pinned, Holder: c.Holder, Remedy: RemedyPortal, Site: me, At: now,
				Evidence: fmt.Sprintf("%ds in a %d-box at (%d,%d) holder=%s",
					int(now.Sub(w.boxAt).Seconds()), deadmanBox, w.boxPos.X, w.boxPos.Y, orDash(c.Holder))}
			w.prescribedPortal(now)
			return v
		}
		if now.Sub(w.emaRefAt) > pacerAfter {
			v := Verdict{Pathology: Pacer, Holder: c.Holder, Remedy: RemedyPortal, Site: me, At: now,
				Evidence: fmt.Sprintf("centroid (%d,%d) drifted <%d in %ds holder=%s",
					w.emaRef.X, w.emaRef.Y, pacerDrift, int(now.Sub(w.emaRefAt).Seconds()), orDash(c.Holder))}
			w.prescribedPortal(now)
			return v
		}
	}

	if !w.checkAt.IsZero() && now.Sub(w.checkAt) < CheckEvery {
		return Verdict{}
	}
	w.checkAt = now

	holder := c.Holder
	if holder != "" && !c.StationaryOK && holder == w.holder && now.Sub(w.holderAt) > tenureMin {
		if v, ok := w.pocket(c, now); ok {
			return v
		}
	}
	return w.thrash(c, now)
}

// pocket runs STUCK and ORBIT over the trailing window.
func (w *Watchdog) pocket(c Context, now time.Time) (Verdict, bool) {
	var recent []posSample
	for _, s := range w.pos {
		if now.Sub(s.at) <= stuckWindow {
			recent = append(recent, s)
		}
	}
	if len(recent) < 8 || now.Sub(recent[0].at) <= stuckSpan {
		return Verdict{}, false
	}
	minX, maxX := recent[0].p.X, recent[0].p.X
	minY, maxY := recent[0].p.Y, recent[0].p.Y
	for _, s := range recent {
		minX, maxX = min(minX, s.p.X), max(maxX, s.p.X)
		minY, maxY = min(minY, s.p.Y), max(maxY, s.p.Y)
	}
	span := int(now.Sub(recent[0].at).Seconds())
	v := Verdict{Holder: c.Holder, Culprit: c.Holder, BenchFor: StuckBench, Site: w.last.Pos, At: now}
	if maxX-minX <= stuckBox && maxY-minY <= stuckBox {
		v.Pathology = Stuck
		v.Evidence = fmt.Sprintf("holder=%s pinned in %dx%d box for %ds", c.Holder, maxX-minX, maxY-minY, span)
	} else {
		path := 0
		for i := 1; i < len(recent); i++ {
			path += chebyshev(recent[i-1].p, recent[i].p)
		}
		net := chebyshev(recent[0].p, recent[len(recent)-1].p)
		if path <= orbitPath || net >= orbitNet {
			return Verdict{}, false
		}
		v.Pathology = Orbit
		v.Evidence = fmt.Sprintf("holder=%s path=%d net=%d over %ds", c.Holder, path, net, span)
	}
	var run int
	v.Remedy, run = w.pocketRemedy(c, now)
	v.Evidence += fmt.Sprintf(" run=%d", run)
	return v, true
}

// pocketRemedy is THE POCKET BREAKER (01:23: pinned in a Stony pen, every local
// maneuver a wiggle inside the box): repeated verdicts in one pocket refute
// footwork — the portal is the door. Otherwise one novel-bearing stride; at a
// held door, no footwork at all (P-5.10) until the long leash runs out.
// The second result is the run count this verdict made.
func (w *Watchdog) pocketRemedy(c Context, now time.Time) (Remedy, int) {
	if chebyshev(w.last.Pos, w.runPos) > runRadius {
		w.runPos, w.runN = w.last.Pos, 0
	}
	w.runN++
	n := w.runN
	need := runPortal
	if c.CrossingHot {
		need = runPortalHot
	}
	if n >= need && w.portalReady(c, now) {
		w.prescribedPortal(now) // restarts the run
		return RemedyPortal, n
	}
	if c.CrossingHot {
		return RemedyNone, n
	}
	return RemedyStride, n
}

// thrash: ≥6 grant handovers in 30s across ≤3 distinct holders. Bench only —
// thrash is a decision problem; the evidence names the pair that ping-pongs.
func (w *Watchdog) thrash(c Context, now time.Time) Verdict {
	var recent []grantSample
	for _, g := range w.grants {
		if now.Sub(g.at) <= thrashWindow {
			recent = append(recent, g)
		}
	}
	if len(recent) < thrashMin {
		return Verdict{}
	}
	count := map[string]int{}
	for _, g := range recent {
		count[g.who]++
	}
	if len(count) > thrashFew {
		return Verdict{}
	}
	names := make([]string, 0, len(count))
	for n := range count {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if count[names[i]] != count[names[j]] {
			return count[names[i]] > count[names[j]]
		}
		return names[i] < names[j]
	})
	pair := names
	if len(pair) > 2 {
		pair = pair[:2]
	}
	culprit := recent[len(recent)-1].who // the most recent: the flip's latest winner
	return Verdict{Pathology: Thrash, Holder: c.Holder, Culprit: culprit, BenchFor: ThrashBench,
		Remedy: RemedyNone, Site: w.last.Pos, At: now, Involved: names,
		Evidence: fmt.Sprintf("%s: %d handovers/30s among %d holders", strings.Join(pair, "<->"), len(recent), len(count))}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
