package inventory

import "testing"

func TestWearFromTheOwnersGear(t *testing.T) {
	// R34 live read: belt/boots/gloves/armor at 0, helm 13/35, jewelry has no durability
	w := Assess([]Gear{
		{Slot: "belt", Dur: 0, MaxDur: 12}, {Slot: "feet", Dur: 0, MaxDur: 14}, {Slot: "gloves", Dur: 0, MaxDur: 12},
		{Slot: "torso", Dur: 0, MaxDur: 32}, {Slot: "head", Dur: 13, MaxDur: 35}, {Slot: "left_arm", Dur: 185, MaxDur: 250},
		{Slot: "neck", Dur: 0, MaxDur: 0}, {Slot: "left_ring", Dur: 0, MaxDur: 0},
	})
	if len(w.Broken) != 4 || !w.GoHome() || !w.RepairInTown() || w.Worst.Pct() != 0 {
		t.Fatalf("four broken pieces: go home and repair; got %+v", w)
	}
	fresh := Assess([]Gear{{Dur: 250, MaxDur: 250}, {Dur: 0, MaxDur: 0}})
	if fresh.GoHome() || fresh.RepairInTown() {
		t.Fatal("full durability and jewelry: nothing to do")
	}
	if !Assess([]Gear{{Dur: 80, MaxDur: 100}}).RepairInTown() {
		t.Fatal("80%: obsessive town repair")
	}
}
