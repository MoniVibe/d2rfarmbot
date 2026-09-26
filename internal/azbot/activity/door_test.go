package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/object"
)

// A door with a selection box in the object table is aimed inside that box;
// a row with none falls back to the stash offsets.
func TestDoorAimUsesTheObjectBox(t *testing.T) {
	var withBox object.Name = -1
	for id := 1; id < 600; id++ {
		n := object.Name(id)
		if d := n.Desc(); d.Width > 0 && d.Height > 0 {
			withBox = n
			break
		}
	}
	if withBox < 0 {
		t.Skip("no object row with a selection box in this table")
	}
	d := withBox.Desc()
	offs := doorAimOffsets(withBox)
	want := boxOffsetsXY(d.Left, d.Top, d.Width, d.Height)
	if len(offs) == 0 || offs[0] != want[0] {
		t.Fatalf("aim %v, want the box center %v", offs, want[0])
	}
	if got := doorAimOffsets(object.Name(0)); len(got) != len(stashAim) && object.Name(0).Desc().Width == 0 {
		t.Fatal("no box: the stash offsets")
	}
}
