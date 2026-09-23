package screen

import "image"

// ---------------------------------------------------------------- Unphotographed panels
//
// NO CAPTURE, NO COORDINATE. Each of these panels needs a real 1920x1050 shot
// before a pixel test exists; a guessed coordinate is a detector that lies with
// confidence. Until then each returns (seen=false, unsure=true): absence is NOT
// proven, and Observe lists the panel as Unsure. The half-panel frames
// (LeftPanelVisible / RightPanelVisible) still catch the framed ones as
// "something is open on this side".

// sightStub is the shared "detector unavailable" answer.
func sightStub(img image.Image) (seen, unsure bool) { return false, true }

// TODO(capture relay/R2/inventory.png): the bag alone, no vendor. The bag's X
// at (1788,18) is photographed only beside a vendor (RightPanelX).
func InventoryVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/charsheet.png)
func CharSheetVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/skilltree.png)
func SkillTreeVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/questlog.png)
func QuestLogVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/stash.png)
func StashVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/waypoint.png): the memory flag LINGERS true after the
// panel closes (usewaypoint.go, 03:08/03:18), so only sight can assert it.
func WaypointVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/chat.png)
func ChatVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/skill_picker.png)
func SkillPickerVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/hireling.png)
func MercenaryVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/npc_dialog_text.png)
func NPCDialogVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/automap_overlay.png)
func AutomapVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// TODO(capture relay/R2/cursor_item.png): memory's cursor read (WARNING 9) is
// the proven channel meanwhile.
func CursorItemVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }

// NPCMenuVisible: testdata/npc_menu.png exists, but the Talk/Trade list FLOATS
// above the NPC (there at 893-1045 x 255-367) — one shot cannot fix a position
// or prove a scan. The 0xF4 byte is the proven channel meanwhile.
// TODO(capture relay/R2/npc_menu_*.png): the list at 2+ other screen positions.
func NPCMenuVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }
