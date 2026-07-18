// Package watchdog is azbot's self-observation: the bot watching its own position,
// grant, and outcome streams for the pathologies the owner named — stuck, looping,
// thrashing — and prescribing its own unstick. The same signals a human reads in the
// logs, consumed in-band (LAW 3's ledger, closing its loop).
package watchdog

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

type Pathology uint8

const (
	Healthy Pathology = iota
	Stuck             // holder active, position pinned
	Orbit             // movement without displacement (the circle)
	Thrash            // grant flip-flopping between few holders
)

func (p Pathology) String() string {
	switch p {
	case Stuck:
		return "stuck"
	case Orbit:
		return "orbit"
	case Thrash:
		return "thrash"
	default:
		return "healthy"
	}
}

type posSample struct {
	p  data.Position
	at time.Time
}

type grantSample struct {
	who string
	at  time.Time
}

type Verdict struct {
	Pathology Pathology
	Detail    string
	// CoolDown: the watchdog's prescription — this holder may not win a grant again
	// until the moment passes (survival/recovery classes are never cooled).
	CoolWho   string
	CoolUntil time.Time
}

type Watchdog struct {
	pos    []posSample
	grants []grantSample
	lastRx time.Time
}

func New() *Watchdog { return &Watchdog{} }

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

// Observe feeds one cycle's state. holder is "" when idle.
func (w *Watchdog) Observe(pos data.Position, holder string) {
	now := time.Now()
	w.pos = append(w.pos, posSample{pos, now})
	for len(w.pos) > 0 && now.Sub(w.pos[0].at) > 45*time.Second {
		w.pos = w.pos[1:]
	}
	if holder != "" && (len(w.grants) == 0 || w.grants[len(w.grants)-1].who != holder) {
		w.grants = append(w.grants, grantSample{holder, now})
	}
	for len(w.grants) > 0 && now.Sub(w.grants[0].at) > 40*time.Second {
		w.grants = w.grants[1:]
	}
}

// Check runs the pathology tests. Rate-limited by the caller (every ~5s is plenty).
// stationaryOK: the caller vouches that standing still IS the work right now (a bow
// volley holds ground by design — cooling fight mid-volley cost her kills, measured
// 2026-07-19); it suppresses Stuck/Orbit but never Thrash.
func (w *Watchdog) Check(holder string, stationaryOK bool) Verdict {
	now := time.Now()

	// STUCK: an active holder, yet the trailing 20s of positions fit in a 5-tile box.
	if holder != "" && !stationaryOK && len(w.pos) >= 8 {
		var recent []posSample
		for _, s := range w.pos {
			if now.Sub(s.at) <= 20*time.Second {
				recent = append(recent, s)
			}
		}
		if len(recent) >= 8 && now.Sub(recent[0].at) > 15*time.Second {
			minX, maxX := recent[0].p.X, recent[0].p.X
			minY, maxY := recent[0].p.Y, recent[0].p.Y
			for _, s := range recent {
				if s.p.X < minX {
					minX = s.p.X
				}
				if s.p.X > maxX {
					maxX = s.p.X
				}
				if s.p.Y < minY {
					minY = s.p.Y
				}
				if s.p.Y > maxY {
					maxY = s.p.Y
				}
			}
			if maxX-minX <= 5 && maxY-minY <= 5 {
				return Verdict{Pathology: Stuck,
					Detail:    fmt.Sprintf("holder=%s pinned in %dx%d box for %ds", holder, maxX-minX, maxY-minY, int(now.Sub(recent[0].at).Seconds())),
					CoolWho:   holder,
					CoolUntil: now.Add(15 * time.Second)}
			}
			// ORBIT: real movement (path length) with no net displacement — the circle.
			path := 0
			for i := 1; i < len(recent); i++ {
				path += chebyshev(recent[i-1].p, recent[i].p)
			}
			net := chebyshev(recent[0].p, recent[len(recent)-1].p)
			if path > 60 && net < 12 {
				return Verdict{Pathology: Orbit,
					Detail:    fmt.Sprintf("holder=%s path=%d net=%d over %ds", holder, path, net, int(now.Sub(recent[0].at).Seconds())),
					CoolWho:   holder,
					CoolUntil: now.Add(15 * time.Second)}
			}
		}
	}

	// THRASH: ≥6 grant handovers in 30s across ≤3 distinct holders.
	if len(w.grants) >= 6 {
		var recent []grantSample
		for _, g := range w.grants {
			if now.Sub(g.at) <= 30*time.Second {
				recent = append(recent, g)
			}
		}
		if len(recent) >= 6 {
			distinct := map[string]bool{}
			for _, g := range recent {
				distinct[g.who] = true
			}
			if len(distinct) <= 3 {
				// Cool the most recent (lowest-priority contributor to the flip).
				return Verdict{Pathology: Thrash,
					Detail:    fmt.Sprintf("%d handovers/30s among %d holders", len(recent), len(distinct)),
					CoolWho:   recent[len(recent)-1].who,
					CoolUntil: now.Add(20 * time.Second)}
			}
		}
	}
	return Verdict{Pathology: Healthy}
}
