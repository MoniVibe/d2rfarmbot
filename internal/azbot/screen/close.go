package screen

import "fmt"

// ActionKind is what CloseStep asks the motor to do.
type ActionKind uint8

const (
	ActNone    ActionKind = iota // screen clear: nothing to do
	ActClick                     // real menu click at (X,Y)
	ActKey                       // real key press VK
	ActUnknown                   // something blocks, but no safe close is known
)

func (k ActionKind) String() string {
	switch k {
	case ActNone:
		return "none"
	case ActClick:
		return "click"
	case ActKey:
		return "key"
	}
	return "unknown"
}

// VKEscape is the Windows virtual-key code for ESC.
const VKEscape = 0x1B

// Action is ONE next step toward a clear screen.
type Action struct {
	Kind   ActionKind
	X, Y   int   // ActClick: capture pixel space
	VK     uint8 // ActKey
	Panel  Panel // what the step targets
	Reason string
}

func (a Action) String() string {
	switch a.Kind {
	case ActClick:
		return fmt.Sprintf("click (%d,%d) closes %s: %s", a.X, a.Y, a.Panel, a.Reason)
	case ActKey:
		return fmt.Sprintf("key 0x%02X closes %s: %s", a.VK, a.Panel, a.Reason)
	}
	return a.Kind.String() + ": " + a.Reason
}

// escCloses: panels one ESC closes. Not the pause menu (ESC toggles it — the
// Return-to-Game click is the known-good path), not the sub-panel (its X is),
// not the automap (Tab toggles it, and it eats nothing).
const escCloses = NPCMenu | NPCDialog | Shop | Inventory | CharSheet | SkillTree |
	QuestLog | Stash | Waypoint | Chat | SkillPicker | Mercenary | LeftPanel | RightPanel

// CloseStep picks ONE next action to clear the screen; call it again on the
// next Reading until it answers ActNone.
//
// THE INVARIANT: no key is ever sent for a panel that was not POSITIVELY
// observed — seen on the capture, or asserted by a memory channel proven for
// exactly that panel (0xF4 → NPC menu). Every blind ESC on 2026-09-23 either
// raised the pause menu or landed in a sub-panel that ate the next run's keys.
// Clicks on a SEEN button come first (the button is its own proof); ESC only
// when an ESC-closable panel is positively observed and no pause menu or
// sub-panel is up. Panel toggle keys (I, C, T, Q, O) are never sent: they are
// deaf to synthetic input on this build (WARNING 5), while ESC is proven to
// land (safeEsc) and closes every side panel at once.
func CloseStep(r Reading) Action {
	if r.Mode == Loading {
		return Action{Kind: ActUnknown, Reason: "loading: no screen to act on"}
	}
	// Seen buttons, topmost first: the sub-panel sits on the pause menu, the
	// vendor X also takes down the bag beside it.
	for _, q := range []Panel{SubPanel, PauseMenu, Shop, Inventory, RightPanel} {
		if r.Sight&q == 0 {
			continue
		}
		if pt, ok := r.Close[q]; ok {
			why := "red X seen"
			if q == PauseMenu {
				why = "Return to Game"
			}
			return Action{Kind: ActClick, X: pt.X, Y: pt.Y, Panel: q, Reason: why + " (" + r.Evidence[q] + ")"}
		}
	}
	if r.Panels&(SubPanel|PauseMenu) != 0 {
		// Up but its button unmeasured — ESC here would toggle the menu.
		return Action{Kind: ActUnknown, Panel: r.Panels & (SubPanel | PauseMenu),
			Reason: "pause/sub-panel up without a known button"}
	}
	proven := r.Sight | (r.Panels & memoryProven)
	if esc := proven & escCloses; esc != 0 {
		if r.Mode != World {
			return Action{Kind: ActUnknown, Panel: esc, Reason: "mode " + r.Mode.String() + ": ESC withheld"}
		}
		return Action{Kind: ActKey, VK: VKEscape, Panel: esc, Reason: "ESC closes " + esc.String() + " (" + r.Why(esc) + ")"}
	}
	if b := r.Panels &^ nonBlocking; b != 0 {
		return Action{Kind: ActUnknown, Panel: b, Reason: b.String() + " asserted but not seen (" + r.Why(b) + "): no key without sight"}
	}
	why := "clear"
	if r.Unsure != 0 {
		why += " (unchecked: " + r.Unsure.String() + ")"
	}
	return Action{Kind: ActNone, Reason: why}
}
