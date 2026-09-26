package activity

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
)

// InvTracker is the one inventory state tracker (inventory.Tracker): the
// executive observes it every tick (named states, stuck detection, Rx), and the
// inventory activities consult its quarantine before touching an item.
var InvTracker = inventory.NewTracker()

func dataUnit(u uint32) data.UnitID { return data.UnitID(u) }
