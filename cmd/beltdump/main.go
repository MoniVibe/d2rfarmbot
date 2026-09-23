package main

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/memory"
)

// Read-only: attach to D2R and list belt items (id, name, position) plus the
// player's HP, to extend azbot's potion recognizer (belt-full-of-strangers wound).
func main() {
	p, err := memory.NewProcess()
	if err != nil {
		fmt.Println("attach failed:", err)
		return
	}
	d := memory.NewGameReader(p).GetData()
	fmt.Printf("player=%s hp%%=%d\n", d.PlayerUnit.Name, d.PlayerUnit.HPPercent())
	for _, it := range d.Inventory.ByLocation(item.LocationBelt) {
		fmt.Printf("belt id=%d name=%q x=%d y=%d\n", it.ID, string(it.Name), it.Position.X, it.Position.Y)
	}
}
