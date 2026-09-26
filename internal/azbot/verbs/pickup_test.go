package verbs

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
)

// Run u (step 11): in a pile the probe hovered the neighbours, never the exact
// UnitID. The target still wins when hovered; with a policy, any wanted item
// under the cursor is a valid click; without one, only the target.
func TestHoveredPickTargetThenPolicy(t *testing.T) {
	key := data.Item{UnitID: 11, Quality: item.QualityNormal}
	ring := data.Item{UnitID: 12, Quality: item.QualityUnique}
	target := data.Item{UnitID: 10, Quality: item.QualityUnique}
	wantUniques := func(it data.Item) bool { return it.Quality >= item.QualityUnique }

	// HoverData names the target.
	if id, ok := hoveredPick(data.HoverData{IsHovered: true, UnitID: 10, UnitType: 4},
		[]data.Item{target, key}, 10, wantUniques); !ok || id != 10 {
		t.Fatalf("target via HoverData: %d %v", id, ok)
	}
	// The target's own IsHovered flag (HoverData silent).
	th := target
	th.IsHovered = true
	if id, ok := hoveredPick(data.HoverData{}, []data.Item{key, th}, 10, nil); !ok || id != 10 {
		t.Fatalf("target via IsHovered: %d %v", id, ok)
	}
	// A Key under the cursor: not wanted — whiff, as before.
	if _, ok := hoveredPick(data.HoverData{IsHovered: true, UnitID: 11, UnitType: 4},
		[]data.Item{target, key}, 10, wantUniques); ok {
		t.Fatal("an unwanted neighbour must not be clicked")
	}
	// A wanted unique neighbour under the cursor: take it.
	if id, ok := hoveredPick(data.HoverData{IsHovered: true, UnitID: 12, UnitType: 4},
		[]data.Item{target, key, ring}, 10, wantUniques); !ok || id != 12 {
		t.Fatalf("wanted neighbour: %d %v", id, ok)
	}
	// ...but never without a policy.
	if _, ok := hoveredPick(data.HoverData{IsHovered: true, UnitID: 12, UnitType: 4},
		[]data.Item{target, key, ring}, 10, nil); ok {
		t.Fatal("no policy: exact target only")
	}
	// A MONSTER sharing the neighbour's UnitID is not an item hover.
	if _, ok := hoveredPick(data.HoverData{IsHovered: true, UnitID: 12, UnitType: 1},
		[]data.Item{target, ring}, 10, wantUniques); ok {
		t.Fatal("a unit-type-1 hover is not an item")
	}
}
