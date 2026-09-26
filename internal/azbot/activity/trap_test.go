package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestTrapAimPicksDensestPack(t *testing.T) {
	me := data.Position{X: 100, Y: 100}
	en := []percept.EnemyRef{
		{Pos: data.Position{X: 103, Y: 100}},                                                                             // lone, near
		{Pos: data.Position{X: 110, Y: 100}}, {Pos: data.Position{X: 111, Y: 101}}, {Pos: data.Position{X: 110, Y: 102}}, // pack of 3
		{Pos: data.Position{X: 100, Y: 108}, Walled: true},
	}
	aim, n, ok := trapAim(me, en)
	if !ok || n != 3 || chebyshev(aim, data.Position{X: 110, Y: 101}) > 1 {
		t.Fatalf("aim %v pack %d ok %v, want the 3-pack", aim, n, ok)
	}
	if _, _, ok := trapAim(me, en[:1]); ok {
		t.Fatal("a lone monster is not worth a trap")
	}
	far := []percept.EnemyRef{{Pos: data.Position{X: 130, Y: 100}}, {Pos: data.Position{X: 131, Y: 100}}}
	if _, _, ok := trapAim(me, far); ok {
		t.Fatal("a pack beyond trapReach is the march's business")
	}
}

func TestTrapBudgetExpires(t *testing.T) {
	f := &Fight{}
	now := time.Now()
	for i := 0; i < trapMax; i++ {
		f.trapAt = append(f.trapAt, now.Add(-time.Duration(i)*time.Second))
	}
	if n := f.trapsLive(now); n != trapMax {
		t.Fatalf("live %d, want %d", n, trapMax)
	}
	if n := f.trapsLive(now.Add(trapLife)); n != 0 {
		t.Fatalf("after a trap life: live %d, want 0", n)
	}
}
