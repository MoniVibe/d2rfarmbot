package coverage

import (
	"fmt"
	"time"
)

// Params tune the exploration policy. Zero values take the defaults.
type Params struct {
	MinCluster int           // clusters smaller than this are noise (pillar shadows); default 3
	Chunk      int           // frontier chunk size (tiles); default DefaultChunk
	SizeW      float64       // tiles of path a frontier cell is worth; default 0.5
	SizeCap    int           // size credit saturates here (one chunk is one goal); default 24
	BiasW      float64       // tiles of score per tile of goal→exit distance; default 0.5
	Arrive     int           // Chebyshev arrival radius at a goal; default 3
	Keep       int           // a goal stays valid while frontier lies within this; default 4
	Settle     time.Duration // at a goal whose frontier persists, wait this long for rooms to stream in; default 8s
	NoProgress time.Duration // no approach this long = unreachable; default 30s
	BlackR     int           // a blacklisted goal bars frontier within this radius; default 6
	DoneCov    float64       // the area is done at this coverage; default 0.95
}

func (p Params) withDefaults() Params {
	if p.MinCluster <= 0 {
		p.MinCluster = 3
	}
	if p.Chunk == 0 {
		p.Chunk = DefaultChunk
	}
	if p.SizeW == 0 {
		p.SizeW = 0.5
	}
	if p.SizeCap <= 0 {
		p.SizeCap = 24
	}
	if p.BiasW == 0 {
		p.BiasW = 0.5
	}
	if p.Arrive <= 0 {
		p.Arrive = 3
	}
	if p.Keep <= 0 {
		p.Keep = 4
	}
	if p.Settle <= 0 {
		p.Settle = 8 * time.Second
	}
	if p.NoProgress <= 0 {
		p.NoProgress = 30 * time.Second
	}
	if p.BlackR <= 0 {
		p.BlackR = 6
	}
	if p.DoneCov <= 0 {
		p.DoneCov = 0.95
	}
	return p
}

// Bias is the leg's direction hint: an exit/door position Advance or the route
// believes in. Key names the hint so a CHANGED hint re-picks the goal.
type Bias struct {
	At  Pos
	Key string
	OK  bool
}

// Status is the policy's verdict on the area.
type Status int

const (
	Exploring    Status = iota
	NoFrontier          // no reachable frontier remains: the area is done
	CoverageDone        // coverage reached DoneCov: the area is done
)

func (s Status) String() string {
	switch s {
	case Exploring:
		return "exploring"
	case NoFrontier:
		return "no-frontier"
	case CoverageDone:
		return "covered"
	}
	return "?"
}

// Pick is one chosen exploration goal.
type Pick struct {
	Goal    Pos
	Cluster int // cluster ID in the extraction it came from
	Size    int
	Cost    int // path cost in tiles (nav metric)
	Score   float64
	Why     string
	Path    []Pos // the cost field's route, start..goal (for the debug picture)
	Bias    string
	At      time.Time
}

// Policy picks frontier goals for ONE area visit: its blacklist dies with the
// visit (the map itself persists).
type Policy struct {
	P Params

	cur       *Pick
	bestD     int // closest Chebyshev approach to the goal
	bestR     int // fewest path cells remaining (a winding cave route rarely closes Chebyshev)
	bestAt    time.Time
	arrivedAt time.Time
	black     []Pos
}

// Current is the goal in force (nil when none).
func (p *Policy) Current() *Pick { return p.cur }

// Blacklist bars frontier near at for the rest of this visit.
func (p *Policy) Blacklist(at Pos) {
	p.black = append(p.black, at)
	if p.cur != nil && cheb(p.cur.Goal, at) <= p.P.withDefaults().BlackR {
		p.cur = nil
	}
}

// Fail convicts the current goal (planner NoPath, a stalled walk): blacklisted.
func (p *Policy) Fail() {
	if p.cur != nil {
		p.Blacklist(p.cur.Goal)
	}
}

// Blacklisted lists the barred goals (debug picture).
func (p *Policy) Blacklisted() []Pos { return p.black }

func (p *Policy) barred(q Pos, r int) bool {
	for _, b := range p.black {
		if cheb(b, q) <= r {
			return true
		}
	}
	return false
}

