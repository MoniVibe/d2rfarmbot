// invdump: read-only — the inventory as the bot's memory model sees it: equipped
// durability, the bag with mod-aware sizes and dispositions, and the belt.
package main

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
)

func main() {
	p, err := memory.NewProcess()
	if err != nil {
		fmt.Println("attach failed:", err)
		return
	}
	db, _ := gamedata.Load(gamedata.DefaultRoot)
	d := memory.NewGameReader(p).GetData()
	fmt.Printf("player=%s hp%%=%d\n", d.PlayerUnit.Name, d.PlayerUnit.HPPercent())
	var gear []inventory.Gear
	for _, it := range d.Inventory.ByLocation(item.LocationEquipped) {
		g := inventory.Gear{Slot: string(it.Location.BodyLocation), ID: int(it.ID)}
		if v, ok := it.FindStat(stat.Durability, 0); ok {
			g.Dur = v.Value
		}
		if v, ok := it.FindStat(stat.MaxDurability, 0); ok {
			g.MaxDur = v.Value
		}
		gear = append(gear, g)
		c := loot.Classify(int(it.ID))
		fmt.Printf("equipped %-20s row %-4d %-4s dur %3d/%-3d (%3d%%) broken=%v\n", g.Slot, g.ID, c.Code, g.Dur, g.MaxDur, g.Pct(), g.Broken())
	}
	w := inventory.Assess(gear)
	fmt.Printf("wear: worst %s %d%%, broken %d, below %d%%: %d — repair in town=%v, go home=%v\n",
		w.Worst.Slot, w.Worst.Pct(), len(w.Broken), inventory.TownRepairPct, len(w.Low), w.RepairInTown(), w.GoHome())
	for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
		c := loot.Classify(int(it.ID))
		uq := ""
		if it.Quality == item.QualityUnique && db != nil && int(it.UniqueSetID) >= 0 && int(it.UniqueSetID) < len(db.Uniques) {
			u := db.Uniques[it.UniqueSetID]
			uq = fmt.Sprintf(" unique#%d=%s base=%s req=%d match=%v", it.UniqueSetID, u.Name, u.Code, u.LevelReq, u.Code == c.Code)
		}
		fmt.Printf("bag (%d,%d) row %-4d %-4s %-8v %dx%d q=%d ident=%v%s\n", it.Position.X, it.Position.Y, it.ID, c.Code, c.Kind, c.W, c.H, it.Quality, it.Identified, uq)
	}
	for _, it := range d.Inventory.Belt.Items {
		fmt.Printf("belt slot %d row %d %s\n", it.Position.X, it.ID, inventory.PotionOf(int(it.ID)))
	}
}
