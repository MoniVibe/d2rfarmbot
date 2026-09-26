// Package screen is azbot's screen-state oracle: what is ON SCREEN, read from a
// capture, with memory hints admitted only where they are proven.
//
// WHY A SCREEN ORACLE: memory cannot be trusted for UI on this mod.
// OpenMenus.QuitMenu reads false with the pause menu up; the 0xF4 UI byte
// (percept MenuOpen) reflects NPC menus only and is blind to the inventory and
// the pause menu; vendor stock and OpenMenus.Waypoint LINGER after their panels
// close. A bot that believes memory "opens menus accidentally" and keeps
// clicking into them. Here the capture is the authority and memory may only
// ADD a panel through a channel proven to read true while that panel is up.
//
// PURE: stdlib + image only — no repo imports, so it tests on Linux against
// real captures (internal/game imports Windows-only libraries).
package screen

import "strings"

// Panel is a SET of on-screen panels — several can be up at once (a vendor
// shows the bag beside the stock; a Chronicle sits on the pause menu).
type Panel uint32

const (
	PauseMenu   Panel = 1 << iota // ESC menu (grey stone buttons)
	SubPanel                      // Options / Chronicle / Loot Filter (centered frame, red X)
	NPCMenu                       // Talk / Trade list beside an NPC
	NPCDialog                     // NPC speech text
	Shop                          // vendor trade panel
	Inventory                     // the bag
	CharSheet                     // stats
	SkillTree                     // skill tree
	QuestLog                      // quests
	Stash                         // stash
	Waypoint                      // waypoint list
	Chat                          // chat input line
	SkillPicker                   // skill selection popup
	Mercenary                     // hireling panel
	Automap                       // map overlay — draws over the world, eats nothing
	LeftPanel                     // SOME left-half framed panel, identity unknown
	RightPanel                    // SOME right-half framed panel, identity unknown
)

// All lists every panel bit in display order.
var All = []Panel{PauseMenu, SubPanel, NPCMenu, NPCDialog, Shop, Inventory, CharSheet,
	SkillTree, QuestLog, Stash, Waypoint, Chat, SkillPicker, Mercenary, Automap, LeftPanel, RightPanel}

var panelNames = map[Panel]string{
	PauseMenu: "pause", SubPanel: "subpanel", NPCMenu: "npcmenu", NPCDialog: "npcdialog",
	Shop: "shop", Inventory: "inventory", CharSheet: "charsheet", SkillTree: "skilltree",
	QuestLog: "questlog", Stash: "stash", Waypoint: "waypoint", Chat: "chat",
	SkillPicker: "skillpicker", Mercenary: "mercenary", Automap: "automap",
	LeftPanel: "left-panel", RightPanel: "right-panel",
}

// nonBlocking: panels that leave world clicks AND keys alone. Chat is NOT here —
// an open chat line swallows every skill/potion key, which is as bad as a click.
const nonBlocking = Automap

// Has reports whether every bit of q is in p.
func (p Panel) Has(q Panel) bool { return q != 0 && p&q == q }

// Blocking reports whether anything in p eats world input.
func (p Panel) Blocking() bool { return p&^nonBlocking != 0 }

// String: "inventory+shop" style, "none" for the empty set.
func (p Panel) String() string {
	if p == 0 {
		return "none"
	}
	var parts []string
	for _, q := range All {
		if p&q != 0 {
			parts = append(parts, panelNames[q])
		}
	}
	return strings.Join(parts, "+")
}

// Mode is the top-level state, derived from memory hints (the capture cannot
// tell a load screen from a black room).
type Mode uint8

const (
	Unknown Mode = iota // no valid memory snapshot
	World               // in game, alive
	Loading             // load screen
	Dead                // her body is down
)

func (m Mode) String() string {
	switch m {
	case World:
		return "world"
	case Loading:
		return "loading"
	case Dead:
		return "dead"
	}
	return "unknown"
}
