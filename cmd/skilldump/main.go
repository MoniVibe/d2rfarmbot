// skilldump — a PURE READER of the mod's skill tables: one class's skills
// with id, table key, in-game name, range and tree seat.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

func main() {
	class := flag.String("class", "ass", "charclass code (ama sor nec pal bar dru ass)")
	mon := flag.String("mon", "", "list monster rows whose code or name contains this (case-insensitive) instead")
	flag.Parse()
	db, err := gamedata.Load(gamedata.DefaultRoot)
	if err != nil || db == nil {
		fmt.Println("gamedata:", err)
		os.Exit(1)
	}
	if *mon != "" {
		for id := 0; id < 2000; id++ {
			m := db.Monster(id)
			if m == nil {
				continue
			}
			if strings.Contains(strings.ToLower(m.Code+" "+m.Name), strings.ToLower(*mon)) {
				fmt.Println(m.ID, "code="+m.Code, "name="+m.Name, "killable", m.Killable, "npc", m.NPC, "nevercount", m.NeverCount)
			}
		}
		return
	}
	for _, s := range db.ClassSkills(*class) {
		fmt.Printf("%4d  key=%-22q name=%-24q range=%-5s page=%d row=%d col=%d req=%d passive=%v mana=%d\n",
			s.ID, s.Key, s.Name, s.Range, s.Page, s.Row, s.Column, s.ReqLevel, s.Passive, s.Mana)
	}
}
