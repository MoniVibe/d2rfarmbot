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
	time.Sleep(hold) // motor-owned wait class: bounded by the verb's own budget
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
		Evidence: fmt.Sprintf("from=(%d,%d) to=(%d,%d) gain=%d", start.Me.Pos.X, start.Me.Pos.Y, end.Me.Pos.X, end.Me.Pos.Y, gain),
	}
	switch {
	case !end.Valid:
		o.Result = ResTimeout
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