// eligible filters the frontier: big enough, not barred.
func (p *Policy) eligible(m *Map, t *Terrain, P Params) []Cluster {
	var out []Cluster
	for _, c := range m.Frontier(t, P.Chunk) {
		if c.Size < P.MinCluster || p.barred(c.Centroid, P.BlackR) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Next returns the goal to walk to, or a done verdict. It keeps the current
// goal while its frontier survives and she approaches it; it re-picks when the
// frontier there is resolved, the bias changes, or the goal is convicted
// (30 s without approach, or arrival with nothing revealed after Settle).
// changed reports a fresh pick (the caller logs it and starts a new route).
func (p *Policy) Next(m *Map, t *Terrain, me Pos, b Bias, now time.Time) (pk Pick, st Status, changed bool) {
	P := p.P.withDefaults()
	if m == nil || t == nil || !m.Fits(t) {
		return Pick{}, NoFrontier, false
	}
	cl := p.eligible(m, t, P)
	if p.cur != nil {
		cur := p.cur
		alive := false
		for _, c := range cl {
			if cheb(c.Centroid, cur.Goal) > P.Keep+P.Chunk*2 {
				continue // cheap reject before the member scan
			}
			for _, q := range c.Cells {
				if cheb(q, cur.Goal) <= P.Keep {
					alive = true
					break
				}
			}
			if alive {
				break
			}
		}
		biasKey := ""
		if b.OK {
			biasKey = b.Key
		}
		switch {
		case !alive || biasKey != cur.Bias:
			p.cur = nil // resolved (or re-aimed): pick again below
		default:
			d := cheb(me, cur.Goal)
			if d < p.bestD {
				p.bestD, p.bestAt = d, now
			}
			if r := remaining(cur.Path, me); r < p.bestR {
				p.bestR, p.bestAt = r, now
			}
			if d <= P.Arrive {
				if p.arrivedAt.IsZero() {
					p.arrivedAt = now
				}
				if now.Sub(p.arrivedAt) > P.Settle {
					p.Blacklist(cur.Goal) // stood there; the frontier never resolved
					cl = p.eligible(m, t, P)
					break
				}
				return *cur, Exploring, false
			}
			p.arrivedAt = time.Time{}
			if now.Sub(p.bestAt) > P.NoProgress {
				p.Blacklist(cur.Goal) // no approach in 30 s: unreachable in practice
				cl = p.eligible(m, t, P)
				break
			}
			return *cur, Exploring, false
		}
	}
	if m.CoverageAll(t) >= P.DoneCov {
		return Pick{}, CoverageDone, false
	}
	if len(cl) == 0 {
		return Pick{}, NoFrontier, false
	}
	targets := make([]Pos, len(cl))
	for i, c := range cl {
		targets[i] = c.Centroid
	}
	cf := m.Costs(t, me, targets)
	if cf == nil {
		return Pick{}, NoFrontier, false
	}
	best, bestS := -1, 0.0
	for i, c := range cl {
		d := cf.Dist(c.Centroid)
		if d == Unreachable {
			continue // NoPath today; the terrain may open it later
		}
		s := float64(d)/costStraight - P.SizeW*float64(minInt(c.Size, P.SizeCap))
		if b.OK {
			s += P.BiasW * float64(cheb(c.Centroid, b.At))
		}
		if best < 0 || s < bestS {
			best, bestS = i, s
		}
	}
	if best < 0 {
		return Pick{}, NoFrontier, false
	}
	c := cl[best]
	cost := int(cf.Dist(c.Centroid)) / costStraight
	why := fmt.Sprintf("nearest-by-path of %d clusters", len(cl))
	if b.OK {
		why = fmt.Sprintf("nearest-by-path of %d clusters, biased to %s (%d,%d)", len(cl), b.Key, b.At.X, b.At.Y)
	}
	pk = Pick{Goal: c.Centroid, Cluster: c.ID, Size: c.Size, Cost: cost, Score: bestS,
		Why: why, Path: cf.Path(c.Centroid), At: now}
	if b.OK {
		pk.Bias = b.Key
	}
	p.cur = &pk
	p.bestD, p.bestR, p.bestAt, p.arrivedAt = cheb(me, pk.Goal), remaining(pk.Path, me), now, time.Time{}
	return pk, Exploring, true
}

// remaining: path cells left after the furthest path point within 2 of her
// (len(path) when she is off the route).
func remaining(path []Pos, me Pos) int {
	for i := len(path) - 1; i >= 0; i-- {
		if cheb(path[i], me) <= 2 {
			return len(path) - 1 - i
		}
	}
	return len(path)
}
