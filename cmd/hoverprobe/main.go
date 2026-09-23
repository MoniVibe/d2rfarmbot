// hoverprobe — PURE READ: sample HoverData + nearest monsters for N seconds.
package main

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/memory"
)

func main() {
	p, err := memory.NewProcess()
	if err != nil {
		fmt.Println("attach failed:", err)
		return
	}
	gr := memory.NewGameReader(p)
	for i := 0; i < 40; i++ {
		d := gr.GetData()
		hd := d.HoverData
		fmt.Printf("hover=%v unit=%d type=%d monsters=%d\n", hd.IsHovered, hd.UnitID, hd.UnitType, len(d.Monsters.Enemies()))
		time.Sleep(250 * time.Millisecond)
	}
}
