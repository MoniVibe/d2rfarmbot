package verbs

import (
	"fmt"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
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
}

// isoCarrot projects the TRUE direction to an always-in-window screen point
// (the off-window-discard law: far targets project off-screen and the game
// drops the cursor sample — walkCarrot's lesson, in the type).
func isoCarrot(gr *game.MemoryReader, me data.Position, tx, ty int) (int, int) {
	dx, dy := tx-me.X, ty-me.Y
	sx := float64(dx-dy) * 19.8
	sy := float64(dx+dy) * 9.9
	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	ang := math.Atan2(sy, sx)
	return cx + int(300*math.Cos(ang)), cy + int(140*math.Sin(ang))
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
	ax, ay := isoCarrot(gr, start.Me.Pos, s.To.X, s.To.Y)
	if !m.StrideEdge(ax, ay) {
		o := Outcome{Verb: "stride", Holder: holder, Result: ResRefused, Evidence: "motor disengaged"}
		led.Append(o)
		return o
	}
	t0 := time.Now()
	// WATCHED HOLD (the owner: "why is it sluggish to react?" — the old blind
	// time.Sleep(hold) made every stride up to 1.6s of total reaction blindness while
	// monsters outran her). The hold now re-reads the world every 120ms and bails on
	// death, real damage, or early arrival; the executive gets its reflexes back.
	abort := ""
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
		Evidence: fmt.Sprintf("from=(%d,%d) to=(%d,%d) gain=%d%s", start.Me.Pos.X, start.Me.Pos.Y, end.Me.Pos.X, end.Me.Pos.Y, gain, abort),
	}
	switch {
	case !end.Valid:
		o.Result = ResTimeout
	case abort == " ABORT:damage" || abort == " ABORT:died":
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
