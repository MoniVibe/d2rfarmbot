# R2 + R2b captures (laptop, shot.exe built @ 3cec116, 1920x1050 client)

Keyboard panels (items 1-8) were driven by the laptop agent with `ctl key <k> -real`; the rest were opened
by the owner's hand, and each capture was taken with an Insert-key hotkey. The owner's order drifted from the
list, so the files were **renamed by visual content** afterwards. `capkey_raw_order.log` has the raw press order.

| file | content (verified by eye) |
|---|---|
| world_town.png, world_town_2.png | Lut Gholein, nothing open |
| inventory.png, inventory_2.png | I (inventory), nothing on the cursor |
| charsheet.png | C |
| skilltree.png | T |
| inv_and_sheet.png | I + C |
| questlog.png | Q |
| automap_overlay.png | Tab |
| chat.png | Enter (chat box, empty) |
| pause_menu.png | ESC pause menu (bonus; ctl state read QuitMenu=false while it stood) |
| skill_picker.png, skill_picker_2.png | right-skill picker popup ("Press F1-F8 to bind a skill") |
| hireling.png | O: mercenary Ashaya |
| npc_talk.png | Fara menu (Talk / Trade/Repair / Cancel) |
| npc_dialog_text.png | NPC mid-speech text box |
| npc_menu_2.png, npc_menu_3.png, npc_menu_4.png | Fara menu from 3 more positions (R2b item 1) |
| stash.png | stash + inventory |
| waypoint.png | Act 2 waypoint panel (Lut Gholein) |
| world_town_act1.png | Rogue Encampment waypoint, nothing open (R2b item 2) |
| inventory_act1.png, inventory_act1_2.png | Act 1 camp, inventory open (R2b item 3) |
| waypoint_act1_and_inventory.png | Act 1 waypoint panel + inventory together (bonus: two panels) |
| world_field_night.png | outdoor field, nothing open (dark negative; area not recorded) |
| world_cave.png | Pit Level 1 (area 12), nothing open, dark cave negative (taken after the first push) |

`ctl_state.txt`: the percept `MenuOpen(0xF4)` / `QuitMenu` flags read right after each keyboard panel opened.
Both were **false for every panel**, including the standing pause menu.
