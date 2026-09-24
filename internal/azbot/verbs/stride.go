package verbs

import (
	"fmt"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Stride is the ONLY locomotion verb: one committed force-move stride toward a world
// target, built on the measured key-edge law (D2R samples direction at key-down).
// Postcondition: net displacement ≥ MinGain within the commit window — a stride that
// moved nothing reports ResBlocked with the position as evidence; it never self-retries.
type Stride struct {
	To      data.Position
	Hold    time.Duration // commit window; default 1.6s
	MinGain int           // required chebyshev displacement; default 4
	// Planned: To is a planner carrot already checked for line of sight. Skip
	// steerAround — a second steering authority bending a planned heading up to
	// ±112° fights the route (and walks off it into the next wall).
	Planned bool
	// Reach caps the cursor's distance from the player in logical px (0 = the
	// carrot box edge). The game walks toward the cursor point, so reach sets how
	// far one tap travels (R6). Measured by cmd/stridecal -reach before any use.
	Reach float64
}

// isoCarrot projects the TRUE direction to an always-in-window screen point
// (the off-window-discard law: far targets project off-screen and the game
// drops the cursor sample — walkCarrot's lesson, in the type). The angle is kept
// exactly (nav.ScreenCarrot): the old (300cos, 140sin) ellipse bent world-axis
// headings ~20°, straight into corridor walls.
func isoCarrot(gr *game.MemoryReader, me data.Position, tx, ty int, reach float64) (int, int) {
	ox, oy := nav.ScreenCarrotReach(float64(tx-me.X), float64(ty-me.Y), nav.IsoX, nav.IsoY, nav.CarrotX, nav.CarrotY, reach)
	return gr.GameAreaSizeX/2 + ox, gr.GameAreaSizeY/2 + oy
}

// Walkable is the live grid's verdict for one world tile (true for unknown/unloaded
// ground). The executive installs it; nil disables the look-ahead.
var Walkable func(data.Position) bool

// clearRay: the first n tiles from me along (fx,fy) are walkable.
func clearRay(me data.Position, fx, fy float64, n int) bool {
	for i := 1; i <= n; i++ {
		p := data.Position{X: me.X + int(math.Round(fx*float64(i))), Y: me.Y + int(math.Round(fy*float64(i)))}
		if !Walkable(p) {
			return false
		}
	}
	return true
}

// steerAround turns a walled heading into the nearest open one (±22.5° steps up
// to ±112.5°), returning a stand-in target along it. ok=false: boxed in.
// THE WALL-HUG (2026-09-23, the owner: "it seems like it's trying to run into some
// walls"): direction-only force-moves were emitted into collision and held 1.6s
// for gain=0, over and over — the look-ahead ends that at the source.
func steerAround(me, to data.Position) (data.Position, int, bool) {
	dx, dy := float64(to.X-me.X), float64(to.Y-me.Y)
	l := math.Hypot(dx, dy)
	if l < 1 || Walkable == nil {
		return to, 0, true
	}
	base := math.Atan2(dy, dx)
	for k := 0; k <= 5; k++ {
		for _, sgn := range []float64{1, -1} {
			if k == 0 && sgn < 0 {
				continue
			}
			a := base + sgn*float64(k)*math.Pi/8
			fx, fy := math.Cos(a), math.Sin(a)
			if clearRay(me, fx, fy, 4) {
				if k == 0 {
					return to, 0, true
				}
				return data.Position{X: me.X + int(fx*12), Y: me.Y + int(fy*12)}, int(sgn) * k, true
			}
		}
	}
	return to, 0, false
}

// Do executes the stride synchronously (M2 form; the polled state-machine form arrives
// with the arbiter). The caller holds a RoleSteer lease.
func (s Stride) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	hold := s.Hold
	if hold <= 0 {
		hold = 1600 * time.Millisecond
	}
	minGain := s.MinGain
	if minGain <= 0 {
		minGain = 4
	}
	start := p.Capture()
	if !start.Valid {
		o := Outcome{Verb: "stride", Holder: holder, Result: ResRefused, Evidence: "no valid snapshot"}
		led.Append(o)
		return o
	}
	to, turned, open := s.To, 0, true
	if !s.Planned {
		to, turned, open = steerAround(start.Me.Pos, s.To)
	}
	if !open {
		o := Outcome{Verb: "stride", Holder: holder, Target: fmt.Sprintf("(%d,%d)", s.To.X, s.To.Y), Result: ResBlocked,
			Evidence: fmt.Sprintf("boxed in at (%d,%d): no open heading within ±112° — no push emitted", start.Me.Pos.X, start.Me.Pos.Y)}
		time.Sleep(150 * time.Millisecond) // a refusal must never be free
		led.Append(o)
		return o
	}
	detour := ""
	if turned != 0 {
		detour = fmt.Sprintf(" detour=%+d°", turned*45/2)
	}
	ax, ay := isoCarrot(gr, start.Me.Pos, to.X, to.Y, s.Reach)
	if !m.StrideEdge(ax, ay) {
		o := Outcome{Verb: "stride", Holder: holder, Result: ResRefused, Evidence: "motor disengaged"}
		led.Append(o)
		return o
	}
	t0 := time.Now()
	// WATCHED HOLD (the owner: "why is it sluggish to react?" — the old blind
	// time.Sleep(hold) made every stride up to 1.6s of total reaction blindness while
	// monsters outran her). The hold now re-reads the world every 120ms and bails on
	// death, real damage, incoming fire, or early arrival.
	abort := ""
	missDist := map[data.UnitID]int{} // per-missile distance last read — the closing detector
	for _, ms := range start.Missiles {
		missDist[ms.ID] = chebyshev(start.Me.Pos, ms.Pos)
	}
	for time.Since(t0) < hold {
		time.Sleep(120 * time.Millisecond)
		cur := p.Capture()
		if !cur.Valid {
			continue // load screen / pause: let the postcondition read judge
		}
		if cur.Me.HPPct <= 0 {
			abort = " ABORT:died"
			break
		}
		if start.Me.HPPct-cur.Me.HPPct >= 10 {
			abort = " ABORT:damage" // reflexes over locomotion — hand the cycle back
			break
		}
		// INCOMING FIRE mid-stride: a missile that closed ≥2 tiles in one 120ms read
		// (~16+ tiles/s inbound) and is inside 20 tiles will land before this hold ends —
		// the arrow used to win because Dodge could not bid until the stride was over.
		// Yield now; the executive's next cycle hands the reflex the actuator. Short
		// pulses (≤400ms — Dodge's own sidesteps, the navigator's tight-space taps)
		// are exempt: they end quickly anyway, and a sidestep must not abort on the
		// very arrow it is escaping.
		if hold > 400*time.Millisecond {
			next := map[data.UnitID]int{}
			for _, ms := range cur.Missiles {
				d := chebyshev(cur.Me.Pos, ms.Pos)
				next[ms.ID] = d
				if prev, seen := missDist[ms.ID]; seen && prev-d >= 2 && d <= 20 {
					abort = " ABORT:missile"
				}
			}
			missDist = next
			if abort != "" {
				break
			}
		}
		if chebyshev(cur.Me.Pos, s.To) <= 2 {
			abort = " arrived-early"
			break
		}
	}
	m.MoveStop()
	end := p.Capture()
	held := time.Since(t0).Milliseconds()
	gain := 0
	if end.Valid {
		gain = chebyshev(start.Me.Pos, end.Me.Pos)
	}
	o := Outcome{
		Verb: "stride", Holder: holder,
		Target:   fmt.Sprintf("(%d,%d)", s.To.X, s.To.Y),
		HeldMS:   held,
		Evidence: fmt.Sprintf("from=(%d,%d) to=(%d,%d) gain=%d%s%s", start.Me.Pos.X, start.Me.Pos.Y, end.Me.Pos.X, end.Me.Pos.Y, gain, abort, detour),
	}
	switch {
	case !end.Valid:
		o.Result = ResTimeout
	case abort == " ABORT:damage" || abort == " ABORT:died" || abort == " ABORT:missile":
		o.Result = ResDone // yielded to reflexes — NOT a wall; no slide retries wanted
	case gain >= minGain:
		o.Result = ResDone
	default:
		o.Result = ResBlocked
	}
	led.Append(o)
	return o
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
