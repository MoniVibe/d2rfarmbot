// Package route holds the march's pure door and ladder decisions, split out of
// activity so Linux tests can pin them (activity imports the Windows reader).
//
// The failure it exists for (relay R1, run w, seed 466817790): Dry Hills 42 ->
// Halls of the Dead 56 marched at tgt=(15080,6580) from (5630,4558) — a door
// 9450 tiles outside the level she stood in — for 20 minutes, while the ladder
// logged "rung 3 (fail-leg) for intent.56" over and over and changed nothing.
package route

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// Rect is a world-subtile frame: a live DrlgLevel placement or a map grid.
type Rect struct{ X, Y, W, H int }

func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

func (r Rect) Contains(p data.Position) bool {
	return !r.Empty() && p.X >= r.X && p.Y >= r.Y && p.X < r.X+r.W && p.Y < r.Y+r.H
}

func (r Rect) Grow(m int) Rect { return Rect{r.X - m, r.Y - m, r.W + 2*m, r.H + 2*m} }

func (r Rect) String() string { return fmt.Sprintf("[%d,%d %dx%d]", r.X, r.Y, r.W, r.H) }

// DoorMargin: how far outside the live frame a door may sit — a walkable
// border's nearest far-side room tile, or a map exit on the seam.
const DoorMargin = 60

// MaxLevelSpan: the live grid builder rejects frames wider than this, so a door
// farther than this from her cannot belong to the level she stands in.
const MaxLevelSpan = 4000

func Cheb(a, b data.Position) int {
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

// Plausible reports whether p can be a door out of the level she stands in.
// The zero value means "unknown", never a place. With a live frame that holds
// her (a frame that doesn't is stale — last area's grid), the door must sit in
// it plus the seam margin; without one, within a level's span of her.
func Plausible(p, me data.Position, live Rect) bool {
	if p == (data.Position{}) {
		return false
	}
	if live.Contains(me) {
		return live.Grow(DoorMargin).Contains(p)
	}
	return Cheb(me, p) <= MaxLevelSpan
}

// Candidate is one door hint and where it came from (fact, map, map-entrance,
// rooms), in the order borderTarget trusts them.
type Candidate struct {
	Src string
	Pos data.Position
}

// Pick is PickDoor's answer. Rejected lists the implausible candidates skipped
// on the way, for the ledger; disbelieved ones are skipped silently.
type Pick struct {
	Door     Candidate
	Known    bool
	Rejected []Candidate
}

// PickDoor returns the first candidate that is plausible and not disbelieved.
// Nothing left is Known=false: the caller explores; it never marches a zero or
// a phantom.
func PickDoor(cands []Candidate, me data.Position, live Rect, disbelieved func(data.Position) bool) Pick {
	var out Pick
	for _, c := range cands {
		if !Plausible(c.Pos, me, live) {
			out.Rejected = append(out.Rejected, c)
			continue
		}
		if disbelieved != nil && disbelieved(c.Pos) {
			continue
		}
		out.Door, out.Known = c, true
		return out
	}
	return out
}

// Project maps p from one frame into another by its relative place in the
// frame, clamped inside. A map hint that lies (the mod moves levels) may still
// say which side of the level the exit is on; it steers exploration, never a
// march.
func Project(p data.Position, from, to Rect) (data.Position, bool) {
	if !from.Contains(p) || to.Empty() {
		return data.Position{}, false
	}
	x := to.X + (p.X-from.X)*to.W/from.W
	y := to.Y + (p.Y-from.Y)*to.H/from.H
	return data.Position{X: min(max(x, to.X), to.X+to.W-1), Y: min(max(y, to.Y), to.Y+to.H-1)}, true
}

// Disbelief holds door positions the ladder convicted, each until an expiry,
// plus a blanket window in which every hint is doubted. Expiring, not
// session-long: rooms stream in and a hint may earn a second look.
type Disbelief struct {
	until    map[data.Position]time.Time
	allUntil time.Time
}

func (d *Disbelief) Add(p data.Position, until time.Time) {
	if p == (data.Position{}) {
		return
	}
	if d.until == nil {
		d.until = map[data.Position]time.Time{}
	}
	d.until[p] = until
}

func (d *Disbelief) Blind(until time.Time) { d.allUntil = until }

func (d *Disbelief) Clear() { *d = Disbelief{} }

// Has reports whether p is disbelieved at now; within 8 tiles counts (a hint
// refreshed by a live unit drifts a few tiles).
func (d *Disbelief) Has(p data.Position, now time.Time) bool {
	if now.Before(d.allUntil) {
		return true
	}
	for q, u := range d.until {
		if !now.Before(u) {
			delete(d.until, q)
			continue
		}
		if Cheb(p, q) <= 8 {
			return true
		}
	}
	return false
}

// Rung is one step of the escalation ladder.
type Rung int

const (
	Replan Rung = iota
	Alternate
	Portal
	FailLeg
)

func (r Rung) String() string {
	switch r {
	case Replan:
		return "replan"
	case Alternate:
		return "alternate"
	case Portal:
		return "portal"
	default:
		return "fail-leg"
	}
}

// LadderQuiet: a verdict this long after the last climb starts a fresh ladder —
// old convictions must not stack onto a plan that has since been replaced.
const LadderQuiet = 3 * time.Minute

// Ladder is the escalation state for one key (the committed leg). Cycle counts
// how many times it passed FAIL-LEG, so each pass can reach for a different
// remedy instead of repeating the same one.
type Ladder struct {
	Key   string
	Rung  Rung
	At    time.Time
	Cycle int
}

// Climb: a new key or a quiet ladder starts at REPLAN; otherwise one rung up.
// Past FAIL-LEG it wraps to REPLAN on the next cycle — the old ladder topped
// out and returned FAIL-LEG forever (run w: 20+ identical lines in 20 min).
func (l Ladder) Climb(key string, now time.Time) Ladder {
	switch {
	case l.Key != key:
		return Ladder{Key: key, Rung: Replan, At: now}
	case now.Sub(l.At) > LadderQuiet:
		return Ladder{Key: key, Rung: Replan, At: now, Cycle: l.Cycle}
	case l.Rung >= FailLeg:
		return Ladder{Key: key, Rung: Replan, At: now, Cycle: l.Cycle + 1}
	}
	l.Rung++
	l.At = now
	return l
}

// SearchWindow is how long FAIL-LEG hands the march to the coverage search:
// longer each cycle, capped so a real door is not ignored for long.
func SearchWindow(cycle int) time.Duration {
	w := time.Duration(cycle+1) * 30 * time.Second
	if w > 2*time.Minute {
		w = 2 * time.Minute
	}
	return w
}

// TurnHeading rotates a search bearing index by 135° (3 of 8), so each
// FAIL-LEG tours a new side of the level. Kept non-zero: search treats 0 as
// "unset".
func TurnHeading(h, n int) int {
	h = (h+3)%n + n
	return h
}
