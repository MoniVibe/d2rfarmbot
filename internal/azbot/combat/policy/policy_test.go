package policy

import (
	"testing"
	"time"
)

// carnage is Fableboi's 2026-09-24 kit: Carnage on the left, Leap Attack and
// Double Swing proven on the right (Calibrate makes Leap Attack the Combat
// binding when it is proven).
func carnage() Inputs {
	return Inputs{LeftProven: true, LeapProven: true, SwingProven: true,
		CombatProven: true, CombatIsLeap: true, MPPct: 80, LeapReady: true, HoverOK: true}
}

func TestChoose(t *testing.T) {
	cases := []struct {
		name string
		in   func() Inputs
		want Kind
	}{
		{"left in reach, full pool: carnage, no key swap", func() Inputs { in := carnage(); in.Dist = 2; return in }, Left},
		{"left point-blank, dry pool: still carnage", func() Inputs { in := carnage(); in.Dist = 1; in.MPPct = 3; in.LeapReady = false; return in }, Left},
		{"left, target beyond reach, leap ready: leap closes the gap", func() Inputs { in := carnage(); in.Dist = 7; return in }, Leap},
		{"left, beyond reach, leap unaffordable, hover ok: aimed left pursues", func() Inputs {
			in := carnage()
			in.Dist, in.LeapReady = 7, false
			return in
		}, Left},
		{"left, beyond reach, movement leap hungry: no combat leap", func() Inputs {
			in := carnage()
			in.Dist, in.LeapBlocked = 7, true
			return in
		}, Left},
		{"left, beyond reach, no leap, hover dark: approach, never a blind left", func() Inputs {
			in := carnage()
			in.Dist, in.LeapProven, in.HoverOK = 6, false, false
			return in
		}, Approach},
		{"left, in reach, hover dark: shift-left carnage", func() Inputs { in := carnage(); in.Dist, in.HoverOK = 3, false; return in }, Left},
		{"left benched, in reach: double swing fallback", func() Inputs { in := carnage(); in.Dist, in.LeftBenched = 2, true; return in }, Swing},
		{"left benched, far: leap", func() Inputs { in := carnage(); in.Dist, in.LeftBenched = 6, true; return in }, Leap},
		{"left benched, dry, leap-combat gated: basic", func() Inputs {
			in := carnage()
			in.Dist, in.LeftBenched, in.MPPct, in.LeapReady = 2, true, 5, false
			return in
		}, Basic},
		// The pre-Carnage order is untouched without a proven left skill.
		{"legacy: far, leap ready", func() Inputs { in := carnage(); in.LeftProven, in.Dist = false, 6; return in }, Leap},
		{"legacy: close, swing", func() Inputs { in := carnage(); in.LeftProven, in.Dist = false, 2; return in }, Swing},
		{"legacy: owner-declared combat key", func() Inputs {
			return Inputs{CombatProven: true, Dist: 2, MPPct: 50}
		}, Combat},
		{"legacy: nothing proven", func() Inputs { return Inputs{Dist: 2} }, Basic},
	}
	for _, c := range cases {
		if got := Choose(c.in()); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMouse(t *testing.T) {
	for k, want := range map[Kind]string{Left: "left", Basic: "left", Leap: "right", Swing: "right", Combat: "right", Approach: ""} {
		if got := k.Mouse(); got != want {
			t.Errorf("%v.Mouse() = %q, want %q", k, got, want)
		}
	}
}

func TestLeftAudit(t *testing.T) {
	var a LeftAudit
	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < DeafLimit-1; i++ {
		if a.Resolve(false, t0) {
			t.Fatalf("benched early after %d silent strikes", i+1)
		}
	}
	if a.Resolve(true, t0) || a.Deaf != 0 {
		t.Fatal("evidence must clear the silent run")
	}
	for i := 0; i < DeafLimit-1; i++ {
		a.Resolve(false, t0)
	}
	if !a.Resolve(false, t0) {
		t.Fatal("the DeafLimit-th silent strike must bench")
	}
	if !a.Benched(t0.Add(BenchFor - time.Second)) {
		t.Fatal("benched inside the window")
	}
	if a.Benched(t0.Add(BenchFor)) {
		t.Fatal("the bench must lift: the left skill re-arms and re-proves")
	}
}
