package main

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Relay R10: only an item the percept judged merchandise while it lay in the
// bag counts as known junk once it rides the cursor.
func TestKnownJunkByUnitID(t *testing.T) {
	bag := []data.Item{
		{UnitID: 11, Position: data.Position{X: 2, Y: 4}},
		{UnitID: 12, Position: data.Position{X: 5, Y: 5}},
	}
	ids := junkIDs([]percept.InvItem{{GX: 2, GY: 4}}, bag)
	if !ids[11] || ids[12] {
		t.Fatalf("junk ids %v", ids)
	}
	if !knownJunk(ids, []data.Item{{UnitID: 11}}) {
		t.Fatal("the junk from cell (2,4) on the cursor is known junk")
	}
	if knownJunk(ids, []data.Item{{UnitID: 12}}) || knownJunk(ids, nil) || knownJunk(nil, []data.Item{{UnitID: 11}}) {
		t.Fatal("a keeper, an empty cursor or an empty set is not known junk")
	}
}

// The held item goes back to the picker when it bids, else to any bidder
// that owns cursor work (Equip in town) — never to the holder itself.
func TestHandBackTo(t *testing.T) {
	owns := func(n string) bool { return n == "fence" || n == "equip" }
	bids := []arbiter.Demand{{Who: "advance"}, {Who: "equip"}, {Who: "fence"}}
	if got := handBackTo("advance", "fence", bids, owns); got != "fence" {
		t.Fatalf("picker bidding: %q", got)
	}
	if got := handBackTo("advance", "loot", bids, owns); got != "equip" {
		t.Fatalf("picker gone: %q", got)
	}
	owns2 := func(n string) bool { return n != "advance" }
	if got := handBackTo("advance", "", []arbiter.Demand{{Who: "restock"}, {Who: "advance"}}, owns2); got != "" {
		t.Fatalf("a service that only waits on a held item is no parker: %q", got)
	}
	if got := handBackTo("equip", "", []arbiter.Demand{{Who: "equip"}, {Who: "advance"}}, owns); got != "" {
		t.Fatalf("nobody else parks: %q", got)
	}
}
