package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/difficulty"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	d2gomem "github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/gear"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// farmbot: minimal, MOD-AGNOSTIC bot. Scavenges koolo's low-level layer (memory reader,
// injector, HID) but makes ZERO vanilla assumptions — reads real monster positions from
// memory, moves/attacks via game-native click-to-move, and casts by the char's ACTUAL
// hotkeys (passed as flags), not hardcoded skill IDs. Build: Rabies werewolf.

func findD2RPID() uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if windows.Process32First(snap, &e) != nil {
		return 0
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "d2r.exe") {
			return e.ProcessID
		}
		if windows.Process32Next(snap, &e) != nil {
			break
		}
	}
	return 0
}

func findHWND(pid uint32) win.HWND {
	var hwnd win.HWND
	cb := syscall.NewCallback(func(h win.HWND, _ uintptr) uintptr {
		var p uint32
		win.GetWindowThreadProcessId(h, &p)
		if p == pid && win.IsWindowVisible(h) {
			hwnd = h
			return 0
		}
		return 1
	})
	windows.EnumWindows(cb, nil)
	return hwnd
}

// nextHopArea BFSes the map-data area graph (d.Areas, from FetchMapData) for the first hop on
// the route from -> to. Returns 0 when unknown (no map data / no route) — the caller then falls
// back to treating `to` as directly adjacent.
// nakedCorpseHunt picks the first adjacent non-town area (from map data) to search for a corpse
// after a death this process didn't witness. Returns 0 when map data is absent.
func nakedCorpseHunt(d game.Data) area.ID {
	ad, ok := d.Areas[d.PlayerUnit.Area]
	if !ok {
		return 0
	}
	for _, al := range ad.AdjacentLevels {
		if !al.Area.IsTown() {
			return al.Area
		}
	}
	return 0
}

func nextHopArea(d game.Data, from, to area.ID) area.ID {
	if from == to || len(d.Areas) == 0 {
		return 0
	}
	prev := map[area.ID]area.ID{from: from}
	queue := []area.ID{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ad, ok := d.Areas[cur]
		if !ok {
			continue
		}
		for _, al := range ad.AdjacentLevels {
			if _, seen := prev[al.Area]; seen {
				continue
			}
			prev[al.Area] = cur
			if al.Area == to {
				hop := to
				for prev[hop] != from {
					hop = prev[hop]
				}
				return hop
			}
			queue = append(queue, al.Area)
		}
	}
	return 0
}

func gameToScreen(gr *game.MemoryReader, px, py, dx, dy int) (int, int) {
	diffX := dx - px
	diffY := dy - py
	sx := int(float32(diffX-diffY)*19.8) + gr.GameAreaSizeX/2
	sy := int(float32(diffX+diffY)*9.9) + gr.GameAreaSizeY/2
	return sx, sy
}

// survivalStatus is the verdict of a per-tick survival check: quaff potions, chicken (flee),
// or the character is already dead.
type survivalStatus int

const (
	StatusNormal survivalStatus = iota
	StatusChicken
	StatusDead
	// StatusGameGone: D2R exited, or we are no longer in a game. NOT a character death — a dead
	// process reads back as all-zeros through the memory reader, which is indistinguishable from
	// HP=0 unless you ask the OS. The loop used to call that a death, so every crash was logged
	// as "dead — stopping" and every postmortem started from a false premise.
	StatusGameGone
)

func chebyshev(a, b data.Position) int {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

func main() {
	seconds := flag.Int("seconds", 90, "run duration in seconds")
	werewolf := flag.String("werewolf", "f1", "shapeshift hotkey")
	rabies := flag.String("rabies", "f2", "rabies attack hotkey")
	attackRange := flag.Int("attackrange", 4, "engage distance for the hover-confirmed left-click attack (4 = melee swing; ~25 with a bow — shots fire at the hovered target from range)")
	golemKey := flag.String("golem", "", "golem hotkey (e.g. f2): cast at own position every -golemevery seconds — golems need no corpse and recasting refreshes a wounded one")
	golemEvery := flag.Int("golemevery", 120, "seconds between golem refresh casts")
	melee := flag.String("melee", "", "normal-attack hotkey: pressed instead of -rabies when MP%% is below -meleebelow (pre-skills fallback for a low-level char — bind Attack to a key in-game). Empty = left-click normal attack (no binding needed).")
	meleeBelow := flag.Int("meleebelow", 25, "MP%% threshold below which the bot melees instead of casting")
	castRange := flag.Int("castrange", 8, "max distance (subtiles) to cast the right skill — Firestorm's flames crawl and dissipate, so casts from bite range (18) burn mana into empty ground")
	autoSkill := flag.String("autoskill", "1378,201,1651,279", "'tabX,tabY,skillX,skillY': when a skill point is banked and no enemy is near, open the skill tree, click the tab then the skill, verify the point was spent. All overnight points go to one skill. Default: Summoning tab, Raise Skeleton (calibrated from a live tree shot 2026-07-18 — top-right cell; every point widens the army, and the army cap follows the skill level live).")
	autoStat := flag.String("autostat", "", "'strX,strY,dexX,dexY,vitX,vitY': auto-spend banked stat points when calm — str/dex to ~10 buffer over gear reqs, rest vitality (char-panel + button coords from -statsnap)")
	autoProgress := flag.Bool("autoprogress", false, "farm along the act-1 route (Den of Evil -> Cold Plains -> Burial Grounds -> Stony Field -> Dark Wood -> Black Marsh), advancing when an area runs dry; position persisted across runs")
	summonKey := flag.String("summon", "", "summon hotkey (e.g. f2 = RaiseSkeleton): when pets are below -maxpets and a monster corpse is near, select the summon and cast it at the corpse; the next bite's -rabies press restores the attack skill")
	maxPets := flag.Int("maxpets", 3, "stop summoning at this many living pets")
	spirit := flag.String("spirit", "f3", "spirit/aura buff hotkey")
	wolves := flag.String("wolves", "f4", "summon wolves hotkey")
	creeper := flag.String("creeper", "f5", "summon creeper hotkey")
	moveKey := flag.String("move", "", "Force Move hotkey (bind 'Force Move' in D2R Options>Controls to this key; walking needs it)")
	runwalkKeyStr := flag.String("runwalk", "", "Toggle Run/Walk hotkey (bind 'Toggle Run/Walk' in D2R Options>Controls). Set so the bot WALKS near enemies (running = 0 defense) and RUNS when traveling.")
	dangerRange := flag.Int("dangerrange", 30, "walk (not run) when any enemy is within this many units, to keep defense up")
	radius := flag.Int("radius", 45, "only engage monsters within this many units")
	mapcheck := flag.Bool("mapcheck", false, "probe: fetch collision-map data for the current area, print it, and exit")
	mapalign := flag.Bool("mapalign", false, "probe: walk to sample walkable tiles, brute-force the grid offset that fits them, exit")
	nav := flag.Bool("nav", false, "enable map navigation: fetch map, auto-calibrate offset, A* pathfind to targets")
	levelprobe := flag.Bool("levelprobe", false, "probe: read live DrlgLevel origin/size from memory, compare to generated grid, exit")
	charProbe := flag.Bool("charprobe", false, "probe: read the character sheet from memory (name/class/level/skills/L+R skill), exit")
	objProbe := flag.Int("objprobe", 0, "probe: READ-ONLY dump of objects/entrances within this radius (subtiles) of the player — name/id/selectable/interactType/mode/portal. The interactables inventory for this area.")
	interactWith := flag.String("interact", "", "PROBE: walk to and operate the nearest matching interactable, then report every observable state change. Kinds: chest|shrine|wp|portal|entrance|<numeric object id>. Needs -move e.")
	dieTest := flag.Bool("dietest", false, "DEATH-CYCLE PROBE — SOFTCORE ONLY: leave town, aggro, die on purpose, learn the respawn input, confirm town respawn, corpse-run, recover the body. Needs -move e.")
	uiSnap := flag.String("uisnap", "", "PROBE: press this key (e.g. 't' for the skill tree), screenshot the panel to logs/uisnap.png, press esc to close, exit. For mapping panel coordinates.")
	panelTest := flag.String("paneltest", "", "PROBE: open the panel via -panelkey, uiClick at 'x,y' (screenshot pixel coords), screenshot the result, close, exit. Calibrates the panel click space.")
	panelKey := flag.String("panelkey", "t", "key -paneltest presses to open the panel first; 'none' clicks with no panel opened (e.g. the skill-select slot on the HUD)")
	bindSkill := flag.String("bindskill", "", "PROBE: 'slotX,slotY,skillX,skillY,key' — click the HUD skill slot to open the selector, HOVER the skill icon, press the hotkey to bind it, screenshot, exit.")
	pressOnly := flag.String("press", "", "PROBE: press this key once and exit (e.g. esc to close a leaked pause menu)")
	gearProbe := flag.Bool("gearprobe", false, "PROBE: READ-ONLY gear evaluation — dump every equipped/inventory item with its full stat list, flag unidentified items, and note skill-granting gear that hotkey bindings depend on")
	gearOracle := flag.Bool("gearoracle", false, "PROBE: READ-ONLY gear recommendations — parse the mod's recipe/item tables, score equipped+inventory for the summoner build, print ranked one-step upgrades (equip / socket / cube)")
	statSnap := flag.Bool("statsnap", false, "PROBE: open the character panel (c), screenshot to logs/statsnap.png, report banked StatPoints + current str/dex/vita/ene and gear str/dex requirements. Calibration for the stat allocator.")
	statAlloc := flag.String("statalloc", "", "'strBtnX,strBtnY,dexBtnX,dexBtnY,vitBtnX,vitBtnY': spend banked stat points — str/dex to a small buffer over gear requirements, rest to vitality. Verified via BaseStats. Needs the char panel coords from -statsnap.")
	moveLab := flag.String("movelab", "", "MEASUREMENT: drive a fixed square course under locomotion modes ('carrot', 'lookahead', 'click', or 'all'), scoring time / stall-seconds / path efficiency per leg. The winner becomes the mover's engine. Needs -move e.")
	gotoArea := flag.Int("goto", 0, "with -nav: travel to this area ID first (e.g. 2=Blood Moor) via its exit, then farm")
	roomprobe := flag.Int("roomprobe", 0, "probe: dump live Room2 graph + borders to this destination area ID, exit")
	collprobe := flag.Bool("collprobe", false, "probe: live-room collision availability + compare live vs koolo-map grid at player, exit")
	wpprobe := flag.Bool("wpprobe", false, "probe: dump nearby objects + OpenMenus + waypoint hover state for ~20s, no clicks, exit")
	hoverProbe := flag.Bool("hoverprobe", false, "probe: sweep the cursor over nearby on-screen units and read back D2R's hover state — proves whether D2R sees the (driver-level) cursor; no clicks, no walking, exit")
	foreground := flag.Bool("foreground", false, "force the D2R window to the foreground before running (D2R only processes RawInput while focused)")
	cursorScan := flag.Bool("cursorscan", false, "probe: differential-scan D2R memory for its INTERNAL cursor-position variable (briefly commandeers the real cursor for calibration), exit")
	cursorDrive := flag.Bool("cursordrive", false, "with -cursorscan: after locating the cursor variable, run the WRITE-DRIVE (aquarium) test — steer the character by memory write with the real mouse parked")
	cursorAddr := flag.String("cursoraddr", "", "skip the scan: drive the WRITE test against this hex cursor-variable address (valid only within the same D2R process), e.g. 0xD3DD9FE4C4")
	cursorNoFG := flag.Bool("cursornofg", false, "with the WRITE test: do NOT foreground D2R (tests whether background RawInput still overrides the written value)")
	pathProbe := flag.Bool("pathprobe", false, "probe: dump the player's DynamicPath struct and flag position/target words, exit (read-only, non-intrusive)")
	pathDrive := flag.Bool("pathdrive", false, "test: write candidate path-target fields to try to walk the character with pure memory writes (no cursor/focus/key), exit")
	pathNoKey := flag.Bool("pathnokey", false, "with -pathdrive: do NOT hold Force-Move (tests whether the target write ALONE drives movement)")
	shadowWalk := flag.String("shadowwalk", "", "NON-INTRUSIVE aquarium test: hold Force-Move via memory patch + spam-write the cursor shadow at this hex addr; no cursor/focus/driver touched. Needs -move e and open ground.")
	findWriter := flag.String("findwriter", "", "RE: become D2R's debugger and capture the instruction that writes the cursor-mirror at this hex addr (the hardware-input write site), exit. Elevated.")
	inputTest := flag.Bool("inputtest", false, "identify which cursor-read export (GetPhysicalCursorPos/GetCursorInfo/GetCursorPos) D2R uses in-game by patching each and testing Force-Move; the winner is the hands-off movement primitive. Needs -move e. Non-intrusive.")
	liveGrid := flag.Bool("livegrid", false, "build the nav grid from LIVE room collision (mod-accurate) instead of koolo-map; use for mod-altered areas where koolo-map's grid is wrong")
	diff := flag.String("diff", "normal", "current game difficulty for map generation (normal/nightmare/hell)")
	belt := flag.String("belt", "1,2,3,4", "comma-separated hotkeys for the 4 belt columns, left to right")
	hppct := flag.Int("hppct", 60, "quaff a healing/rejuv potion at or below this HP percent")
	mppct := flag.Int("mppct", 30, "quaff a mana/rejuv potion at or below this MP percent")
	chicken := flag.Int("chicken", 20, "abandon the fight and flee (chicken) at or below this HP percent")
	potcd := flag.Int("potcd", 1500, "minimum ms between potion quaffs of the same type")
	tp := flag.String("tp", "", "Tome of Town Portal hotkey; empty disables emergency TP on chicken")
	idKey := flag.String("idkey", "f4", "Book of Identify hotkey (bind the ID tome's skill in-game like F1-F3). Identify = select the skill, WORLD right-click raises the ID cursor, LEFT-click each item — panel right-clicks are deaf on this build, world right-clicks are not.")
	cubeKey := flag.String("cubekey", "h", "Horadric Cube UI keybinding (Options>Controls — D2R's direct open-cube key). Sidesteps the deaf panel right-click that opening the cube would otherwise need. Empty disables cube stashing.")
	clickMove := flag.Bool("clickmove", true, "far travel via the game's OWN pathfinder (ground clicks at the carrot) instead of continuous force-move steering — the fish cure. Force-move stays for close-in work and fallbacks.")
	tripItems := flag.Int("tripitems", 26, "auto town trip (TP + identify + sell + restock + return) when the inventory holds this many items; 0 = only on demand via `echo tp`")
	loot := flag.Bool("loot", false, "enable ground-item looting between fights")
	lootradius := flag.Int("lootradius", 30, "only pick up ground items within this many units")
	lootAll := flag.Bool("lootall", false, "loot every ground item (old behavior). Default is the filter: unique/set/rare/crafted + gold + potions the belt is short on")
	hardcore := flag.Bool("hc", false, "hardcore mode: STOP on death. Default (softcore): respawn via esc, travel back, recover the corpse, resume farming")
	objects := flag.Bool("objects", false, "open chests / smash barrels / use nearby selectable objects between fights")
	objradius := flag.Int("objradius", 40, "only interact with objects within this many units")
	hpcol := flag.Int("hpcol", -1, "fixed belt column (0-3) to press for healing instead of auto-detecting; -1=auto")
	mpcol := flag.Int("mpcol", -1, "fixed belt column (0-3) to press for mana instead of auto-detecting; -1=auto")
	walkToPt := flag.String("walkto", "", `prove locomotion: force-move to WORLD subtile "x,y" in the current area, report ARRIVED/WEDGED, exit`)
	moveTest := flag.Bool("movetest", false, "calibrate the actuator: pulse force-move toward 8 screen directions, log the resulting WORLD delta, exit")
	realCursor := flag.Bool("realcursor", false, "drive the REAL system cursor (SetCursorPos) to move targets — for builds where D2R reads the hardware mouse via RawInput, not the injected GetCursorPos")
	hwMove := flag.String("hwmove", "off", "Interception driver mouse-move mode: off (DEFAULT — non-intrusive, no real-cursor movement) | rel (closed-loop real-cursor move) | abs (absolute real-cursor move). rel/abs are intrusive diagnostics.")
	dpiScale := flag.Float64("dpiscale", 1.5, "logical→physical pixel scale for the in-game GetPhysicalCursorPos patch (1.5 at 150% display scaling)")
	worldScale := flag.Float64("worldscale", 0, "logical→WORLD cursor-space scale for world aim (hover/attack/move direction). 0 = follow -dpiscale (measured correct). Pass 1 for the old unscaled behavior.")
	hwProbe := flag.Bool("hwprobe", false, "validate the Interception mouse-move path: drive the REAL cursor to known screen points, read back GetCursorPos, report match/mismatch, exit (no D2R attach)")
	fixInput := flag.Bool("fixinput", false, "REPAIR: resume any leaked thread suspends + heal D2R's input functions (restore GetPhysicalCursorPos/GetCursorPos/etc. to pristine) after an unclean exit — no D2R restart needed, exit")
	resumeThreads := flag.Bool("resumethreads", false, "REPAIR: resume any D2R threads left suspended by a crashed run (unblocks SendMessage/HoldKey hangs), exit")
	hookScan := flag.Bool("hookscan", false, "DIAGNOSTIC (READ-ONLY, patches nothing): dump the live bytes of D2R's input functions vs our pristine user32, and name any module already hooking them (e.g. amdihk64 = AMD overlay). Two modules patching one function is a classic random crash. Exits.")
	clickTest := flag.Bool("clicktest", false, "diagnose in-game LEFT-click: aim interactClick at the nearest monster and report whether she attacks (player Mode change / monster damage) — tells us button-vs-hitbox. Needs -move e.")
	wpAt := flag.Bool("wpat", false, "waypoint probe: stand the character ON/next to a waypoint, then run this — it dumps nearby selectable objects, clicks the nearest, and reports panel-open signals (AvailableWaypoints/OpenMenus/area). Cracks the mod's WP object id + panel detection. Needs -move e.")
	screenshot := flag.String("screenshot", "", "capture the D2R window to this PNG path and exit (for mapping UI panels)")
	wpTown := flag.Bool("wptown", false, "waypoint-travel to Act2 town (Lut Gholein): click the nearby WP, open the panel, select the town, confirm arrival. the character must be near a waypoint. Needs -move e.")
	wpRow := flag.Int("wprow", 178, "y-pixel of the target waypoint destination row (853x480 client space) for -wptown calibration")
	wpCol := flag.Int("wpcol", 250, "x-pixel of the destination click (853x480 client space) for -wptown calibration")
	wpTab := flag.Bool("wptab", false, "with -wptown: click the Act II tab first (usually unnecessary — panel opens on the current act)")
	panelFn := flag.String("panelfn", "phys", "which cursor export uiClick patches for panel clicks: phys (GetPhysicalCursorPos — correct, panels read it) | info (GetCursorInfo — the only OTHER distinct export). NOTE: 'pos'/'all' were deleted — GetCursorPos IS GetPhysicalCursorPos on this build (one address), so they switched between identical functions while only varying the coord scale.")
	wpAim := flag.Bool("wpaim", false, "AIM ORACLE (zero clicks, zero travel): open the WP panel, then sweep the patched cursor down the destination rows and screenshot each — reveals which row highlights at each aim-Y (the export+affine map) without selecting anything. Honors -panelfn. Needs -move e, the character on a CLEARED waypoint.")
	wpGoto := flag.Int("wpgoto", 0, "travel to this AREA ID via the waypoint panel (uses the calibrated clickY=96+41*row map + area->row table). the character must be near a waypoint. Needs -move e.")
	idProbe := flag.Bool("identify", false, "PROBE: run the identify errand (ID tome on unidentified inventory items) and exit.")
	npcClick := flag.String("npcclick", "", "PROBE: 'nameID,dx,dy' — walk to the town NPC and BLIND interactClick at body screen pos + offset, screenshot npcclick.png (hover is dead for NPCs on this build).")
	npcProbe := flag.Int("npcprobe", 0, "TOWN DISCOVERY: lists nearby units, then walks to + interacts with the NPC whose NAME ID == this value (via interactNPC body-hover), screenshots + reports OpenMenus. Run once to read the unit list, then re-run with a town-NPC name id. Needs -move e.")
	hoverGrid := flag.Bool("hovergrid", false, "DIAGNOSTIC: sweep the cursor across a screen grid, logging every point where anything becomes hovered (HoverData OR any monster.IsHovered). Reveals whether/where NPC hover fires. Needs -move e.")
	aimProbe := flag.Bool("aimprobe", false, "AIM SCALE ORACLE (read+aim only, zero clicks): pick the nearest monster, aim at t*gameToScreen prediction for t=0.5..1.7, log where the game reports hover. The t where hover fires IS the world-aim scale factor (1.0 = mapping correct as-is).")
	shotDir := flag.String("shotdir", "shots", "directory for diagnostic screenshots (created if missing; relative to the working dir).")
	wpCal := flag.Bool("wpcal", false, "WP CALIBRATION ORACLE (opens the panel, clicks NOTHING, travels nowhere): reports which row the blue compass lights for the area she is standing in. That (area -> litRow) pair is ground truth; run it once per waypoint area to build the row table honestly. Needs -move e, the character near a CLEARED waypoint.")
	uiClickAt := flag.String("uiclick", "", "DIAGNOSTIC: uiClick these CLIENT coords \"x,y\" on whatever UI is already open, screenshotting before/after. Tests the panel-click primitive in isolation against a known target (e.g. a panel's X button) instead of inferring it from a travel that didn't happen. Needs -move e.")
	aimSweep := flag.Bool("aimsweep", false, "AIM ORACLE (compose with -wpcal; clicks NOTHING): sweep the patched cursor down the open WP panel and report which row the GAME highlights at each aim-Y. Measures the ABSOLUTE cursor mapping — the one thing force-move and hoverPickClick's self-correcting sweep can never test. Needs -move e.")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Diagnostic screenshots used to go to a hardcoded per-session scratch path, which silently
	// rotted every time the session changed (two dead GUIDs were still in here). Keep it relative
	// and self-creating so a shot always lands somewhere real.
	if err := os.MkdirAll(*shotDir, 0o755); err != nil {
		logger.Warn("could not create shot dir", "dir", *shotDir, "err", err)
	}
	shotPath := func(name string) string { return filepath.Join(*shotDir, name) }

	// -hwprobe validates driver stroke -> OS cursor with no game attach at all.
	if *hwProbe {
		game.SetHWMoveMode(*hwMove)
		for _, p := range [][2]int{{100, 100}, {960, 540}, {1800, 300}, {400, 800}, {100, 100}} {
			gx, gy, sent := game.HWMoveProbe(p[0], p[1])
			logger.Info("hwprobe", "target", fmt.Sprintf("(%d,%d)", p[0], p[1]),
				"cursor", fmt.Sprintf("(%d,%d)", gx, gy), "sent", sent,
				"match", sent && int(gx) == p[0] && int(gy) == p[1])
		}
		return
	}
	if err := config.Load(); err != nil {
		logger.Error("config load failed", "err", err)
		return
	}
	cfg := config.Characters["Main"]
	game.SetHWMoveMode(*hwMove)
	game.SetPhysicalScale(*dpiScale)
	// World aim space (measured by -aimprobe): defaults to the display scale; pass
	// -worldscale 1 to restore the old unscaled behavior if this machine measures differently.
	if *worldScale > 0 {
		game.SetWorldScale(*worldScale)
	} else {
		game.SetWorldScale(*dpiScale)
	}

	// Movement REQUIRES the Force Move key. Without -move, walkToHold falls back to a left-click
	// "move", which on this build is unreliable (D2R ignores injected left-clicks) AND spams
	// left-clicks that desync D2R's in-process mouse state (the flaky-LMB symptom). Refuse to run
	// the farming/goto loop without it. Read-only probes don't move, so they're exempt.
	probeOnly := *mapcheck || *mapalign || *levelprobe || *charProbe || *aimProbe || *objProbe != 0 || *interactWith != "" || *dieTest || *uiSnap != "" || *panelTest != "" || *bindSkill != "" || *pressOnly != "" || *gearProbe || *gearOracle || *statSnap || *statAlloc != "" || *moveLab != "" || *collprobe || *roomprobe != 0 || *wpprobe || *hoverProbe || *cursorScan || *cursorAddr != "" || *pathProbe || *pathDrive || *shadowWalk != "" || *findWriter != "" || *inputTest || *clickTest || *wpAt || *fixInput || *resumeThreads || *screenshot != "" || *wpTown || *wpAim || *wpGoto != 0 || *npcProbe != 0 || *hoverGrid || *npcClick != "" || *idProbe
	if !probeOnly && *moveKey == "" {
		logger.Error("-move is required: bind 'Force Move' in D2R Options>Controls and pass e.g. -move e. " +
			"Refusing to run without it — the left-click move fallback is unreliable on this build and corrupts D2R's mouse state.")
		return
	}

	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("D2R.exe not running — load a game first")
		return
	}

	// -fixinput: heal a D2R left with input patches applied (mouse broken). Do this BEFORE any
	// injector Load so we don't re-patch — just restore the functions to pristine and exit.
	if *fixInput {
		gi, err := game.InjectorInit(logger, pid)
		if err != nil {
			logger.Error("fixinput: injector init failed", "err", err)
			return
		}
		// Recover any LEAKED thread suspend first (a past deadlock could leave D2R's message pump
		// frozen so SendMessage/HoldKey hang), THEN heal the patched input functions.
		resumed := gi.ResumeAllThreads()
		n, err := gi.HealInput()
		if err != nil {
			logger.Error("fixinput failed", "err", err)
			return
		}
		logger.Info("fixinput: done — your mouse should work in D2R now", "functionsHealed", n, "threadsResumed", resumed)
		return
	}
	// -hookscan: READ-ONLY. Attaches (no Load, no stubs, no suspends) and reports who — if anyone —
	// is already patching the input functions we also patch. Run it BEFORE farmbot has touched
	// anything, so a "differs" verdict means a THIRD party got there first.
	if *hookScan {
		gi, err := game.InjectorInit(logger, pid)
		if err != nil {
			logger.Error("hookscan: injector init failed", "err", err)
			return
		}
		reports, err := gi.ScanInputHooks()
		if err != nil {
			logger.Error("hookscan failed", "err", err)
			return
		}
		hooked := 0
		for _, r := range reports {
			hx := func(b []byte) string {
				parts := make([]string, len(b))
				for i, c := range b {
					parts[i] = fmt.Sprintf("%02x", c)
				}
				return strings.Join(parts, " ")
			}
			if !r.Differs {
				logger.Info("hookscan: CLEAN", "fn", r.Name, "bytes", hx(r.Target[:8]))
				continue
			}
			hooked++
			logger.Warn("hookscan: DIFFERS — someone patched this", "fn", r.Name,
				"d2r", hx(r.Target), "pristine", hx(r.Pristine),
				"hookKind", r.HookKind, "hookDest", fmt.Sprintf("0x%x", r.HookDest),
				"destOwner", r.DestOwner)
		}
		logger.Info("hookscan: DONE", "functionsScanned", len(reports), "patched", hooked,
			"verdict", map[bool]string{true: "a THIRD PARTY is on our functions", false: "our functions are pristine — no contention"}[hooked > 0])

		// LAYOUT AUDIT: these exports are tiny thunks packed into 16-byte slots. We write a 16-byte
		// stub, a 21-byte restore, and a 24-byte heal. If a function's slot is smaller than what we
		// write, we are scribbling over the NEXT function — a real corruption bug that would present
		// exactly as a random crash. Measure the true gap to the next export instead of assuming.
		byAddr := append([]game.InputHookReport(nil), reports...)
		sort.Slice(byAddr, func(a, b int) bool { return byAddr[a].Addr < byAddr[b].Addr })
		logger.Info("hookscan: LAYOUT AUDIT — bytes we write vs bytes actually available")
		for idx, r := range byAddr {
			gap := -1
			if idx+1 < len(byAddr) {
				gap = int(byAddr[idx+1].Addr - r.Addr)
			}
			writes := "stub=16 restore=21 heal=24"
			verdict := "gap unknown (last fn scanned; neighbour is some other export)"
			if gap > 0 {
				switch {
				case gap < 16:
					verdict = "OVERFLOW: even the 16-byte stub runs past this fn"
				case gap < 21:
					verdict = "OVERFLOW: the 21-byte restore runs past this fn"
				case gap < 24:
					verdict = "OVERFLOW: the 24-byte heal runs past this fn"
				default:
					verdict = "ok — slot is >= every write we make"
				}
			}
			logger.Info("hookscan: layout", "fn", r.Name, "addr", fmt.Sprintf("0x%x", r.Addr),
				"gapToNextScannedFn", gap, "weWrite", writes, "verdict", verdict)
		}
		return
	}
	if *resumeThreads {
		gi, err := game.InjectorInit(logger, pid)
		if err != nil {
			logger.Error("resumethreads: injector init failed", "err", err)
			return
		}
		resumed := gi.ResumeAllThreads()
		logger.Info("resumethreads: done — resumed leaked thread suspends", "count", resumed)
		return
	}
	hwnd := findHWND(pid)
	logger.Info("attaching", "pid", pid)

	gr, err := game.NewGameReader(cfg, "Main", pid, hwnd, logger)
	if err != nil {
		logger.Error("game reader failed", "err", err)
		return
	}
	gi, err := game.InjectorInit(logger, pid)
	if err != nil {
		logger.Error("injector init failed", "err", err)
		return
	}
	if err := gi.Load(); err != nil {
		logger.Error("injector load failed", "err", err)
		return
	}
	defer gi.Unload() // fully restores D2R memory on exit
	// Restore on Ctrl+C / termination too, so an interrupted run never leaves D2R's mouse/keyboard
	// functions patched (that corrupts input for the human until the game is restarted).
	// NOTE: a hard kill (SIGKILL / Stop-Process -Force) cannot be caught — never force-kill farmbot.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("signal received — restoring D2R memory")
		gi.Unload()
		os.Exit(0)
	}()
	hid := game.NewHID(gr, gi)

	if *foreground {
		logger.Info("forcing D2R foreground (RawInput needs focus)")
		game.ForceForegroundHWND(gr.HWND)
		time.Sleep(300 * time.Millisecond)
	}

	// Belt hotkeys, one per column — sourced from -belt (NEVER d.KeyBindings; its offsets are
	// unreliable on this repack).
	var beltKeys [4]byte
	beltToks := strings.Split(*belt, ",")
	for i := 0; i < 4 && i < len(beltToks); i++ {
		beltKeys[i] = hid.GetASCIICode(strings.TrimSpace(beltToks[i]))
	}

	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2

	// walkTo issues a Force Move to an on-screen point. D2R polls the move key as HELD each
	// frame via GetKeyState, so we override its key-state for the whole step AND send the key
	// message events, then restore. (Without moveKey we fall back to a left-click move.)
	var mk byte
	if *moveKey != "" {
		mk = hid.GetASCIICode(*moveKey)
	}
	// (moveStop machinery lives just below; declared before castSelf so every input wrapper
	// can release the held move key first.)
	var moveMu sync.Mutex
	moveHeld := false
	var moveTimer *time.Timer
	moveStop := func() {
		moveMu.Lock()
		defer moveMu.Unlock()
		if !moveHeld {
			return
		}
		if moveTimer != nil {
			moveTimer.Stop()
			moveTimer = nil
		}
		hid.RawKeyUp(mk)
		_ = gi.RestoreGetKeyState()
		_ = gi.RestoreGetAsyncKeyState()
		moveHeld = false
	}

	// cast a self/summon skill: select it, then right-click near the player.
	// Empty key = the char doesn't have this skill (e.g. a level-1 char) — skip entirely,
	// otherwise the right-click below fires the CURRENT right skill at nothing every call site.
	castSelf := func(key string) {
		if key == "" {
			return
		}
		moveStop() // held force-move would turn the cast right-click into a walk
		hid.PressKey(hid.GetASCIICode(key))
		time.Sleep(120 * time.Millisecond)
		hid.Click(game.RightButton, cx, cy)
		time.Sleep(300 * time.Millisecond)
	}
	// COMBAT SELF-AWARENESS: derive HOW to fight from what the character actually HAS,
	// re-read live every tick — equipped weapon (the user hot-swaps gear mid-run), known
	// skills, and the game's own skill->key bindings — instead of flags frozen at launch.
	// The lesson: the bow left with the user, -attackrange 25 stayed, and the bot kept
	// sniping from 25 tiles with a stick.
	type combatProf struct {
		ranged   bool
		castKB   data.KeyBinding // bound damage spell, best-first
		hasCast  bool
		castID   skill.ID
		curseKB  data.KeyBinding // bound curse
		hasCurse bool
		curseID  skill.ID
	}
	deriveCombat := func(d game.Data) combatProf {
		var p combatProf
		for _, it := range d.Inventory.ByLocation(item.LocationEquipped) {
			// ACTIVE hands only: the swap set (secondary arm slots) still carries the old
			// bow+quiver after a weapon switch and must not vote. First deploy read ALL
			// equipped slots and kept sniping at 21 tiles with the wand in hand.
			if it.Location.BodyLocation != item.LocLeftArm && it.Location.BodyLocation != item.LocRightArm {
				continue
			}
			n := string(it.Name)
			// Name-substring on purpose: the mod remaps SOME item names (tomes read as
			// "Jawbone"/"Eye"), so an unknown name defaults to NOT-ranged — worst case he
			// walks up close, which beats shooting a phantom bow.
			if strings.Contains(n, "Bow") || strings.Contains(n, "Crossbow") ||
				strings.Contains(n, "Arrow") || strings.Contains(n, "Bolt") {
				p.ranged = true
			}
		}
		// Trust a binding only when the skill it names is one the char actually KNOWS
		// (Skills map) — the 3.2 keybinding block is otherwise unverified memory (the
		// frozen Life stat taught humility about this build's offsets).
		bound := func(sk skill.ID) (data.KeyBinding, bool) {
			if pts, known := d.PlayerUnit.Skills[sk]; !known || pts.Level == 0 {
				return data.KeyBinding{}, false
			}
			return d.KeyBindings.KeyBindingForSkill(sk)
		}
		for _, sk := range []skill.ID{skill.BoneSpear, skill.Teeth} {
			if kb, ok := bound(sk); ok {
				p.castKB, p.hasCast, p.castID = kb, true, sk
				break
			}
		}
		if kb, ok := bound(skill.AmplifyDamage); ok {
			p.curseKB, p.hasCurse, p.curseID = kb, true, skill.AmplifyDamage
		}
		return p
	}
	// CONTINUOUS LOCOMOTION: the force-move key is a STATE, not a pulse. The old walkToHold
	// blocked the loop for the whole hold then RELEASED the key — so the character stood
	// still during every read/plan/log between steps (~40% duty cycle, 0% during rebuilds:
	// the user-reported walk/pause/walk rhythm). Now KEYDOWN persists across ticks; each
	// walk call just re-aims (force-move follows the cursor while held) and pushes the
	// release deadline out; a timer releases if the loop stops asking. Every non-walk input
	// (click, cast, hover sweep, panel) calls moveStop() first — with the key held, ANY
	// cursor aim would walk the character toward it.
	walkToHold := func(sx, sy, holdMs int) {
		// Aim via the GetPhysicalCursorPos patch ONLY (the real in-game cursor read on this
		// build). MovePointer's extra WM_MOUSEMOVE/GetCursorPos would feed a competing cursor
		// and make force-move drift, so movement uses the lean physical aim.
		hid.AimPhysical(sx, sy)
		if *realCursor {
			// D2R reads the HARDWARE mouse via RawInput in-game (the injected GetCursorPos is
			// ignored), so force-move only follows the real cursor. Drive the real system cursor
			// to the same screen point — our process's SetCursorPos isn't affected by D2R's patch.
			win.SetCursorPos(int32(gr.WindowLeftX+sx), int32(gr.WindowTopY+sy))
		}
		if *moveKey == "" {
			time.Sleep(20 * time.Millisecond)
			hid.Click(game.LeftButton, sx, sy)
			return
		}
		moveMu.Lock()
		// Override pokes are atomic 1-byte compare writes — idempotent and cheap to repeat
		// (they also self-heal if some other path Restored the stub since our keydown).
		_ = gi.OverrideGetKeyState(mk)
		_ = gi.OverrideGetAsyncKeyState(mk)
		if !moveHeld {
			hid.RawKeyDown(mk)
			moveHeld = true
		}
		if moveTimer != nil {
			moveTimer.Stop()
		}
		// Grace on top of the requested hold: the loop normally re-aims well before this
		// fires; the timer is the dead-man's brake for when it stops asking (combat took
		// the tick, run ended, panel opened elsewhere).
		release := moveStop
		moveTimer = time.AfterFunc(time.Duration(holdMs+150)*time.Millisecond, release)
		moveMu.Unlock()
	}
	walkTo := func(sx, sy int) { walkToHold(sx, sy, 320) }

	// ensureGait keeps her WALKING near enemies (running zeroes defense in D2) and RUNNING when
	// traveling. Player Mode is authoritative while she's moving (Running=3, Walking=2), so this
	// is closed-loop: if the observed gait disagrees with what we want, press Toggle Run/Walk.
	// Also makes movement more deliberate — walking = shorter steps, far less overshoot.
	var runwalkKey byte
	if *runwalkKeyStr != "" {
		runwalkKey = hid.GetASCIICode(*runwalkKeyStr)
	}
	lastGaitToggle := time.Now()
	ensureGait := func(wantRun bool) {
		if runwalkKey == 0 || time.Since(lastGaitToggle) < 250*time.Millisecond {
			return
		}
		moveStop() // gait toggle is a keypress — release the held move first
		m := gr.GetData().PlayerUnit.Mode
		mismatch := (m == mode.Running && !wantRun) || (m == mode.Walking && wantRun)
		if mismatch {
			hid.PressKey(runwalkKey)
			lastGaitToggle = time.Now()
		}
	}
	anyEnemyWithin := func(d game.Data, me data.Position, r int) bool {
		for _, m := range d.Monsters.Enemies() {
			if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= r {
				return true
			}
		}
		return false
	}

	// interactClick does a RELIABLE in-game left-click at screen (sx,sy) — for looting ground
	// items, waypoints, NPCs, chests. A plain message-only left-click is ignored because D2R
	// polls GetKeyState(VK_LBUTTON) (same reason force-move needs the move key HELD), so we:
	// aim the physical cursor, override the left button to "held" for the click window, send
	// the WM button messages, then restore. VK_LBUTTON = 0x01.
	interactClick := func(sx, sy int) {
		moveStop() // a held move key turns this aim+click into a walk
		hid.AimPhysical(sx, sy)
		time.Sleep(30 * time.Millisecond)
		_ = gi.OverrideGetKeyState(0x01)
		_ = gi.OverrideGetAsyncKeyState(0x01)
		hid.Click(game.LeftButton, sx, sy)
		_ = gi.RestoreGetKeyState()
		_ = gi.RestoreGetAsyncKeyState()
	}

	// uiClick clicks a fixed in-game UI coordinate (panel buttons — waypoint destinations, shop
	// items, etc.). In-game panels read the cursor via GetPhysicalCursorPos (the same path we
	// patch), so aim the physical cursor at the button, let it settle, then a discrete click.
	// aimPanel patches the cursor-read export(s) the UI panel reads at client (sx,sy). The
	// in-game WORLD reads GetPhysicalCursorPos, but UI panels (waypoint/shop/stash) on this
	// build may read the classic GetCursorPos (logical coords for a DPI-unaware process) or
	// GetCursorInfo (physical). -panelfn selects which; "all" patches every candidate.
	aimPanel := func(sx, sy int) {
		// MEASURED cursor mapping (2026-07-17), not derived. The game recovers its client point as
		// (physicalCursor - physicalWindowOrigin), so the CLIENT offset must be added UNSCALED to
		// the scaled origin. The old formula scaled the whole sum — (origin+client)*1.5 — which made
		// the game see client*1.5: aiming at row 2 (y=260) put its cursor at y=390.
		//
		// Proof: with the old math, clicking a destination row did nothing and left the panel open
		// (it was landing on row 0 = the current area, the one row where a click is a no-op). Aiming
		// at (250,260)/1.5 = (167,173) — i.e. pre-dividing to cancel the bug — travelled Lut Gholein
		// -> Dry Hills (area 40 -> 42). This formula reproduces those exact physical coords directly.
		px := int(float64(gr.WindowLeftX)*(*dpiScale)) + sx
		py := int(float64(gr.WindowTopY)*(*dpiScale)) + sy
		// "pos" and "all" are GONE. GetCursorPos and GetPhysicalCursorPos are ONE function on this
		// build, so "phys vs pos" switched between two identical things while secretly varying only
		// the coordinate scale — and "all" wrote the pos stub last, so it silently WAS "pos" and
		// never tested "phys" at all. That flag shipped as a which-export diagnostic and drove the
		// M1 plan for a day. GetCursorInfo is the only genuinely distinct export, so it is the only
		// alternative worth keeping.
		if *panelFn == "info" {
			_ = gi.OverrideGetCursorInfo(px, py)
			return
		}
		_ = gi.OverridePhysicalCursorPos(px, py)
	}
	restorePanel := func() {
		_ = gi.RestorePhysicalCursorPos()
		_ = gi.RestoreGetCursorInfo()
		_ = gi.RestoreGetCursorPosAddr()
	}
	uiClick := func(sx, sy int) {
		moveStop() // panels + held force-move don't mix
		aimPanel(sx, sy)
		// Async hover (PostMessage). A SYNCHRONOUS SendMessage hover was tried here on the theory
		// that rows need a processed hover — that theory was WRONG: rows ignored clicks because the
		// ABSOLUTE AIM was off (they were landing on row 0, the current area). With the aim fixed,
		// travel works with this plain async hover, so the sync version bought nothing and is gone.
		// It was also a live hazard: SendMessage BLOCKS on a frozen/crashing D2R window, which is
		// precisely how farmbot hangs mid-run holding patches.
		hid.MouseMoveClient(sx, sy)
		time.Sleep(150 * time.Millisecond)
		// D2R registers a click only when it polls GetKeyState(VK_LBUTTON)=down (same reason
		// force-move needs the key HELD and interactClick overrides it). A bare LeftClickNoMove
		// is ignored on panel rows, so hold the left button "down" across the click window.
		_ = gi.OverrideGetKeyState(0x01)
		_ = gi.OverrideGetAsyncKeyState(0x01)
		hid.LeftClickNoMoveClient(sx, sy) // panels hit-test off client-coord lParam
		_ = gi.RestoreGetKeyState()
		_ = gi.RestoreGetAsyncKeyState()
		time.Sleep(120 * time.Millisecond)
		restorePanel()
		time.Sleep(30 * time.Millisecond)
	}

	// panelLitRow reads the WP panel's lit BLUE compass column and returns (litRow, maxBlueness).
	// The current area's row is marked with a bright-blue compass; maxBlueness > ~25 means the
	// panel is OPEN (and litRow = the area she's in). Used to (a) verify the panel actually opened
	// before clicking a destination, and (b) map currentArea -> row with zero travel.
	panelLitRow := func() (int, float64) {
		img := gr.Screenshot()
		best, at := -1e9, -1
		for r := 0; r < 8; r++ {
			ry := 178 + 41*r
			var sum, n float64
			for y := ry - 8; y <= ry+8; y++ {
				for x := 142; x <= 170; x++ {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					sum += float64(int(bb>>8) - (int(rr>>8)+int(gg>>8))/2)
					n++
				}
			}
			if n > 0 {
				if bl := sum / n; bl > best {
					best, at = bl, r
				}
			}
		}
		return at, best
	}

	// hoverPickClick is the universal "interact with a world thing" primitive (items, chests,
	// barrels, waypoints, shrines, NPCs). A thing's clickable label sits ABOVE its sprite at no
	// fixed screen offset, so plain gameToScreen misses it. We sweep the physical cursor over
	// the region above the world point until the GAME itself reports that unit hovered
	// (HoverData.UnitID), then click exactly there — using the game's own feedback instead of
	// guessing pixels. Returns true if it found the hover and clicked. Proven: hover DOES
	// register with our physical-cursor patch, and clicking a hovered item picks it up.
	isHover := func(unitID data.UnitID) bool {
		hd := gr.GetData().HoverData
		return hd.IsHovered && hd.UnitID == unitID
	}
	hoverPickClick := func(world data.Position, unitID data.UnitID) bool {
		moveStop() // the sweep aims the cursor — held move would walk toward every probe
		for attempt := 0; attempt < 3; attempt++ {
			me := gr.GetData().PlayerUnit.Position
			bx, by := gameToScreen(gr, me.X, me.Y, world.X, world.Y)
			for dy := -80; dy <= 16; dy += 6 {
				for _, dx := range []int{0, -8, 8, -16, 16, -26, 26} {
					px, py := bx+dx, by+dy
					hid.AimPhysical(px, py)
					time.Sleep(55 * time.Millisecond) // let hover state catch up (frame latency)
					if !isHover(unitID) {
						continue
					}
					// DOUBLE-CONFIRM to beat frame latency: the hover we just read may reflect a
					// cursor position from a prior sweep step. Re-aim THIS exact point, settle, and
					// only click if it's still hovered — then the cursor is truly on the label.
					hid.AimPhysical(px, py)
					time.Sleep(70 * time.Millisecond)
					if !isHover(unitID) {
						continue
					}
					// Discrete left-click WITHOUT re-moving the pointer: cursor is already confirmed on
					// the label; Click()'s MovePointer/WM_MOUSEMOVE could nudge it into a ground-move.
					// VK_LBUTTON override for the click window: D2R polls GetKeyState — items happened
					// to accept the bare message click, but chests/objects ignore it without the poll.
					_ = gi.OverrideGetKeyState(0x01)
					_ = gi.OverrideGetAsyncKeyState(0x01)
					hid.LeftClickNoMove(px, py)
					_ = gi.RestoreGetKeyState()
					_ = gi.RestoreGetAsyncKeyState()
					// Left-click-item = "walk to it and pick up" — it takes time and MUST NOT be
					// interrupted by the next loop action. Wait (uninterrupted) for the item to
					// actually leave the ground before returning.
					for w := 0; w < 14; w++ {
						time.Sleep(100 * time.Millisecond)
						still := false
						for _, x := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
							if x.UnitID == unitID {
								still = true
								break
							}
						}
						if !still {
							return true
						}
					}
					return true
				}
			}
		}
		return false
	}

	// interactNPC opens a town NPC's menu. NPC hover DOES fire via HoverData with UnitType==5
	// (monster) — proven by -hovergrid; the earlier failures were STALE aim (she wandered when
	// blind clicks missed). So: re-read positions fresh each attempt, sweep the body, and click
	// ONLY when a monster is hovered (never blind — that's what made her walk around). We prefer
	// matching the target's HoverData.UnitID; log any hovered-monster id so we learn if HoverData's
	// id equals Monster.UnitID on this build.
	interactNPC := func(unitID data.UnitID) bool {
		for attempt := 0; attempt < 6; attempt++ {
			m, ok := gr.GetData().Monsters.FindByID(unitID)
			if !ok {
				logger.Warn("interactNPC: unit not found", "unitID", unitID)
				return false
			}
			me := gr.GetData().PlayerUnit.Position
			bx, by := gameToScreen(gr, me.X, me.Y, m.Position.X, m.Position.Y)
			logger.Info("interactNPC: sweep", "attempt", attempt, "npcDist", chebyshev(me, m.Position),
				"bodyScreen", fmt.Sprintf("(%d,%d)", bx, by))
			for _, dy := range []int{-20, -8, -32, 0, -44, -56, 8} {
				for _, dx := range []int{0, -12, 12, -24, 24, -36, 36} {
					px, py := bx+dx, by+dy
					hid.AimPhysical(px, py)
					time.Sleep(90 * time.Millisecond) // dwell so a solid hover registers
					hd := gr.GetData().HoverData
					if !hd.IsHovered || hd.UnitType != 5 { // 5 = monster/NPC
						continue
					}
					// Re-confirm by RE-AIMING this exact point. A bare re-read is worthless: the
					// hover we just saw can still reflect a PRIOR sweep step's cursor (frame
					// latency), which is exactly the stale aim that made earlier attempts miss.
					// hoverPickClick re-aims here and it works — mirror it.
					hid.AimPhysical(px, py)
					time.Sleep(70 * time.Millisecond)
					if hd = gr.GetData().HoverData; !hd.IsHovered || hd.UnitType != 5 {
						continue
					}
					// Do the hover ids line up with Monster.UnitID on this build? Unknown — log it
					// rather than filter on it, so a mismatch teaches us instead of silently never
					// clicking. (Once proven equal, require the match to avoid grabbing the merc.)
					logger.Info("interactNPC: monster hovered", "hoverID", hd.UnitID, "targetID", unitID,
						"idMatch", hd.UnitID == unitID, "at", fmt.Sprintf("(%d,%d)", px, py))
					_ = gi.OverrideGetKeyState(0x01)
					_ = gi.OverrideGetAsyncKeyState(0x01)
					hid.LeftClickNoMove(px, py)
					_ = gi.RestoreGetKeyState()
					_ = gi.RestoreGetAsyncKeyState()
					time.Sleep(700 * time.Millisecond)
					return true
				}
			}
		}
		return false
	}
	_ = interactNPC

	// screenPointToward returns the FURTHEST on-screen point along a direction from the player.
	// The window is small, so a straight click can land off-edge and the game ignores it.
	// minCarrot pushes a screen point out to a minimum radius from the character. A carrot
	// inside D2's ~40px click dead-zone around the char produces ZERO movement — and since the
	// cursor only sets force-move DIRECTION (holdMs sets distance), a short Navigator step then
	// never advances committedS and the same tiny step re-issues forever: a hard nav deadlock
	// (measured: 5 minutes bit-identical position in town while "walking"). Extending the radius
	// is free — overshoot is impossible, the hold duration limits the step.
	minCarrot := func(sx, sy int) (int, int) {
		maxY := int(float32(gr.GameAreaSizeY) / 1.25)
		ox, oy := float64(sx-cx), float64(sy-cy)
		n := math.Hypot(ox, oy)
		const minR = 120
		if n == 0 {
			return sx, sy
		}
		if n < minR {
			sx = cx + int(ox*minR/n)
			sy = cy + int(oy*minR/n)
			sx = max(50, min(gr.GameAreaSizeX-50, sx))
			sy = max(50, min(maxY-4, sy))
		}
		return sx, sy
	}
	screenPointToward := func(me data.Position, dx, dy int) (int, int) {
		maxY := int(float32(gr.GameAreaSizeY) / 1.25) // stay above the HUD
		sx, sy := cx, cy
		for f := 10; f >= 2; f-- {
			x, y := gameToScreen(gr, me.X, me.Y, me.X+dx*f/10, me.Y+dy*f/10)
			if x > 40 && x < gr.GameAreaSizeX-40 && y > 40 && y < maxY {
				sx, sy = x, y
				break
			}
		}
		return minCarrot(sx, sy)
	}

	// carrotScreen returns the on-screen point to force-move toward to reach world point w, never
	// collapsing to the player's own feet (which would be a zero-motion "move"). Shared by the
	// Navigator-driven chase/explore so movement follows the deliberate planned path.
	carrotScreen := func(me, w data.Position) (int, int, bool) {
		dx, dy := w.X-me.X, w.Y-me.Y
		if dx == 0 && dy == 0 {
			return 0, 0, false
		}
		maxY := int(float32(gr.GameAreaSizeY) / 1.25)
		for f := 100; f >= 10; f -= 10 {
			sx, sy := gameToScreen(gr, me.X, me.Y, me.X+dx*f/100, me.Y+dy*f/100)
			if sx > 40 && sx < gr.GameAreaSizeX-40 && sy > 40 && sy < maxY {
				sx, sy = minCarrot(sx, sy) // dead-zone escape — see minCarrot
				return sx, sy, true
			}
		}
		sx, sy := screenPointToward(me, dx, dy)
		return sx, sy, true
	}
	_ = carrotScreen // retired from chase/explore; kept for -walkto until that probe ports

	// Wait until properly in-game with a sane player position (lets the offsets settle;
	// scanning during a load gives garbage and the character moves the wrong way).
	for i := 0; i < 24; i++ {
		p := gr.GetData().PlayerUnit.Position
		if p.X > 0 && p.Y > 0 {
			logger.Info("in-game", "pos", fmt.Sprintf("(%d,%d)", p.X, p.Y))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Collision-source probe: (1) how much of the level's live collision is loaded in memory
	// (decides whether we can drop koolo-map for a full live grid), and (2) does live collision
	// disagree with koolo-map's grid at the player (confirms mod-changed-town).
	if *collprobe {
		g, err := gr.ReadCurrentRoomGraph()
		if err != nil {
			logger.Error("collprobe: graph failed", "err", err)
			return
		}
		loaded, total := g.CountLoadedRooms()
		logger.Info("collprobe: room collision loaded", "loaded", loaded, "total", total,
			"level", int(g.LevelID))
		coll, ok := gr.ReadRoom1Collision(g.Rooms[g.Current].Room1Ptr)
		if !ok {
			logger.Error("collprobe: current room collision not loaded")
			return
		}
		logger.Info("collprobe: current room live collision",
			"sub", fmt.Sprintf("(%d,%d)", coll.SubX, coll.SubY), "size", fmt.Sprintf("%dx%d", coll.W, coll.H))
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		_ = gr.FetchMapData()
		gg := gr.GetData().AreaData.Grid
		if lf, e := gr.ReadLiveLevelFrame(); e == nil && gg != nil {
			gg.OffsetX, gg.OffsetY = lf.OriginX, lf.OriginY
		}
		p := gr.GetData().PlayerUnit.Position
		logger.Info("collprobe: comparing live-collision vs koolo-grid, player", "pos", fmt.Sprintf("(%d,%d)", p.X, p.Y))
		for dx := 0; dx <= 24; dx += 4 {
			wx, wy := p.X+dx, p.Y
			lb, lok := gr.LiveBlockedAt(coll, wx, wy)
			kw := gg != nil && gg.IsWalkable(data.Position{X: wx, Y: wy})
			logger.Info("collprobe: cell east", "off", dx,
				"liveBlocked", lb, "liveInRoom", lok, "kooloWalkable", kw)
		}
		return
	}

	// Room-graph probe (advisor decisive experiment): does a live Room2 neighbor already report
	// the destination level from mid-town? If yes, the transition direction is knowable now.
	if *roomprobe != 0 {
		g, err := gr.ReadCurrentRoomGraph()
		if err != nil {
			logger.Error("roomprobe: read graph failed", "err", err)
			return
		}
		p := gr.GetData().PlayerUnit.Position
		logger.Info("roomprobe: graph", "curLevel", int(g.LevelID), "rooms", len(g.Rooms),
			"externalNeighbors", len(g.External), "player", fmt.Sprintf("(%d,%d)", p.X, p.Y))
		seen := map[int]int{}
		for _, e := range g.External {
			seen[int(e.LevelID)]++
		}
		for lvl, n := range seen {
			logger.Info("roomprobe: neighbor level", "levelID", lvl, "roomsTouching", n)
		}
		borders := g.BordersTo(area.ID(*roomprobe))
		logger.Info("roomprobe: borders to destination", "dstArea", *roomprobe, "count", len(borders))
		for _, b := range borders {
			ax, _ := b.CrossingAt((b.SpanStart + b.SpanEnd) / 2)
			logger.Info("roomprobe: BORDER", "side", b.Side.String(),
				"spanTiles", fmt.Sprintf("%d..%d", b.SpanStart, b.SpanEnd),
				"fromRect", fmt.Sprintf("(%d,%d %dx%d)", b.FromRect.X, b.FromRect.Y, b.FromRect.W, b.FromRect.H),
				"approachSubtile", fmt.Sprintf("(%d,%d)", ax.X, ax.Y))
		}
		return
	}

	// Waypoint probe: dump nearby objects + OpenMenus + waypoint hover state for ~20s. No clicks
	// (cursor moves only). FetchMapData FIRST: koolo's GetData only populates d.Objects when the
	// current area's map data is cached (GetData merges the live object read with koolo-map's
	// objects inside `if ok`), so without a fetch d.Objects is always empty. Goal: see the live
	// WaypointPortal object and whether hovering it registers IsHovered before we try to click.
	if *wpprobe {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		if err := gr.FetchMapData(); err != nil {
			logger.Error("wpprobe: FetchMapData failed", "err", err)
			return
		}
		end := time.Now().Add(20 * time.Second)
		for time.Now().Before(end) {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			logger.Info("wpprobe: player", "area", int(d.PlayerUnit.Area), "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
			logger.Info("wpprobe: openMenus", "menus", fmt.Sprintf("%+v", d.OpenMenus))
			logger.Info("wpprobe: availableWaypoints", "count", len(d.PlayerUnit.AvailableWaypoints),
				"areas", fmt.Sprintf("%v", d.PlayerUnit.AvailableWaypoints))

			logger.Info("wpprobe: counts", "monsters", len(d.Monsters), "objects", len(d.Objects),
				"entrances", len(d.Entrances), "groundItems", len(d.Inventory.ByLocation(item.LocationGround)),
				"gridNil", d.AreaData.Grid == nil)

			var nearestWP data.Object
			haveWP := false
			nearestWPDist := 1 << 30
			dumped := 0
			for _, o := range d.Objects {
				dd := chebyshev(me, o.Position)
				if dumped < 14 { // dump up to 14 objects at ANY distance, to see what (if anything) is read
					logger.Info("wpprobe: object", "name", int(o.Name), "unitID", int(o.ID),
						"isWaypoint", o.IsWaypoint(), "dist", dd, "selectable", o.Selectable,
						"mode", int(o.Mode), "pos", fmt.Sprintf("(%d,%d)", o.Position.X, o.Position.Y))
					dumped++
				}
				if o.IsWaypoint() && dd < nearestWPDist {
					nearestWP, haveWP, nearestWPDist = o, true, dd
				}
			}
			mdump := 0
			for _, m := range d.Monsters {
				if mdump >= 5 {
					break
				}
				logger.Info("wpprobe: monster", "name", m.Name, "dist", chebyshev(me, m.Position),
					"pos", fmt.Sprintf("(%d,%d)", m.Position.X, m.Position.Y))
				mdump++
			}

			if haveWP {
				sx, sy := gameToScreen(gr, me.X, me.Y, nearestWP.Position.X, nearestWP.Position.Y)
				logger.Info("wpprobe: hovering nearest WP", "name", int(nearestWP.Name), "dist", nearestWPDist,
					"screen", fmt.Sprintf("(%d,%d)", sx, sy))
				hid.MovePointer(sx, sy) // cursor move ONLY — no click
				time.Sleep(150 * time.Millisecond)
				if o2, ok := gr.GetData().Objects.FindByID(nearestWP.ID); ok {
					logger.Info("wpprobe: post-hover WP state", "name", int(o2.Name),
						"isHovered", o2.IsHovered, "selectable", o2.Selectable)
				}
			} else {
				logger.Info("wpprobe: no waypoint object found (any dist)")
			}

			time.Sleep(700 * time.Millisecond)
		}
		logger.Info("wpprobe: done")
		return
	}

	// -inputtest: identify which cursor-read export D2R uses in-game (see inputtest.go).
	if *inputTest {
		if *moveKey == "" {
			logger.Error("-inputtest needs -move e to hold Force-Move")
			return
		}
		moveHold := func(dur time.Duration) {
			_ = gi.OverrideGetKeyState(mk)
			_ = gi.OverrideGetAsyncKeyState(mk)
			hid.HoldKey(mk, dur)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
		}
		runInputTest(logger, gr, gi, moveHold)
		return
	}

	// -findwriter: debugger-based hunt for the cursor-mirror write site (see hwbp.go).
	if *findWriter != "" {
		var addr uint64
		if _, err := fmt.Sscanf(*findWriter, "0x%x", &addr); err != nil {
			if _, err2 := fmt.Sscanf(*findWriter, "%x", &addr); err2 != nil {
				logger.Error("findwriter: bad hex addr", "value", *findWriter)
				return
			}
		}
		runFindWriter(logger, gr, pid, addr)
		return
	}

	// -pathprobe: dump the player's DynamicPath struct (see pathdrive.go). Read-only.
	if *pathProbe {
		runPathProbe(logger, gr, pid)
		return
	}

	// -pathdrive: try to walk her by writing path-target fields (pure memory, non-intrusive).
	if *pathDrive {
		moveHold := func(dur time.Duration) {
			if *moveKey == "" {
				time.Sleep(dur)
				return
			}
			_ = gi.OverrideGetKeyState(mk)
			_ = gi.OverrideGetAsyncKeyState(mk)
			hid.HoldKey(mk, dur)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
		}
		runPathDrive(logger, gr, pid, *pathNoKey, moveHold)
		return
	}

	// -shadowwalk: fully non-intrusive aquarium test (hold Force-Move via memory patch +
	// spam the cursor shadow; no cursor/focus/driver). Needs -move e.
	if *shadowWalk != "" {
		if *moveKey == "" {
			logger.Error("-shadowwalk needs -move e to hold Force-Move")
			return
		}
		var addr uint64
		if _, err := fmt.Sscanf(*shadowWalk, "0x%x", &addr); err != nil {
			if _, err2 := fmt.Sscanf(*shadowWalk, "%x", &addr); err2 != nil {
				logger.Error("shadowwalk: bad hex addr", "value", *shadowWalk)
				return
			}
		}
		game.SetHWMoveMode("off") // ensure no driver strokes — this test must not touch real input
		s, err := newCursorScanner(logger, pid, 0, 0)
		if err != nil {
			logger.Error("shadowwalk: OpenProcess failed", "err", err)
			return
		}
		defer windows.CloseHandle(s.h)
		moveHold := func(dur time.Duration) {
			_ = gi.OverrideGetKeyState(mk)
			_ = gi.OverrideGetAsyncKeyState(mk)
			hid.HoldKey(mk, dur)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
		}
		shadowWalkBG(logger, s, gr, uintptr(addr), "screen*1.5/int32", moveHold)
		return
	}

	// -cursorscan: hunt for D2R's internal cursor-position variable (see cursorscan.go).
	if *cursorScan {
		var modBase uintptr
		var modSize uint32
		if mods, err := d2gomem.GetProcessModules(pid); err == nil {
			for _, m := range mods {
				if strings.Contains(strings.ToLower(m.ModuleName), "d2r.exe") {
					modBase, modSize = m.ModuleBaseAddress, m.ModuleBaseSize
					break
				}
			}
		}
		// moveHold: hold Force-Move for dur WITHOUT touching the cursor (key state only).
		moveHold := func(dur time.Duration) {
			if *moveKey == "" {
				logger.Error("cursordrive needs -move e to hold Force-Move")
				return
			}
			_ = gi.OverrideGetKeyState(mk)
			_ = gi.OverrideGetAsyncKeyState(mk)
			hid.HoldKey(mk, dur)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
		}
		runCursorScan(logger, gr, hid, gi, pid, modBase, modSize, *cursorDrive, *cursorNoFG, moveHold)
		return
	}

	// -cursoraddr: drive the WRITE test against an already-known address (no rescan). Only
	// valid within the SAME D2R process the scan found it in.
	if *cursorAddr != "" {
		var addr uint64
		if _, err := fmt.Sscanf(*cursorAddr, "0x%x", &addr); err != nil {
			if _, err2 := fmt.Sscanf(*cursorAddr, "%x", &addr); err2 != nil {
				logger.Error("cursoraddr: bad hex", "value", *cursorAddr)
				return
			}
		}
		moveHold := func(dur time.Duration) {
			if *moveKey == "" {
				logger.Error("cursoraddr drive needs -move e")
				return
			}
			_ = gi.OverrideGetKeyState(mk)
			_ = gi.OverrideGetAsyncKeyState(mk)
			hid.HoldKey(mk, dur)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
		}
		runCursorDriveAt(logger, gr, hid, pid, uintptr(addr), "screen*1.5/int32", *cursorNoFG, moveHold)
		return
	}

	// -hoverprobe: does D2R SEE our driver-level cursor? Sweep the cursor over the nearest
	// on-screen objects/monsters (no clicks, no walking — wedge-immune, unlike -movetest)
	// and read the game's hover state back from memory. Any hover registering proves the
	// input path end-to-end in the CURRENT foreground/background state — run once with D2R
	// backgrounded and once foregrounded to isolate WM_INPUT delivery.
	if *hoverProbe {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		fetched := false
		for i := 0; i < 12 && !fetched; i++ {
			if err := gr.FetchMapData(); err == nil && gr.MapSeed() != 0 {
				fetched = true
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}
		logger.Info("hoverprobe: map fetch", "ok", fetched, "seed", gr.MapSeed())
		d := gr.GetData()
		me := d.PlayerUnit.Position
		type cand struct {
			label string
			id    data.UnitID
			pos   data.Position
			dist  int
		}
		var cands []cand
		for _, o := range d.Objects {
			cands = append(cands, cand{fmt.Sprintf("obj:%d", int(o.Name)), o.ID, o.Position, chebyshev(me, o.Position)})
		}
		for _, m := range d.Monsters {
			cands = append(cands, cand{fmt.Sprintf("mon:%d", int(m.Name)), m.UnitID, m.Position, chebyshev(me, m.Position)})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		hovered, tried := 0, 0
		maxY := int(float32(gr.GameAreaSizeY) / 1.25) // above the HUD
		for _, c := range cands {
			if tried >= 8 {
				break
			}
			sx, sy := gameToScreen(gr, me.X, me.Y, c.pos.X, c.pos.Y)
			if sx < 20 || sx > gr.GameAreaSizeX-20 || sy < 20 || sy > maxY {
				continue // off-screen — the cursor can't reach it
			}
			tried++
			hid.MovePointer(sx, sy)
			time.Sleep(250 * time.Millisecond)
			d2 := gr.GetData()
			unitHover := false
			if o2, ok := d2.Objects.FindByID(c.id); ok {
				unitHover = o2.IsHovered
			} else if m2, ok := d2.Monsters.FindByID(c.id); ok {
				unitHover = m2.IsHovered
			}
			hit := unitHover || (d2.HoverData.IsHovered && d2.HoverData.UnitID == c.id)
			if hit {
				hovered++
			}
			logger.Info("hoverprobe", "unit", c.label, "dist", c.dist,
				"screen", fmt.Sprintf("(%d,%d)", sx, sy), "unitHovered", unitHover,
				"globalHover", d2.HoverData.IsHovered, "hoverUnitID", int(d2.HoverData.UnitID))
		}
		verdict := "NO hover registered — D2R is not receiving the cursor in this window state"
		if hovered > 0 {
			verdict = "D2R SEES the driver-level cursor"
		}
		logger.Info("hoverprobe: done", "tried", tried, "hovered", hovered, "verdict", verdict)
		return
	}

	// -charprobe: READ-ONLY character sheet straight from the player unit — class byte @+0x17C,
	// Level stat off the statlist, the live skill list, and the currently selected L/R skills.
	// This is the source of truth for combat flags (the KeyBindings offsets are stale on 3.2,
	// but WHAT the char can cast comes from here, not from hotkeys).
	if *charProbe {
		d := gr.GetData()
		p := d.PlayerUnit
		classNames := [...]string{"Amazon", "Sorceress", "Necromancer", "Paladin", "Barbarian", "Druid", "Assassin"}
		className := fmt.Sprintf("unknown(%d)", uint(p.Class))
		if int(p.Class) < len(classNames) {
			className = classNames[p.Class]
		}
		lvl, lvlOK := p.Stats.FindStat(stat.Level, 0)
		if !lvlOK {
			lvl, lvlOK = p.BaseStats.FindStat(stat.Level, 0)
		}
		xp, xpOK := p.Stats.FindStat(stat.Experience, 0)
		if !xpOK {
			xp, _ = p.BaseStats.FindStat(stat.Experience, 0)
		}
		lvlStr := fmt.Sprintf("%d", lvl.Value)
		if !lvlOK {
			lvlStr = "NOT_FOUND"
		}
		skillName := func(id skill.ID) string {
			if n, ok := skill.SkillNames[id]; ok {
				return n
			}
			return fmt.Sprintf("skill#%d", int(id))
		}
		logger.Info("charprobe",
			"name", p.Name, "class", className, "level", lvlStr, "xp", xp.Value,
			"area", int(p.Area), "pos", fmt.Sprintf("(%d,%d)", p.Position.X, p.Position.Y),
			"leftSkill", skillName(p.LeftSkill), "rightSkill", skillName(p.RightSkill))
		for id, pts := range p.Skills {
			logger.Info("charprobe: skill", "id", int(id), "name", skillName(id),
				"level", pts.Level, "quantity", pts.Quantity, "charges", pts.Charges)
		}
		logger.Info("charprobe: belt", "items", len(d.Inventory.Belt.Items),
			"rows", d.Inventory.Belt.Rows(), "beltName", string(d.Inventory.Belt.Name))
		for _, bp := range d.Inventory.Belt.Items {
			logger.Info("charprobe: belt item", "name", string(bp.Name),
				"pos", fmt.Sprintf("(%d,%d)", bp.Position.X, bp.Position.Y))
		}
		for _, loc := range []item.LocationType{item.LocationEquipped, item.LocationInventory} {
			its := d.Inventory.ByLocation(loc)
			logger.Info("charprobe: items", "location", string(loc), "count", len(its))
			for _, it := range its {
				logger.Info("charprobe: item", "location", string(loc), "name", string(it.Name),
					"quality", it.Quality.ToString())
			}
		}
		return
	}

	// Live-level-origin probe (advisor Rank 1 validation): read DrlgLevel Pos/Size from memory,
	// generate the grid, and confirm size*5 == grid size and origin*5 ≈ the fitted offset.
	if *levelprobe {
		lf, err := gr.ReadLiveLevelFrame()
		if err != nil {
			logger.Error("levelprobe: read failed", "err", err)
			return
		}
		logger.Info("levelprobe: live level frame",
			"area", lf.Area,
			"rawPos", fmt.Sprintf("(%d,%d) tiles", lf.RawPosX, lf.RawPosY),
			"rawSize", fmt.Sprintf("(%d,%d) tiles", lf.RawSizeX, lf.RawSizeY),
			"originSubtiles", fmt.Sprintf("(%d,%d)", lf.OriginX, lf.OriginY),
			"sizeSubtiles", fmt.Sprintf("(%d,%d)", lf.SizeX, lf.SizeY))
		pd := gr.GetData()
		logger.Info("levelprobe: player", "pos", fmt.Sprintf("(%d,%d)", pd.PlayerUnit.Position.X, pd.PlayerUnit.Position.Y))
		for _, e := range pd.Entrances {
			logger.Info("levelprobe: LIVE entrance", "name", e.Name,
				"pos", fmt.Sprintf("(%d,%d)", e.Position.X, e.Position.Y))
		}
		for _, al := range pd.AdjacentLevels {
			logger.Info("levelprobe: koolo adjacent", "area", int(al.Area), "isEntrance", al.IsEntrance,
				"pos", fmt.Sprintf("(%d,%d)", al.Position.X, al.Position.Y))
		}
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		if err := gr.FetchMapData(); err != nil {
			logger.Error("levelprobe: FetchMapData failed", "err", err)
			return
		}
		d := gr.GetData()
		g := d.AreaData.Grid
		if g != nil {
			logger.Info("levelprobe: generated grid",
				"gridSize", fmt.Sprintf("(%d,%d)", g.Width, g.Height),
				"kooloMapOffset", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY),
				"SIZE_MATCH", lf.SizeX == g.Width && lf.SizeY == g.Height,
				"delta_liveMinusKoolo", fmt.Sprintf("(%d,%d)", lf.OriginX-g.OffsetX, lf.OriginY-g.OffsetY),
				"playerInLevel", func() bool {
					p := d.PlayerUnit.Position
					return p.X >= lf.OriginX && p.X < lf.OriginX+lf.SizeX && p.Y >= lf.OriginY && p.Y < lf.OriginY+lf.SizeY
				}())
		}
		return
	}

	// Feasibility probe: can koolo-map generate a walkable grid for THIS modded area?
	// (Run from C:\dev\koolo-build so ./tools/koolo-map.exe resolves.)
	if *mapcheck {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		logger.Info("fetching map data", "d2lod", config.Koolo.D2LoDPath, "diff", *diff)
		if err := gr.FetchMapData(); err != nil {
			logger.Error("FetchMapData failed", "err", err)
			return
		}
		d := gr.GetData()
		w, h := 0, 0
		if d.AreaData.Grid != nil {
			w, h = d.AreaData.Grid.Width, d.AreaData.Grid.Height
		}
		logger.Info("MAP RESULT", "seed", gr.MapSeed(), "area", int(d.PlayerUnit.Area),
			"name", d.AreaData.Name, "gridW", w, "gridH", h,
			"exits", len(d.AdjacentLevels), "objects", len(d.Objects),
			"playerPos", fmt.Sprintf("(%d,%d)", d.PlayerUnit.Position.X, d.PlayerUnit.Position.Y),
			"areaOrigin", fmt.Sprintf("(%d,%d)", d.AreaOrigin.X, d.AreaOrigin.Y))
		return
	}

	// ALIGNMENT PROBE: is koolo-map's grid the same GEOMETRY as the mod's level (just offset
	// wrong = fixable), or a different map entirely? Walk around to sample guaranteed-walkable
	// world tiles, then brute-force the grid offset that puts the most samples on walkable cells.
	if *mapalign {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		if err := gr.FetchMapData(); err != nil {
			logger.Error("FetchMapData failed", "err", err)
			return
		}
		grid := gr.GetData().AreaData.Grid
		if grid == nil {
			logger.Error("no grid for current area")
			return
		}
		// Collect distinct walked tiles over a WIDE-ranging wander (commit each heading ~8 steps
		// so we actually travel and get a large-span cloud — small clusters match many offsets).
		pts := []data.Position{}
		seen := map[[2]int]bool{}
		end := time.Now().Add(35 * time.Second)
		tick := 0
		for time.Now().Before(end) {
			p := gr.GetData().PlayerUnit.Position
			k := [2]int{p.X, p.Y}
			if p.X > 0 && !seen[k] {
				seen[k] = true
				pts = append(pts, p)
			}
			tick++
			angle := float64((tick/8)%8) / 8.0 * 2 * math.Pi
			walkTo(cx+int(280*math.Cos(angle)), cy+int(150*math.Sin(angle)))
		}
		if len(pts) == 0 {
			logger.Error("collected no points")
			return
		}
		minX, minY, maxX, maxY := 1<<30, 1<<30, -(1 << 30), -(1 << 30)
		for _, p := range pts {
			minX, maxX = min(minX, p.X), max(maxX, p.X)
			minY, maxY = min(minY, p.Y), max(maxY, p.Y)
		}
		score := func(ox, oy int) int {
			s := 0
			for _, p := range pts {
				rx, ry := p.X-ox, p.Y-oy
				if rx >= 0 && rx < grid.Width && ry >= 0 && ry < grid.Height &&
					grid.CollisionGrid[ry][rx] == game.CollisionTypeWalkable {
					s++
				}
			}
			return s
		}
		// Count how many DISTINCT offsets achieve a near-perfect fit — if many, the cloud is
		// still too ambiguous to trust; if ~1, the alignment is real.
		bestOX, bestOY, bestScore := grid.OffsetX, grid.OffsetY, -1
		perfectOffsets := 0
		for oy := maxY - grid.Height + 1 - 20; oy <= minY+20; oy++ {
			for ox := maxX - grid.Width + 1 - 20; ox <= minX+20; ox++ {
				s := score(ox, oy)
				if s > bestScore {
					bestScore, bestOX, bestOY = s, ox, oy
				}
			}
		}
		for oy := maxY - grid.Height + 1 - 20; oy <= minY+20; oy++ {
			for ox := maxX - grid.Width + 1 - 20; ox <= minX+20; ox++ {
				if score(ox, oy) >= bestScore {
					perfectOffsets++
				}
			}
		}
		logger.Info("ALIGN RESULT",
			"samples", len(pts),
			"ptSpan", fmt.Sprintf("%dx%d", maxX-minX, maxY-minY),
			"kooloMapOffset", fmt.Sprintf("(%d,%d)", grid.OffsetX, grid.OffsetY),
			"scoreAtKooloOffset", fmt.Sprintf("%d/%d", score(grid.OffsetX, grid.OffsetY), len(pts)),
			"bestFitOffset", fmt.Sprintf("(%d,%d)", bestOX, bestOY),
			"bestScore", fmt.Sprintf("%d/%d", bestScore, len(pts)),
			"delta", fmt.Sprintf("(%d,%d)", bestOX-grid.OffsetX, bestOY-grid.OffsetY),
			"offsetsTiedAtBest", perfectOffsets)
		return
	}

	// Map data (the "maphack"): full-area grids + the area adjacency graph, generated from the
	// live map seed via koolo-map + D2LoD. Best-effort — the LIVE grid stays the collision
	// authority (especially in town, where classic collision is wrong for the mod); map data
	// powers multi-hop -goto routing, far-object knowledge, and adjacency.
	cfg.Game.Difficulty = difficulty.Difficulty(*diff)
	if err := gr.FetchMapData(); err != nil {
		logger.Warn("map data unavailable — multi-hop -goto disabled", "err", err)
	} else {
		logger.Info("map data loaded", "areas", len(gr.GetData().Areas))
	}

	// Pre-buff in HUMAN form (summons/spirit can't be cast as a werewolf), then shapeshift.
	// Skipped for -walkto: that mode proves locomotion only and stays combat-free/human-form.
	// Skipped for the town-UI probes: pre-buff's right-click DISMISSES an open NPC dialogue,
	// which broke multi-process panel calibration (measured: TRADE click hit a closed menu).
	if *walkToPt == "" && !*moveTest && *npcClick == "" && *uiClickAt == "" && *uiSnap == "" {
		logger.Info("pre-buff: summons + spirit, then shapeshift")
		castSelf(*wolves)
		castSelf(*wolves)
		castSelf(*wolves)
		castSelf(*creeper)
		castSelf(*spirit)
		castSelf(*werewolf)
	}

	// Navigation setup. Alignment = read the live DrlgLevel origin from memory and install it as
	// the grid offset (koolo-map's Offset is the classic DrlgLevel.PosX*5; live D2R is the exact
	// match) — deterministic, no movement. navDelta translates map-frame entities (exits/objects)
	// into live coords. Everything is re-run when the character crosses into a new area.
	var navGrid *game.Grid
	var navi *Navigator // clearance-aware path follower (built per aligned area)
	alignedArea := -1
	var walkables []data.Position
	var walkCentroid data.Position

	alignArea := func() (*game.Grid, data.Position, bool) {
		g := gr.GetData().AreaData.Grid
		if g == nil {
			return nil, data.Position{}, false
		}
		p := gr.GetData().PlayerUnit.Position
		lf, err := gr.ReadLiveLevelFrame()
		if err != nil || lf.SizeX != g.Width || lf.SizeY != g.Height ||
			!(p.X >= lf.OriginX && p.X < lf.OriginX+lf.SizeX && p.Y >= lf.OriginY && p.Y < lf.OriginY+lf.SizeY) {
			return nil, data.Position{}, false
		}
		delta := data.Position{X: lf.OriginX - g.OffsetX, Y: lf.OriginY - g.OffsetY}
		// CLONE before aligning — g points into the cached map data, and mutating its offsets
		// breaks every later map-frame->live translation (mapExitTo's delta degenerates to 0).
		ng := *g
		ng.OffsetX, ng.OffsetY = lf.OriginX, lf.OriginY
		return &ng, delta, true
	}
	computeWalk := func(g *game.Grid) ([]data.Position, data.Position) {
		var cells []data.Position
		var sx, sy int
		for y := 0; y < g.Height; y++ {
			for x := 0; x < g.Width; x++ {
				if g.CollisionGrid[y][x] == game.CollisionTypeWalkable {
					cells = append(cells, data.Position{X: x, Y: y})
					sx, sy = sx+x, sy+y
				}
			}
		}
		c := data.Position{}
		if len(cells) > 0 {
			c = data.Position{X: sx/len(cells) + g.OffsetX, Y: sy/len(cells) + g.OffsetY}
		}
		return cells, c
	}

	// mapNavi plans on the FULL-AREA map grid (aligned to the live origin) — the live grid only
	// spans loaded rooms, so long-range paths (across the river to a corpse, to a far exit) fail
	// on it while the map grid knows every bridge. NOT built in town: the classic town collision
	// is wrong for the mod (phantom sliver — desktop-measured).
	var mapNavi *Navigator
	buildMapNavi := func() {
		mapNavi = nil
		d := gr.GetData()
		if d.PlayerUnit.Area.IsTown() {
			return
		}
		if mg, _, ok := alignArea(); ok {
			mapNavi = NewNavigator(mg)
			return
		}
		g := d.AreaData.Grid
		lf, err := gr.ReadLiveLevelFrame()
		switch {
		case g == nil:
			logger.Warn("mapNavi: no map grid for area", "area", int(d.PlayerUnit.Area))
		case err != nil:
			logger.Warn("mapNavi: live frame read failed", "err", err)
		case lf.SizeX == g.Height && lf.SizeY == g.Width && navGrid != nil:
			// TRANSPOSED dimensions (measured: live Blood Moor 280x480 vs map 480x280 while town
			// matches exactly). Orientation is NOT assumed: build all four transpose variants and
			// score each against the live grid's collision in the loaded region — walkable AND
			// blocked agreement both, so an all-walkable garbage grid can't fake a match.
			variant := func(fx, fy bool) *game.Grid {
				cg := make([][]game.CollisionType, lf.SizeY)
				for r := 0; r < lf.SizeY; r++ {
					row := make([]game.CollisionType, lf.SizeX)
					for c := 0; c < lf.SizeX; c++ {
						rc, cc := c, r
						if fx {
							rc = lf.SizeX - 1 - c
						}
						if fy {
							cc = lf.SizeY - 1 - r
						}
						row[c] = g.CollisionGrid[rc][cc]
					}
					cg[r] = row
				}
				return game.NewGrid(cg, lf.OriginX, lf.OriginY)
			}
			var blocked []data.Position
			for y := 0; y < navGrid.Height && len(blocked) < 2000; y += 3 {
				for x := 0; x < navGrid.Width && len(blocked) < 2000; x += 3 {
					if navGrid.CollisionGrid[y][x] == game.CollisionTypeNonWalkable {
						blocked = append(blocked, data.Position{X: x + navGrid.OffsetX, Y: y + navGrid.OffsetY})
					}
				}
			}
			score := func(t *game.Grid) float64 {
				wa, wt := 0, 0
				for i := 0; i < len(walkables); i += 5 {
					w := data.Position{X: walkables[i].X + navGrid.OffsetX, Y: walkables[i].Y + navGrid.OffsetY}
					if t.IsWalkable(w) {
						wa++
					}
					wt++
				}
				ba, bt := 0, 0
				for _, b := range blocked {
					if !t.IsWalkable(b) {
						ba++
					}
					bt++
				}
				if wt == 0 || bt == 0 {
					return 0
				}
				return (float64(wa)/float64(wt) + float64(ba)/float64(bt)) / 2
			}
			bestScore, bestFx, bestFy := 0.0, false, false
			for _, v := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
				if s := score(variant(v[0], v[1])); s > bestScore {
					bestScore, bestFx, bestFy = s, v[0], v[1]
				}
			}
			if bestScore > 0.65 {
				mapNavi = NewNavigator(variant(bestFx, bestFy))
				logger.Info("mapNavi: TRANSPOSED map grid aligned", "agreement", fmt.Sprintf("%.2f", bestScore),
					"flipX", bestFx, "flipY", bestFy)
			} else {
				logger.Warn("mapNavi: no transpose variant agrees with live collision",
					"best", fmt.Sprintf("%.2f", bestScore))
			}
		default:
			logger.Warn("mapNavi: align failed", "liveSize", fmt.Sprintf("%dx%d", lf.SizeX, lf.SizeY),
				"mapSize", fmt.Sprintf("%dx%d", g.Width, g.Height),
				"liveOrigin", fmt.Sprintf("(%d,%d)", lf.OriginX, lf.OriginY),
				"playerIn", d.PlayerUnit.Position.X >= lf.OriginX && d.PlayerUnit.Position.X < lf.OriginX+lf.SizeX)
		}
	}

	// mapExitTo translates the CURRENT area's map-frame exit toward `hop` into live coords:
	// delta = liveGridOrigin - mapGridOrigin. Correct in -livegrid mode (map grid offsets are
	// untouched there); in koolo-map mode alignArea mutates the cached offsets so the delta
	// degenerates to 0 — acceptable, this project always runs -livegrid.
	mapExitTo := func(d game.Data, hop area.ID) (data.Position, bool) {
		if navGrid == nil || d.AreaData.Grid == nil {
			return data.Position{}, false
		}
		for _, al := range d.AdjacentLevels {
			if al.Area == hop {
				return data.Position{
					X: al.Position.X + navGrid.OffsetX - d.AreaData.Grid.OffsetX,
					Y: al.Position.Y + navGrid.OffsetY - d.AreaData.Grid.OffsetY,
				}, true
			}
		}
		return data.Position{}, false
	}

	// acquireGrid returns the navigation grid for the current area. Prefers the LIVE room
	// collision (mod-accurate, needs no FetchMapData) when -livegrid is set; otherwise falls
	// back to koolo-map's aligned grid. This is the single choke point for grid acquisition,
	// used by both initial nav setup and per-area re-alignment.
	// The CARTOGRAPHER: every live grid build feeds the persistent atlas (mod-accurate collision
	// keyed by map seed + area, on disk), and the atlas immediately pays it back — rooms Benji
	// has EVER visited overlay onto the fresh grid, so navigation remembers terrain the room
	// streamer has since unloaded. Truth accumulates; the fish-flop radius shrinks.
	atlas := game.NewAtlas(filepath.Join("logs", "atlas"))
	acquireGrid := func() (*game.Grid, bool) {
		if *liveGrid {
			if lg, rooms, err := gr.BuildLiveGridRooms(); err == nil {
				seed := gr.MapSeed()
				dd := gr.GetData()
				areaID := int(dd.PlayerUnit.Area)
				atlas.MergeLiveGrid(seed, areaID, lg, rooms)
				atlas.Overlay(seed, areaID).OverlayOnto(lg)
				// POISON ESCAPE HATCH: if the overlay claims the cell we are STANDING ON is
				// refused/blocked, presence is proof it's wrong — lift the stamp (atlas + this
				// grid) or planning-from-here fails forever and the burst cascade re-stamps
				// more wedges (measured: 41 wedges in 4 minutes, playerWalkable=false).
				me := dd.PlayerUnit.Position
				if !lg.IsWalkable(me) {
					if n := atlas.ClearRefusedNear(seed, areaID, me, 3); n > 0 {
						logger.Warn("nav: cleared refusals covering our own position", "cells", n)
						atlas.Overlay(seed, areaID).OverlayOnto(lg) // won't help blocked->blocked, so also lift locally:
					}
					rp := lg.RelativePosition(me)
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							x, y := rp.X+dx, rp.Y+dy
							if x >= 0 && y >= 0 && x < lg.Width && y < lg.Height {
								lg.CollisionGrid[y][x] = game.CollisionTypeWalkable
							}
						}
					}
				}
				logger.Info("nav: live grid built", "roomsLoaded", len(rooms),
					"origin", fmt.Sprintf("(%d,%d)", lg.OffsetX, lg.OffsetY),
					"atlasKnown", atlas.KnownCells(seed, areaID))
				return lg, true
			} else {
				logger.Warn("nav: BuildLiveGrid failed, falling back to koolo-map", "err", err)
			}
		}
		g, _, ok := alignArea()
		return g, ok
	}

	// -movetest: force-move ACTUATOR calibration. From the neutral cursor, pulse the Force-Move
	// toward each of 8 screen directions and log the resulting WORLD-subtile delta. This maps
	// screen-direction -> world-direction and exposes any axis inversion/mismap that would make
	// path-following drift the wrong way (suspected root of "walks the wrong way, then wedges").
	if *moveTest {
		type mtDir struct {
			name   string
			dx, dy int
		}
		// Targets map to PURE world axes via the inverse iso (diffX ∝ u+2v, diffY ∝ 2v-u): (±200,±100)
		// zeroes one world axis, stays on-screen + above the HUD. SELF-CONTROL aims at the char's own
		// anchor (expect ~0 move; any consistent drift = a real anchor offset). Opposite pairs must cancel.
		// Run in a MONSTER-FREE open spot (pre-buff/summons are skipped for movetest).
		dirs := []mtDir{
			{"SELF-CONTROL", 0, 0},
			{"WORLD+X east", 200, 100},
			{"WORLD-X west", -200, -100},
			{"WORLD+Y south", -200, 100},
			{"WORLD-Y north", 200, -100},
		}
		netX, netY := 0, 0
		for _, dr := range dirs {
			before := gr.GetData().PlayerUnit.Position
			sx, sy := cx+dr.dx, cy+dr.dy
			walkToHold(sx, sy, 400)
			// Readback: is D2R actually foreground, and did the OS cursor land where we aimed?
			// Without these two facts every direction-following result is uninterpretable.
			var cur win.POINT
			win.GetCursorPos(&cur)
			fgIsD2R := win.GetForegroundWindow() == gr.HWND
			time.Sleep(600 * time.Millisecond) // let position settle before reading
			after := gr.GetData().PlayerUnit.Position
			netX += after.X - before.X
			netY += after.Y - before.Y
			logger.Info("movetest", "dir", dr.name, "cursor", fmt.Sprintf("(%d,%d)", sx, sy),
				"before", fmt.Sprintf("(%d,%d)", before.X, before.Y), "after", fmt.Sprintf("(%d,%d)", after.X, after.Y),
				"worldDelta", fmt.Sprintf("(%+d,%+d)", after.X-before.X, after.Y-before.Y),
				"runningNet", fmt.Sprintf("(%+d,%+d)", netX, netY),
				"osCursor", fmt.Sprintf("(%d,%d) want (%d,%d)", cur.X, cur.Y, gr.WindowLeftX+sx, gr.WindowTopY+sy),
				"fgIsD2R", fgIsD2R)
		}
		logger.Info("movetest: done", "net", fmt.Sprintf("(%+d,%+d)", netX, netY))
		return
	}

	// -walkto: point-to-point locomotion proof. Force-moves to a single WORLD subtile via the
	// Navigator (nav.go) — the clearance-aware path follower — reports ARRIVED/WEDGED-*, exits.
	// Root cause this proves out: screenPointToward silently returns the player's own screen
	// center (cx,cy) when a heading can't fit the 853x480 window (edge directions), which reads
	// as a zero-motion "move" and wedges the farm loop. carrotScreen below never does that.
	if *walkToPt != "" {
		var tx, ty int
		if _, err := fmt.Sscanf(*walkToPt, "%d,%d", &tx, &ty); err != nil {
			logger.Error("walkto: bad -walkto", "value", *walkToPt, "err", err)
			return
		}
		target := data.Position{X: tx, Y: ty}
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)

		// [FIX1] Refresh GameAreaSizeX/Y before any screen math — the window may have moved or
		// resized since attach. updateWindowPositionData on MemoryReader is unexported; MovePointer
		// is the only exported path that triggers it, so pulse it at the existing screen center.
		hid.MovePointer(cx, cy)

		// FetchMapData is flaky and sometimes returns no grid for the current area (or a stale
		// one) — retry with alignment + seed validation instead of trusting the first fetch.
		var g *game.Grid
		var delta data.Position
		aligned := false
		for i := 0; i < 12; i++ {
			if err := gr.FetchMapData(); err != nil {
				logger.Warn("walkto: FetchMapData failed", "attempt", i, "err", err)
				time.Sleep(500 * time.Millisecond)
				continue
			}
			ag, ad, ok := alignArea()
			// [FIX6] MapSeed() is a real public accessor (game.MemoryReader) — require a non-zero
			// seed in addition to alignArea's own origin/size/containment validation before
			// accepting the fetch. (If no such accessor existed, alignArea's validation alone
			// would have to be trusted here.)
			if ok && gr.MapSeed() != 0 {
				g, delta, aligned = ag, ad, true
				break
			}
			logger.Warn("walkto: align failed, retrying", "attempt", i,
				"gridNil", gr.GetData().AreaData.Grid == nil, "seed", gr.MapSeed())
			time.Sleep(500 * time.Millisecond)
		}
		if !aligned {
			logger.Error("walkto: no aligned grid after retries")
			return
		}

		// For mod-altered areas koolo-map's collision content is wrong (it can mark the
		// player's own tile blocked). Rebuild the grid from live room collision — authoritative.
		if *liveGrid {
			lg, nrooms, err := gr.BuildLiveGrid()
			if err != nil {
				logger.Error("walkto: BuildLiveGrid failed", "err", err)
				return
			}
			g = lg
			delta = data.Position{} // live grid is aligned by construction
			logger.Info("walkto: using LIVE collision grid", "roomsLoaded", nrooms,
				"origin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY), "size", fmt.Sprintf("%dx%d", g.Width, g.Height),
				"startWalkable", g.IsWalkable(gr.GetData().PlayerUnit.Position))
		}

		walkables, _ := computeWalk(g)
		wnavi := NewNavigator(g)
		area0 := int(gr.GetData().PlayerUnit.Area)
		me0 := gr.GetData().PlayerUnit.Position

		// Snap the requested target to the nearest cell that's strictly walkable and has
		// clearance beyond navHardRadius (so the goal itself isn't wall-hugging). [FIX2] Prefer
		// cells away from the grid border on the first pass — border cells get optimistic
		// clearance (the BFS in buildClearance never sees a "wall" past the edge of loaded map
		// data), which re-exposes the same edge wedge screenPointToward has. Only fall back to a
		// border cell if no interior candidate exists at all.
		var goal data.Position
		haveGoal, nearEdge := false, false
		bestSnapDist := 1 << 30
		for _, c := range walkables {
			wp := data.Position{X: c.X + g.OffsetX, Y: c.Y + g.OffsetY}
			if !g.IsWalkable(wp) || wnavi.clearanceAt(wp) <= navHardRadius {
				continue
			}
			if c.X < navHardRadius || c.X >= g.Width-navHardRadius ||
				c.Y < navHardRadius || c.Y >= g.Height-navHardRadius {
				continue
			}
			if d := chebyshev(target, wp); d < bestSnapDist {
				bestSnapDist, goal, haveGoal = d, wp, true
			}
		}
		if !haveGoal {
			nearEdge = true
			for _, c := range walkables {
				wp := data.Position{X: c.X + g.OffsetX, Y: c.Y + g.OffsetY}
				if !g.IsWalkable(wp) || wnavi.clearanceAt(wp) <= navHardRadius {
					continue
				}
				if d := chebyshev(target, wp); d < bestSnapDist {
					bestSnapDist, goal, haveGoal = d, wp, true
				}
			}
		}
		if !haveGoal {
			logger.Error("walkto: no plannable cell near target", "target", fmt.Sprintf("(%d,%d)", tx, ty))
			return
		}
		if nearEdge {
			logger.Warn("walkto: goal near grid edge", "goal", fmt.Sprintf("(%d,%d)", goal.X, goal.Y))
		}

		startDist := chebyshev(me0, goal)
		logger.Info("walkto: START", "area", area0, "me0", fmt.Sprintf("(%d,%d)", me0.X, me0.Y),
			"requested", fmt.Sprintf("(%d,%d)", tx, ty), "snapped", fmt.Sprintf("(%d,%d)", goal.X, goal.Y),
			"snapDist", bestSnapDist, "startDist", startDist,
			"gridOrigin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY), "delta", fmt.Sprintf("(%d,%d)", delta.X, delta.Y),
			"walkables", len(walkables), "startWalkable", g.IsWalkable(me0), "startClr", wnavi.clearanceAt(me0),
			"gameAreaSize", fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY))

		if !wnavi.BuildPlan(me0, goal, time.Now()) {
			logger.Error("walkto: WEDGED-NOROUTE")
			return
		}

		// [FIX4] Hardened actuator: screenPointToward silently returns the player's own screen
		// center (cx,cy) when NO on-screen point fits the direction — walking toward your own feet
		// is a zero-motion "move" that looks like progress in logs but wedges the loop. carrotScreen
		// refuses that case (ok=false) instead of masking it.
		carrotScreen := func(me, w data.Position) (int, int, bool) {
			dx, dy := w.X-me.X, w.Y-me.Y
			if dx == 0 && dy == 0 {
				return 0, 0, false
			}
			maxY := int(float32(gr.GameAreaSizeY) / 1.25)
			for f := 100; f >= 10; f -= 10 {
				sx, sy := gameToScreen(gr, me.X, me.Y, me.X+dx*f/100, me.Y+dy*f/100)
				if sx > 40 && sx < gr.GameAreaSizeX-40 && sy > 40 && sy < maxY {
					return sx, sy, true
				}
			}
			sx, sy := screenPointToward(me, dx, dy) // last resort
			return sx, sy, true
		}

		start := time.Now()
		deadline := start.Add(time.Duration(*seconds) * time.Second)
		verdict := "WEDGED-TIMEOUT"
		minDist := startDist
		noProg := start
		replans := 0
		tick := 0
		final := me0

	walkLoop:
		for time.Now().Before(deadline) {
			now := time.Now()
			d := gr.GetData()
			me := d.PlayerUnit.Position
			final = me
			if int(d.PlayerUnit.Area) != area0 {
				verdict = "AREA-CHANGED"
				break
			}
			dist := chebyshev(me, goal)
			if dist < minDist {
				minDist = dist
				noProg = now
			}
			// Arrival check MUST run before wnavi.Step, so carrotScreen never sees me==goal (a
			// zero-length carrot is exactly the dx==0&&dy==0 case above) — do not reorder.
			if dist <= 6 || fdist(me, goal) <= navArrive {
				verdict = "ARRIVED"
				break
			}
			step := wnavi.Step(me, now)
			switch {
			case step.Arrived:
				if sx, sy, ok := carrotScreen(me, goal); ok {
					walkToHold(sx, sy, 120)
				}
			case step.Diverged:
				replans++
				if replans > 30 || !wnavi.BuildPlan(me, goal, now) {
					verdict = "WEDGED-DIVERGED"
					break walkLoop
				}
				logger.Info("walkto: replan", "replans", replans, "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
				continue
			case step.Stuck:
				// Same handling as default — Step already computed a local-escape recovery target.
				fallthrough
			default:
				hold := step.HoldMs
				if hold <= 0 {
					hold = 200
				}
				if sx, sy, ok := carrotScreen(me, step.Target); ok {
					walkToHold(sx, sy, hold)
				}
			}
			if time.Since(noProg) > 12*time.Second {
				verdict = "WEDGED-NOPROGRESS"
				break
			}
			tick++
			if tick%3 == 0 {
				pathEnd := 0.0
				if n := len(wnavi.cumS); n > 0 {
					pathEnd = wnavi.cumS[n-1]
				}
				logger.Info("walkto: step", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y), "dist", dist,
					"minDist", minDist, "committedS", int(wnavi.committedS), "pathEnd", int(pathEnd),
					"target", fmt.Sprintf("(%d,%d)", step.Target.X, step.Target.Y), "hold", step.HoldMs,
					"arr", step.Arrived, "div", step.Diverged, "stk", step.Stuck, "stuckHits", wnavi.stuckHits,
					"clr", wnavi.clearanceAt(me), "walkable", g.IsWalkable(me))
			}
			time.Sleep(60 * time.Millisecond)
		}

		logger.Info("walkto: RESULT", "verdict", verdict, "start", fmt.Sprintf("(%d,%d)", me0.X, me0.Y),
			"goal", fmt.Sprintf("(%d,%d)", goal.X, goal.Y), "final", fmt.Sprintf("(%d,%d)", final.X, final.Y),
			"startDist", startDist, "finalDist", chebyshev(final, goal), "minDist", minDist,
			"netMoved", chebyshev(me0, final), "replans", replans, "ticks", tick,
			"elapsedS", time.Since(start).Seconds())
		return
	}

	// -objprobe: READ-ONLY interactables inventory. Objects come straight from the unit table
	// (no map data needed since the GetData fix); entrances from the live DrlgLevel chain.
	if *objProbe != 0 {
		d := gr.GetData()
		me := d.PlayerUnit.Position
		logger.Info("objprobe", "area", int(d.PlayerUnit.Area), "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
			"objectsTotal", len(d.Objects), "entrances", len(d.Entrances), "monsters", len(d.Monsters))
		type row struct {
			o    data.Object
			dist int
		}
		var rows []row
		for _, o := range d.Objects {
			if dist := chebyshev(me, o.Position); dist <= *objProbe {
				rows = append(rows, row{o, dist})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].dist < rows[j].dist })
		for _, r := range rows {
			o := r.o
			name := o.Desc().Name
			if name == "" {
				name = fmt.Sprintf("obj#%d", int(o.Name))
			}
			logger.Info("objprobe: object", "dist", r.dist, "name", name, "id", int(o.Name),
				"pos", fmt.Sprintf("(%d,%d)", o.Position.X, o.Position.Y),
				"selectable", o.Selectable, "interact", fmt.Sprintf("%v", o.InteractType),
				"mode", int(o.Mode), "owner", o.Owner,
				"chest", o.IsChest(), "wp", o.IsWaypoint(),
				"portal", o.IsPortal() || o.IsRedPortal(), "shrine", o.IsShrine())
		}
		for _, e := range d.Entrances {
			logger.Info("objprobe: entrance", "dist", chebyshev(me, e.Position), "name", int(e.Name),
				"pos", fmt.Sprintf("(%d,%d)", e.Position.X, e.Position.Y),
				"selectable", e.Selectable, "hovered", e.IsHovered)
		}
		return
	}

	// -interact: the generic world-interactable exerciser. Approach the nearest matching thing,
	// hover-click it via the game's own HoverData feedback, then report EVERY observable change
	// (object Mode/Selectable, OpenMenus, area, nearby ground items) — each interactable kind
	// teaches us its own success signal instead of us assuming one.
	if *interactWith != "" {
		if *moveKey == "" {
			logger.Error("-interact needs -move e (it walks to the target)")
			return
		}
		kind := *interactWith
		var wantID int
		if n, err := fmt.Sscanf(kind, "%d", &wantID); n == 1 && err == nil {
			kind = "id"
		}
		matchObj := func(o data.Object) bool {
			switch kind {
			case "chest":
				return o.IsChest() && o.Selectable
			case "shrine":
				return o.IsShrine() && o.Selectable
			case "wp":
				return o.IsWaypoint()
			case "portal":
				return o.IsPortal() || o.IsRedPortal()
			case "id":
				return int(o.Name) == wantID
			}
			return false
		}
		groundNear := func(d game.Data, pos data.Position, r int) int {
			n := 0
			for _, it := range d.Inventory.ByLocation(item.LocationGround) {
				if chebyshev(pos, it.Position) <= r {
					n++
				}
			}
			return n
		}
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			areaBefore := d.PlayerUnit.Area

			if kind == "entrance" {
				if len(d.Entrances) == 0 {
					// Entrances appear when their room loads — wander until one does.
					wanderTick := int(time.Until(deadline).Seconds())
					dir := (wanderTick / 4) % 8
					angle := float64(dir) / 8.0 * 2 * math.Pi
					walkToHold(cx+int(300*math.Cos(angle)), cy+int(140*math.Sin(angle)), 250)
					continue
				}
				e := d.Entrances[0]
				for _, cand := range d.Entrances {
					if chebyshev(me, cand.Position) < chebyshev(me, e.Position) {
						e = cand
					}
				}
				dist := chebyshev(me, e.Position)
				if dist > 10 {
					sx, sy := screenPointToward(me, e.Position.X-me.X, e.Position.Y-me.Y)
					walkToHold(sx, sy, 250)
					continue
				}
				logger.Info("interact: at entrance", "name", int(e.Name), "dist", dist,
					"pos", fmt.Sprintf("(%d,%d)", e.Position.X, e.Position.Y))
				clicked := hoverPickClick(e.Position, e.ID)
				if !clicked {
					// No hover registered — blind reliable-click at the predicted point and see.
					sx, sy := gameToScreen(gr, me.X, me.Y, e.Position.X, e.Position.Y)
					logger.Info("interact: entrance hover never fired — blind interactClick", "screen", fmt.Sprintf("(%d,%d)", sx, sy))
					interactClick(sx, sy)
				}
				for w := 0; w < 40; w++ {
					time.Sleep(200 * time.Millisecond)
					d2 := gr.GetData()
					if d2.PlayerUnit.Area != areaBefore {
						logger.Info("interact: AREA CHANGED — entrance works", "hoverClicked", clicked,
							"from", int(areaBefore), "to", int(d2.PlayerUnit.Area),
							"pos", fmt.Sprintf("(%d,%d)", d2.PlayerUnit.Position.X, d2.PlayerUnit.Position.Y))
						return
					}
					if w%5 == 4 {
						logger.Info("interact: waiting for area change", "playerPos",
							fmt.Sprintf("(%d,%d)", d2.PlayerUnit.Position.X, d2.PlayerUnit.Position.Y),
							"entrancePos", fmt.Sprintf("(%d,%d)", e.Position.X, e.Position.Y),
							"dist", chebyshev(d2.PlayerUnit.Position, e.Position),
							"mode", int(d2.PlayerUnit.Mode))
					}
				}
				logger.Warn("interact: clicked entrance but area never changed", "hoverClicked", clicked)
				return
			}

			var tgt *data.Object
			best := 1 << 30
			for i := range d.Objects {
				o := &d.Objects[i]
				if matchObj(*o) {
					if dist := chebyshev(me, o.Position); dist < best {
						best, tgt = dist, o
					}
				}
			}
			if tgt == nil {
				logger.Error("interact: no matching object in memory range — walk nearer or check -objprobe", "kind", kind)
				return
			}
			if best > 8 {
				sx, sy := screenPointToward(me, tgt.Position.X-me.X, tgt.Position.Y-me.Y)
				walkToHold(sx, sy, 250)
				continue
			}
			name := tgt.Desc().Name
			logger.Info("interact: engaging", "kind", kind, "name", name, "objID", int(tgt.Name),
				"unitID", int(tgt.ID), "dist", best,
				"modeBefore", int(tgt.Mode), "selectableBefore", tgt.Selectable,
				"menusBefore", fmt.Sprintf("%+v", d.OpenMenus))
			groundBefore := groundNear(d, tgt.Position, 15)
			clicked := hoverPickClick(tgt.Position, tgt.ID)
			time.Sleep(1200 * time.Millisecond)
			d2 := gr.GetData()
			var after *data.Object
			for i := range d2.Objects {
				if d2.Objects[i].ID == tgt.ID {
					after = &d2.Objects[i]
					break
				}
			}
			if after != nil {
				logger.Info("interact: RESULT", "hoverClicked", clicked,
					"mode", fmt.Sprintf("%d->%d", int(tgt.Mode), int(after.Mode)),
					"selectable", fmt.Sprintf("%v->%v", tgt.Selectable, after.Selectable),
					"groundItemsNear", fmt.Sprintf("%d->%d", groundBefore, groundNear(d2, tgt.Position, 15)),
					"menusAfter", fmt.Sprintf("%+v", d2.OpenMenus),
					"area", fmt.Sprintf("%d->%d", int(areaBefore), int(d2.PlayerUnit.Area)),
					"playerStates", len(d2.PlayerUnit.States))
			} else {
				logger.Info("interact: RESULT (object gone from list)", "hoverClicked", clicked,
					"groundItemsNear", fmt.Sprintf("%d->%d", groundBefore, groundNear(d2, tgt.Position, 15)),
					"menusAfter", fmt.Sprintf("%+v", d2.OpenMenus))
			}
			return
		}
		logger.Error("interact: 90s deadline hit while approaching")
		return
	}

	// -uisnap: open a panel by hotkey, screenshot it, close it. The raw material for mapping
	// panel click coordinates (skill tree, character screen, inventory).
	if *uiSnap != "" {
		d := gr.GetData()
		sp, _ := d.PlayerUnit.Stats.FindStat(stat.SkillPoints, 0)
		stp, _ := d.PlayerUnit.Stats.FindStat(stat.StatPoints, 0)
		logger.Info("uisnap: before", "skillPoints", sp.Value, "statPoints", stp.Value)
		hid.PressKey(hid.GetASCIICode(*uiSnap))
		time.Sleep(800 * time.Millisecond)
		if f, err := os.Create(shotPath("uisnap.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("uisnap: saved", "path", shotPath("uisnap.png"))
		}
		time.Sleep(200 * time.Millisecond)
		hid.PressKey(hid.GetASCIICode("esc"))
		return
	}

	// -movelab: the executor bake-off. Same course, three locomotion modes, hard numbers.
	// This decides HOW the mover walks — measured, not argued.
	if *moveLab != "" {
		if *moveKey == "" {
			logger.Error("-movelab needs -move e")
			return
		}
		lg, nRooms, err := gr.BuildLiveGrid()
		if err != nil {
			logger.Error("movelab: live grid failed", "err", err)
			return
		}
		logger.Info("movelab: grid", "rooms", nRooms, "origin", fmt.Sprintf("(%d,%d)", lg.OffsetX, lg.OffsetY))
		losClear := func(g *game.Grid, a, b data.Position) bool {
			steps := max(abs(b.X-a.X), abs(b.Y-a.Y))
			if steps == 0 {
				return true
			}
			for i := 0; i <= steps; i++ {
				p := data.Position{
					X: a.X + (b.X-a.X)*i/steps,
					Y: a.Y + (b.Y-a.Y)*i/steps,
				}
				if !g.IsWalkable(p) {
					return false
				}
			}
			return true
		}
		start := gr.GetData().PlayerUnit.Position
		// Course: nearest walkable cells to a square around the start. Legs are ~55 subtiles,
		// picked on the LIVE grid so all modes chase identical targets.
		nearWalk := func(want data.Position) data.Position {
			best, bp := 1<<30, want
			for y := 0; y < lg.Height; y += 2 {
				for x := 0; x < lg.Width; x += 2 {
					if lg.CollisionGrid[y][x] != game.CollisionTypeWalkable {
						continue
					}
					w := data.Position{X: x + lg.OffsetX, Y: y + lg.OffsetY}
					if dd := chebyshev(w, want); dd < best {
						best, bp = dd, w
					}
				}
			}
			return bp
		}
		course := []data.Position{
			nearWalk(data.Position{X: start.X + 55, Y: start.Y}),
			nearWalk(data.Position{X: start.X + 55, Y: start.Y + 55}),
			nearWalk(data.Position{X: start.X, Y: start.Y + 55}),
			nearWalk(start),
		}
		logger.Info("movelab: course", "start", fmt.Sprintf("(%d,%d)", start.X, start.Y),
			"wp1", fmt.Sprintf("(%d,%d)", course[0].X, course[0].Y),
			"wp2", fmt.Sprintf("(%d,%d)", course[1].X, course[1].Y),
			"wp3", fmt.Sprintf("(%d,%d)", course[2].X, course[2].Y),
			"wp4", fmt.Sprintf("(%d,%d)", course[3].X, course[3].Y))

		runLeg := func(mode string, dest data.Position) (secs float64, stallSecs float64, walked int, arrived bool) {
			navi := NewNavigator(lg)
			legStart := time.Now()
			last := gr.GetData().PlayerUnit.Position
			lastMoveAt := time.Now()
			lastClick := time.Time{}
			for time.Since(legStart) < 45*time.Second {
				me := gr.GetData().PlayerUnit.Position
				if d := chebyshev(me, last); d > 0 {
					walked += d
					if d > 1 {
						lastMoveAt = time.Now()
					}
				}
				last = me
				if chebyshev(me, dest) <= 5 {
					arrived = true
					break
				}
				if time.Since(lastMoveAt) > 700*time.Millisecond {
					stallSecs += 0.35
				}
				switch mode {
				case "carrot":
					if !navi.havePlan && !navi.BuildPlan(me, dest, time.Now()) {
						sx, sy := screenPointToward(me, dest.X-me.X, dest.Y-me.Y)
						walkToHold(sx, sy, 250)
						continue
					}
					step := navi.Step(me, time.Now())
					if step.Arrived || step.Diverged {
						navi.havePlan = false
						continue
					}
					hold := step.HoldMs
					if hold <= 0 {
						hold = 200
					}
					sx, sy := screenPointToward(me, step.Target.X-me.X, step.Target.Y-me.Y)
					walkToHold(sx, sy, hold)
				case "lookahead":
					// Aim at the FARTHEST point toward dest with clear line of sight — long
					// smooth segments instead of near-waypoint jitter.
					tgt := dest
					if !losClear(lg, me, dest) {
						if !navi.havePlan && !navi.BuildPlan(me, dest, time.Now()) {
							sx, sy := screenPointToward(me, dest.X-me.X, dest.Y-me.Y)
							walkToHold(sx, sy, 250)
							continue
						}
						step := navi.Step(me, time.Now())
						if step.Arrived || step.Diverged {
							navi.havePlan = false
							continue
						}
						// walk the plan but target the farthest planned point we can SEE
						far := step.Target
						for i := len(navi.pts) - 1; i >= 0; i-- {
							if losClear(lg, me, navi.pts[i]) {
								far = navi.pts[i]
								break
							}
						}
						tgt = far
					}
					sx, sy := screenPointToward(me, tgt.X-me.X, tgt.Y-me.Y)
					walkToHold(sx, sy, 300)
				case "click":
					// The game's own pathfinder: a reliable (VK-override) left-click on a far
					// visible point; re-click sparingly and let D2R walk.
					if time.Since(lastClick) < 1200*time.Millisecond {
						time.Sleep(120 * time.Millisecond)
						continue
					}
					tgt := dest
					if !losClear(lg, me, dest) {
						if !navi.havePlan && !navi.BuildPlan(me, dest, time.Now()) {
							tgt = dest // click blind at it; the game may still path
						} else {
							step := navi.Step(me, time.Now())
							_ = step
							far := dest
							for i := len(navi.pts) - 1; i >= 0; i-- {
								if losClear(lg, me, navi.pts[i]) {
									far = navi.pts[i]
									break
								}
							}
							tgt = far
						}
					}
					sx, sy := screenPointToward(me, tgt.X-me.X, tgt.Y-me.Y)
					interactClick(sx, sy)
					lastClick = time.Now()
				}
				time.Sleep(60 * time.Millisecond)
			}
			return time.Since(legStart).Seconds(), stallSecs, walked, arrived
		}

		modes := []string{"carrot", "lookahead", "click"}
		if *moveLab != "all" {
			modes = []string{*moveLab}
		}
		for _, m := range modes {
			var tSecs, tStall float64
			var tWalk, tStraight, arrivals int
			pos := gr.GetData().PlayerUnit.Position
			for li, wp := range course {
				straight := chebyshev(pos, wp)
				secs, stall, walked, ok := runLeg(m, wp)
				logger.Info("movelab: leg", "mode", m, "leg", li, "secs", fmt.Sprintf("%.1f", secs),
					"stall", fmt.Sprintf("%.1f", stall), "walked", walked, "straight", straight, "arrived", ok)
				tSecs += secs
				tStall += stall
				tWalk += walked
				tStraight += straight
				if ok {
					arrivals++
				}
				pos = gr.GetData().PlayerUnit.Position
			}
			eff := 0.0
			if tWalk > 0 {
				eff = float64(tStraight) / float64(tWalk)
			}
			logger.Info("movelab: MODE SUMMARY", "mode", m, "totalSecs", fmt.Sprintf("%.1f", tSecs),
				"stallSecs", fmt.Sprintf("%.1f", tStall), "arrivals", fmt.Sprintf("%d/%d", arrivals, len(course)),
				"pathEfficiency", fmt.Sprintf("%.2f", eff))
		}
		return
	}

	// stat helpers: current attribute value and the max requirement across carried gear.
	statVal := func(d game.Data, id stat.ID) int {
		if v, ok := d.PlayerUnit.Stats.FindStat(id, 0); ok {
			return v.Value
		}
		if v, ok := d.PlayerUnit.BaseStats.FindStat(id, 0); ok {
			return v.Value
		}
		return 0
	}
	gearReq := func(d game.Data) (reqStr, reqDex int) {
		for _, loc := range []item.LocationType{item.LocationEquipped, item.LocationInventory} {
			for _, it := range d.Inventory.ByLocation(loc) {
				if rs := it.Desc().RequiredStrength; rs > reqStr {
					reqStr = rs
				}
				if rd := it.Desc().RequiredDexterity; rd > reqDex {
					reqDex = rd
				}
			}
		}
		return
	}

	// -gearoracle: the oracle speaks. Knowledge from the mod's own tables, valuation for THIS
	// build, feasibility from what he actually carries. Read-only; execution comes with the
	// vendor/cube UI work.
	if *gearOracle {
		tabs, err := gear.LoadTables(gear.DefaultExcelDir)
		if err != nil {
			logger.Error("gearoracle: table load failed", "err", err)
			return
		}
		ps := tabs.Stats
		logger.Info("gearoracle: tables", "items", ps.ItemsLoaded, "recipes", ps.RecipesParsed, "skipped", ps.RecipesSkipped)
		d := gr.GetData()
		eq := d.Inventory.ByLocation(item.LocationEquipped)
		inv := d.Inventory.ByLocation(item.LocationInventory)
		gold := statVal(d, stat.Gold)
		w := gear.DefaultSummonerWeights()
		for _, it := range eq {
			sc, reasons := tabs.ScoreItem(it, w)
			logger.Info("gearoracle: equipped", "name", string(it.Name), "score", fmt.Sprintf("%.1f", sc),
				"why", strings.Join(reasons, "; "))
		}
		sugs := tabs.FeasibleUpgrades(eq, inv, gold, w)
		if len(sugs) == 0 {
			logger.Info("gearoracle: no feasible one-step upgrades right now")
		}
		for i, sg := range sugs {
			if i >= 12 {
				break
			}
			logger.Info("gearoracle: SUGGEST", "rank", i+1, "kind", sg.Kind,
				"gain", fmt.Sprintf("%+.1f", sg.Gain), "gamble", sg.Gamble, "what", sg.What)
		}
		return
	}

	// -statsnap: character-panel calibration + a read of what allocation WOULD do.
	if *statSnap {
		d := gr.GetData()
		sp := statVal(d, stat.StatPoints)
		rs, rd := gearReq(d)
		logger.Info("statsnap", "banked", sp,
			"str", statVal(d, stat.Strength), "dex", statVal(d, stat.Dexterity),
			"vita", statVal(d, stat.Vitality), "ene", statVal(d, stat.Energy),
			"gearReqStr", rs, "gearReqDex", rd)
		moveStop()
		hid.PressKey(hid.GetASCIICode("c"))
		time.Sleep(800 * time.Millisecond)
		if f, err := os.Create(shotPath("statsnap.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("statsnap: saved", "path", shotPath("statsnap.png"))
		}
		time.Sleep(150 * time.Millisecond)
		hid.PressKey(hid.GetASCIICode("c")) // toggle closed (esc opens the pause menu)
		return
	}

	// -statalloc: spend banked points. Buffer str/dex ~10 over gear reqs, rest to vitality.
	if *statAlloc != "" {
		var sbx, sby, dbx, dby, vbx, vby int
		if _, err := fmt.Sscanf(*statAlloc, "%d,%d,%d,%d,%d,%d", &sbx, &sby, &dbx, &dby, &vbx, &vby); err != nil {
			logger.Error("statalloc: want 'strX,strY,dexX,dexY,vitX,vitY'", "got", *statAlloc)
			return
		}
		d := gr.GetData()
		sp := statVal(d, stat.StatPoints)
		if sp <= 0 {
			logger.Info("statalloc: no banked points")
			return
		}
		rs, rd := gearReq(d)
		const buffer = 10
		hid.PressKey(hid.GetASCIICode("c"))
		time.Sleep(800 * time.Millisecond)
		spent := 0
		for i := 0; i < sp; i++ {
			d = gr.GetData()
			var bx, by int
			var which string
			switch {
			case statVal(d, stat.Strength) < rs+buffer:
				bx, by, which = sbx, sby, "str"
			case statVal(d, stat.Dexterity) < rd+buffer:
				bx, by, which = dbx, dby, "dex"
			default:
				bx, by, which = vbx, vby, "vita"
			}
			before := statVal(d, stat.StatPoints)
			uiClick(bx, by)
			time.Sleep(250 * time.Millisecond)
			if statVal(gr.GetData(), stat.StatPoints) < before {
				spent++
			} else {
				logger.Warn("statalloc: click did not spend — stopping", "which", which, "at", fmt.Sprintf("(%d,%d)", bx, by))
				break
			}
		}
		d = gr.GetData()
		logger.Info("statalloc: done", "spent", spent, "str", statVal(d, stat.Strength),
			"dex", statVal(d, stat.Dexterity), "vita", statVal(d, stat.Vitality),
			"banked", statVal(d, stat.StatPoints))
		time.Sleep(150 * time.Millisecond)
		hid.PressKey(hid.GetASCIICode("c"))
		return
	}

	// -gearprobe: READ-ONLY gear evaluation. Unidentified items hide their magic affixes even
	// in memory, so the honest pipeline is ID -> read -> evaluate -> equip/stash; this probe is
	// the "read" stage plus the caveats that gate the others.
	if *gearProbe {
		d := gr.GetData()
		dumpItem := func(loc string, it data.Item) {
			logger.Info("gearprobe: item", "loc", loc, "name", string(it.Name),
				"quality", it.Quality.ToString(), "identified", it.Identified,
				"ethereal", it.Ethereal, "levelReq", it.LevelReq,
				"idName", it.IdentifiedName)
			for _, st := range it.Stats {
				logger.Info("gearprobe: stat", "item", string(it.Name), "stat", st.ID.String(),
					"value", st.Value, "layer", st.Layer)
			}
			for _, st := range it.BaseStats {
				logger.Info("gearprobe: basestat", "item", string(it.Name), "stat", st.ID.String(),
					"value", st.Value)
			}
		}
		for _, it := range d.Inventory.ByLocation(item.LocationEquipped) {
			dumpItem("equipped", it)
		}
		for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
			dumpItem("inventory", it)
		}
		// Skill-granting gear the bindings depend on: any skill in the char's list at hard
		// level 0 exists only via an item — swapping that item silently breaks its hotkey.
		for id, pts := range d.PlayerUnit.Skills {
			if pts.Level == 0 && id != 0 {
				logger.Info("gearprobe: ITEM-GRANTED skill — the granting item is load-bearing",
					"skill", int(id), "name", skill.SkillNames[id])
			}
		}
		return
	}

	// -press: one key, nothing else. For closing leaked menus surgically.
	if *pressOnly != "" {
		hid.PressKey(hid.GetASCIICode(*pressOnly))
		time.Sleep(400 * time.Millisecond)
		if f, err := os.Create(shotPath("press.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		logger.Info("press: done", "key", *pressOnly, "shot", shotPath("press.png"))
		return
	}

	// -bindskill: open the HUD skill selector, hover a skill, press a hotkey to BIND it.
	// Bindings persist across deaths (unlike the right-skill selection, which resets to Attack),
	// so a bound key + the -rabies press-before-bite mechanism survives the whole death cycle.
	if *bindSkill != "" {
		var sx, sy, hx, hy int
		var key string
		if _, err := fmt.Sscanf(*bindSkill, "%d,%d,%d,%d,%s", &sx, &sy, &hx, &hy, &key); err != nil {
			logger.Error("bindskill: want 'slotX,slotY,skillX,skillY,key'", "got", *bindSkill)
			return
		}
		before := gr.GetData().PlayerUnit.RightSkill
		uiClick(sx, sy) // open the selector
		time.Sleep(700 * time.Millisecond)
		if key == "click" {
			uiClick(hx, hy) // select the skill by clicking it in the popup
			time.Sleep(500 * time.Millisecond)
		} else {
			aimPanel(hx, hy) // hover the skill — PANEL cursor space (unscaled), not world space
			hid.MouseMoveClient(hx, hy)
			time.Sleep(500 * time.Millisecond)
			hid.PressKey(hid.GetASCIICode(key))
			time.Sleep(500 * time.Millisecond)
		}
		if f, err := os.Create(shotPath("bindskill.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		if key != "click" {
			time.Sleep(300 * time.Millisecond)
			hid.PressKey(hid.GetASCIICode(key)) // press the hotkey — selects the skill if bound
			time.Sleep(400 * time.Millisecond)
		}
		after := gr.GetData().PlayerUnit.RightSkill
		logger.Info("bindskill: result", "rightSkillBefore", int(before), "rightSkillAfter", int(after),
			"boundAndSelected", after != before, "shot", shotPath("bindskill.png"))
		return
	}

	// -paneltest: open the skill tree, click one point, screenshot the outcome. The screenshot
	// is the truth signal for calibrating the panel click space on this machine (panels recover
	// the UNSCALED client offset per the desktop measurement — verify, don't assume).
	if *panelTest != "" {
		var px, py int
		if _, err := fmt.Sscanf(*panelTest, "%d,%d", &px, &py); err != nil {
			logger.Error("paneltest: want 'x,y'", "got", *panelTest)
			return
		}
		d := gr.GetData()
		sp, spOK := d.PlayerUnit.Stats.FindStat(stat.SkillPoints, 0)
		if !spOK {
			sp, _ = d.PlayerUnit.BaseStats.FindStat(stat.SkillPoints, 0)
		}
		logger.Info("paneltest: before", "skillPoints", sp.Value,
			"rightSkill", int(d.PlayerUnit.RightSkill),
			"raiseSkeletonLvl", d.PlayerUnit.Skills[skill.RaiseSkeleton].Level)
		if *panelKey != "none" {
			hid.PressKey(hid.GetASCIICode(*panelKey))
		}
		time.Sleep(900 * time.Millisecond)
		uiClick(px, py)
		time.Sleep(700 * time.Millisecond)
		if f, err := os.Create(shotPath("paneltest.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		d2 := gr.GetData()
		sp2, ok2 := d2.PlayerUnit.Stats.FindStat(stat.SkillPoints, 0)
		if !ok2 {
			sp2, _ = d2.PlayerUnit.BaseStats.FindStat(stat.SkillPoints, 0)
		}
		logger.Info("paneltest: after", "skillPoints", sp2.Value,
			"rightSkill", int(d2.PlayerUnit.RightSkill),
			"raiseSkeletonLvl", d2.PlayerUnit.Skills[skill.RaiseSkeleton].Level,
			"shot", shotPath("paneltest.png"))
		return
	}

	// -dietest: the death interaction, end to end, on purpose. SOFTCORE ONLY. Stages:
	// leave-town (east-biased wander) → aggro (walk into a monster, no potions, no flee) →
	// dead (observe everything, then learn which input respawns: esc/enter) → corpse-run
	// (walk back to the recorded death spot, hover the corpse via Corpse.IsHovered, click,
	// verify Corpse.Found flips). Each stage logs its evidence — the goal is to LEARN the
	// signals so the farm loop can automate death recovery.
	if *dieTest {
		if *moveKey == "" {
			logger.Error("-dietest needs -move e")
			return
		}
		d0 := gr.GetData()
		logger.Info("dietest: START", "char", d0.PlayerUnit.Name, "area", int(d0.PlayerUnit.Area),
			"hp", d0.PlayerUnit.HPPercent(), "corpseFoundNow", d0.Corpse.Found)
		stage := "leave-town"
		deadline := time.Now().Add(6 * time.Minute)
		dirBias := []int{0, 1, 7, 0, 2, 6, 0, 1, 7, 3, 5, 0} // east-heavy blind wander
		tick := 0
		var deathPos data.Position
		var deathArea area.ID
		wander := func() {
			tick++
			dir := dirBias[tick%len(dirBias)]
			angle := float64(dir) / 8.0 * 2 * math.Pi
			walkToHold(cx+int(300*math.Cos(angle)), cy+int(140*math.Sin(angle)), 300)
		}
		for time.Now().Before(deadline) {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			hp := d.PlayerUnit.HPPercent()
			pmode := d.PlayerUnit.Mode
			dead := pmode == mode.Death || pmode == mode.Dead || hp <= 0
			switch stage {
			case "leave-town":
				if dead {
					stage = "dead"
					deathPos, deathArea = me, d.PlayerUnit.Area
					continue
				}
				if !d.PlayerUnit.Area.IsTown() {
					logger.Info("dietest: out of town", "area", int(d.PlayerUnit.Area))
					stage = "aggro"
					continue
				}
				wander()
			case "aggro":
				if dead {
					stage = "dead"
					deathPos, deathArea = me, d.PlayerUnit.Area
					continue
				}
				var mon *data.Monster
				best := 1 << 30
				for i := range d.Monsters {
					m := &d.Monsters[i]
					if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead || m.IsGoodNPC() || m.IsPet() || m.IsMerc() {
						continue
					}
					if dist := chebyshev(me, m.Position); dist < best {
						best, mon = dist, m
					}
				}
				if mon == nil || best > 60 {
					wander()
					continue
				}
				if best > 3 {
					sx, sy := screenPointToward(me, mon.Position.X-me.X, mon.Position.Y-me.Y)
					walkToHold(sx, sy, 250)
					continue
				}
				logger.Info("dietest: standing in the pack, waiting to die", "hp", hp, "monDist", best)
				time.Sleep(400 * time.Millisecond)
			case "dead":
				logger.Info("dietest: DEAD — observing", "pos", fmt.Sprintf("(%d,%d)", deathPos.X, deathPos.Y),
					"deathArea", int(deathArea), "mode", int(pmode), "hp", hp,
					"corpseFound", d.Corpse.Found,
					"corpsePos", fmt.Sprintf("(%d,%d)", d.Corpse.Position.X, d.Corpse.Position.Y),
					"menus", fmt.Sprintf("%+v", d.OpenMenus))
				time.Sleep(2500 * time.Millisecond)
				respawned := ""
				for _, key := range []string{"esc", "enter", "esc"} {
					hid.PressKey(hid.GetASCIICode(key))
					logger.Info("dietest: respawn input sent", "key", key)
					for w := 0; w < 25; w++ {
						time.Sleep(200 * time.Millisecond)
						d2 := gr.GetData()
						m2 := d2.PlayerUnit.Mode
						if d2.PlayerUnit.Area.IsTown() && d2.PlayerUnit.HPPercent() > 0 &&
							m2 != mode.Death && m2 != mode.Dead {
							respawned = key
							break
						}
					}
					if respawned != "" {
						break
					}
				}
				if respawned == "" {
					d2 := gr.GetData()
					logger.Error("dietest: NO respawn input worked — state dump",
						"mode", int(d2.PlayerUnit.Mode), "hp", d2.PlayerUnit.HPPercent(),
						"area", int(d2.PlayerUnit.Area), "menus", fmt.Sprintf("%+v", d2.OpenMenus))
					return
				}
				d2 := gr.GetData()
				logger.Info("dietest: RESPAWNED in town", "via", respawned,
					"pos", fmt.Sprintf("(%d,%d)", d2.PlayerUnit.Position.X, d2.PlayerUnit.Position.Y),
					"corpseFoundInTown", d2.Corpse.Found,
					"corpsePos", fmt.Sprintf("(%d,%d)", d2.Corpse.Position.X, d2.Corpse.Position.Y))
				stage = "corpse-run"
			case "corpse-run":
				if !d.Corpse.Found {
					// Corpse not visible from here — walk back toward where we died (blind).
					wander()
					continue
				}
				cd := chebyshev(me, d.Corpse.Position)
				if cd > 6 {
					sx, sy := screenPointToward(me, d.Corpse.Position.X-me.X, d.Corpse.Position.Y-me.Y)
					walkToHold(sx, sy, 250)
					continue
				}
				logger.Info("dietest: at corpse", "dist", cd, "notInteractable", d.Corpse.StateNotInteractable(),
					"states", len(d.Corpse.States))
				bx, by := gameToScreen(gr, me.X, me.Y, d.Corpse.Position.X, d.Corpse.Position.Y)
				recovered := false
				for dy := -60; dy <= 16 && !recovered; dy += 6 {
					for _, dx := range []int{0, -8, 8, -16, 16, -26, 26} {
						px, py := bx+dx, by+dy
						hid.AimPhysical(px, py)
						time.Sleep(55 * time.Millisecond)
						d3 := gr.GetData()
						if !d3.Corpse.IsHovered {
							continue
						}
						hid.AimPhysical(px, py)
						time.Sleep(70 * time.Millisecond)
						if !gr.GetData().Corpse.IsHovered {
							continue
						}
						logger.Info("dietest: corpse hovered — clicking", "at", fmt.Sprintf("(%d,%d)", px, py))
						interactClick(px, py)
						for w := 0; w < 20; w++ {
							time.Sleep(150 * time.Millisecond)
							if !gr.GetData().Corpse.Found {
								recovered = true
								break
							}
						}
						break
					}
				}
				d4 := gr.GetData()
				logger.Info("dietest: corpse recovery RESULT", "recovered", recovered,
					"corpseFoundAfter", d4.Corpse.Found, "hp", d4.PlayerUnit.HPPercent())
				return
			}
		}
		logger.Error("dietest: 6-minute deadline hit", "stage", stage)
		return
	}

	// -aimprobe: measure the WORLD aim mapping against the game's own hit-test. gameToScreen
	// predicts the target's client point p; we aim at t*p for t=0.5..1.7 and read HoverData.
	// If hover fires at t≈1.0 the mapping is right; t≈displayScale means the client offset must
	// be SCALED for world aim (the question the desktop's panel-only proof left open). The
	// target's position and the prediction are re-read every step (zombies shamble).
	if *aimProbe {
		nearestMon := func() (*data.Monster, data.Position, int) {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			var tgt *data.Monster
			best := 999
			for i := range d.Monsters {
				m := &d.Monsters[i]
				if dist := chebyshev(me, m.Position); dist < best {
					best, tgt = dist, m
				}
			}
			return tgt, me, best
		}
		// Approach phase: get CLOSE (≤12 subtiles) so the target is solidly on-screen — at 24+
		// subtiles the prediction lands at/off the screen edge and hover can never fire.
		// Steering is direction-only, so the suspect absolute mapping can't invalidate it.
		if *moveKey == "" {
			logger.Error("-aimprobe needs -move e (it walks into range first)")
			return
		}
		approach := func(within int) bool {
			for i := 0; i < 60; i++ {
				tgt, me, best := nearestMon()
				if tgt == nil {
					logger.Error("aimprobe: no monsters in area")
					return false
				}
				if best <= within {
					return true
				}
				// Steer via a CENTERED carrot in the target's direction — the raw gameToScreen of
				// a far target lands at the screen edge/HUD where the cursor is dead (measured:
				// zero net movement), while near-center carrots steer perfectly (-movetest).
				sx, sy := gameToScreen(gr, me.X, me.Y, tgt.Position.X, tgt.Position.Y)
				dx, dy := float64(sx-cx), float64(sy-cy)
				n := math.Hypot(dx, dy)
				if n < 1 {
					n = 1
				}
				walkToHold(cx+int(dx/n*280), cy+int(dy/n*140), 250)
			}
			logger.Error("aimprobe: could not close in")
			return false
		}
		hits := 0
		for pass := 0; pass < 2; pass++ {
			if !approach(12) {
				return
			}
			for t := 0.50; t <= 1.70; t += 0.05 {
				d := gr.GetData()
				me := d.PlayerUnit.Position
				var tgt *data.Monster
				best := 999
				for i := range d.Monsters {
					m := &d.Monsters[i]
					if dist := chebyshev(me, m.Position); dist < best {
						best, tgt = dist, m
					}
				}
				if tgt == nil || best > 20 {
					break // drifted out of hover range — outer pass loop re-approaches
				}
				sx, sy := gameToScreen(gr, me.X, me.Y, tgt.Position.X, tgt.Position.Y)
				ax, ay := int(float64(sx)*t), int(float64(sy)*t)
				hid.AimPhysical(ax, ay)
				time.Sleep(60 * time.Millisecond)
				d2 := gr.GetData()
				monHov := false
				for i := range d2.Monsters {
					if d2.Monsters[i].UnitID == tgt.UnitID && d2.Monsters[i].IsHovered {
						monHov = true
					}
				}
				if d2.HoverData.IsHovered || monHov {
					hits++
					logger.Info("aimprobe HIT", "t", fmt.Sprintf("%.2f", t),
						"predicted", fmt.Sprintf("(%d,%d)", sx, sy), "aimed", fmt.Sprintf("(%d,%d)", ax, ay),
						"hoverUnit", int(d2.HoverData.UnitID), "targetUnit", int(tgt.UnitID),
						"targetHovered", monHov, "dist", best)
				} else {
					logger.Info("aimprobe miss", "t", fmt.Sprintf("%.2f", t),
						"predicted", fmt.Sprintf("(%d,%d)", sx, sy), "aimed", fmt.Sprintf("(%d,%d)", ax, ay), "dist", best)
				}
			}
		}
		logger.Info("aimprobe: DONE", "hits", hits)
		return
	}

	// -hovergrid: sweep the cursor over a screen grid; log every point where ANYTHING is hovered.
	// Definitively answers: does unit/NPC hover fire at all (background), and at what screen point?
	if *hoverGrid {
		if *moveKey == "" {
			logger.Error("-hovergrid needs -move e")
			return
		}
		_ = gr.FetchMapData()
		hits := 0
		for y := 70; y <= 410; y += 24 {
			for x := 110; x <= 750; x += 24 {
				hid.AimPhysical(x, y)
				time.Sleep(35 * time.Millisecond)
				d := gr.GetData()
				hd := d.HoverData
				monHov, monName := data.UnitID(0), -1
				for _, m := range d.Monsters {
					if m.IsHovered {
						monHov, monName = m.UnitID, int(m.Name)
						break
					}
				}
				if hd.IsHovered || monName != -1 {
					hits++
					logger.Info("hovergrid HIT", "screen", fmt.Sprintf("(%d,%d)", x, y),
						"hoverData", fmt.Sprintf("hovered=%v id=%d type=%d", hd.IsHovered, hd.UnitID, hd.UnitType),
						"monsterHovered", fmt.Sprintf("id=%d name=%d", monHov, monName))
				}
			}
		}
		logger.Info("hovergrid: DONE", "hits", hits)
		return
	}

	// -npcprobe: TOWN DISCOVERY. Walk to the Nth-nearest NPC and interact (hoverPickClick), then
	// report which menu opened (NPCShop = vendor, NPCInteract = dialog, Stash = stash) + a
	// screenshot. Identifies vendors/stash live since the mod remaps NPC IDs.
	if *npcProbe != 0 {
		if *moveKey == "" {
			logger.Error("-npcprobe needs -move e")
			return
		}
		_ = gr.FetchMapData()
		d := gr.GetData()
		me := d.PlayerUnit.Position
		type cand struct {
			id   data.UnitID
			name int
			pos  data.Position
			dist int
		}
		var cands []cand
		for _, m := range d.Monsters {
			cands = append(cands, cand{m.UnitID, int(m.Name), m.Position, chebyshev(me, m.Position)})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		// Always list the nearest units so town NPCs (stationary, dist ~15-40) are distinguishable
		// from her summons/merc (name 421/424/425, hugging her at dist <10).
		for i := 0; i < len(cands) && i < 14; i++ {
			logger.Info("npcprobe: unit", "name", cands[i].name, "unitID", cands[i].id,
				"dist", cands[i].dist, "pos", fmt.Sprintf("(%d,%d)", cands[i].pos.X, cands[i].pos.Y))
		}
		// -npcprobe <nameID>: interact with the nearest unit whose Name == nameID.
		var t cand
		have := false
		for _, c := range cands {
			if c.name == *npcProbe {
				t, have = c, true
				break
			}
		}
		if !have {
			// Not in the live unit table — the NPC's rooms aren't loaded. The MAP data knows
			// every town NPC's home spot: walk there (rooms stream in en route), then re-scan.
			var home data.Position
			for _, n := range d.NPCs {
				if int(n.ID) == *npcProbe && len(n.Positions) > 0 {
					home = n.Positions[0]
					break
				}
			}
			if home.X == 0 {
				logger.Error("npcprobe: not in live units and not in map NPCs — pick from the list above", "want", *npcProbe)
				return
			}
			logger.Info("npcprobe: walking to map-data home", "want", *npcProbe,
				"home", fmt.Sprintf("(%d,%d)", home.X, home.Y), "dist", chebyshev(me, home))
			for i := 0; i < 120; i++ {
				me = gr.GetData().PlayerUnit.Position
				if chebyshev(me, home) <= 12 {
					break
				}
				sx, sy := screenPointToward(me, home.X-me.X, home.Y-me.Y)
				walkToHold(sx, sy, 150)
				time.Sleep(150 * time.Millisecond)
			}
			for _, m := range gr.GetData().Monsters {
				if int(m.Name) == *npcProbe {
					t, have = cand{m.UnitID, int(m.Name), m.Position, chebyshev(me, m.Position)}, true
					break
				}
			}
			if !have {
				logger.Error("npcprobe: walked to home but the unit never materialized", "want", *npcProbe)
				return
			}
		}
		logger.Info("npcprobe: target", "npcName", t.name, "unitID", t.id, "dist", t.dist,
			"pos", fmt.Sprintf("(%d,%d)", t.pos.X, t.pos.Y))
		for i := 0; i < 40; i++ {
			me = gr.GetData().PlayerUnit.Position
			if chebyshev(me, t.pos) <= 5 {
				break
			}
			sx, sy := screenPointToward(me, t.pos.X-me.X, t.pos.Y-me.Y)
			walkToHold(sx, sy, 150)
		}
		clicked := interactNPC(t.id)
		time.Sleep(900 * time.Millisecond)
		om := gr.GetData().OpenMenus
		logger.Info("npcprobe: RESULT", "clicked", clicked, "npcName", t.name, "unitID", t.id,
			"NPCShop", om.NPCShop, "NPCInteract", om.NPCInteract, "Stash", om.Stash,
			"Waypoint", om.Waypoint, "area", int(gr.GetData().PlayerUnit.Area))
		shot := shotPath(fmt.Sprintf("npc_name%d.png", t.name))
		if f, err := os.Create(shot); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("npcprobe: screenshot", "path", shot)
		}
		return
	}

	// uiRightClick: right-button twin of uiClick — raw physical panel coords.
	uiRightClick := func(sx, sy int) {
		moveStop()
		aimPanel(sx, sy)
		time.Sleep(150 * time.Millisecond)
		// Shop/inventory right-clicks poll VK_RBUTTON like everything else on this build.
		_ = gi.OverrideGetKeyState(0x02)
		_ = gi.OverrideGetAsyncKeyState(0x02)
		hid.Click(game.RightButton, sx, sy)
		_ = gi.RestoreGetKeyState()
		_ = gi.RestoreGetAsyncKeyState()
		time.Sleep(120 * time.Millisecond)
	}
	// invPixel maps an inventory grid cell to raw physical pixels — measured from uisnap
	// (2026-07-18): grid origin (1292,395), 45px cells, item anchored at its top-left cell.
	invPixel := func(gx, gy int) (int, int) {
		return 1292 + gx*45 + 22, 395 + gy*45 + 22
	}
	// identifyErrand: open inventory, right-click the ID tome, click each unidentified item;
	// closed-loop per item (Identified flag must flip). Panel-space only — no NPC needed.
	identifyErrand := func() int {
		// THE DEAF-RIGHT-CLICK PIVOT: panel right-clicks never land on this build (tome
		// use, shop buy — measured deaf under every override), but WORLD right-clicks work
		// (every golem cast proves it). So ride the tome's GRANTED SKILL instead: -idkey
		// selects Book of Identify as the right skill (user-bound, like F1-F3), a world
		// right-click raises the ID cursor, and the item click is a LEFT panel click —
		// which works. The tome's inventory position stops mattering entirely (the mod
		// renames tomes to ""/Jawbone/Eye, so position was always the fragile part).
		if *idKey == "" {
			return 0
		}
		d := gr.GetData()
		if _, hasBook := d.PlayerUnit.Skills[skill.TomeOfIdentify]; !hasBook {
			logger.Info("identify: Book of Identify skill absent — no ID tome owned?")
			return 0
		}
		var unid []data.Item
		for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
			if !it.Identified {
				unid = append(unid, it)
			}
		}
		if len(unid) == 0 {
			return 0
		}
		moveStop()
		hid.PressKey(hid.GetASCIICode("i"))
		time.Sleep(600 * time.Millisecond)
		done := 0
		for i, it := range unid {
			if i >= 12 { // cap per visit
				break
			}
			hid.PressKey(hid.GetASCIICode(*idKey))
			time.Sleep(200 * time.Millisecond)
			// World right-click on the LEFT half — the inventory panel owns the right.
			hid.Click(game.RightButton, cx/2, cy)
			time.Sleep(350 * time.Millisecond)
			ix, iy := invPixel(it.Position.X, it.Position.Y)
			uiClick(ix, iy)
			time.Sleep(450 * time.Millisecond)
			ok := false
			for _, it2 := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				if it2.Position == it.Position && it2.Identified {
					ok = true
					break
				}
			}
			if ok {
				done++
				logger.Info("identify: item identified", "name", string(it.Name))
			} else {
				logger.Warn("identify: flag did not flip", "name", string(it.Name),
					"cell", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y))
			}
		}
		hid.PressKey(hid.GetASCIICode("i"))
		time.Sleep(300 * time.Millisecond)
		logger.Info("identify: errand done", "identified", done, "candidates", len(unid))
		return done
	}

	// cubePixel maps a Horadric Cube grid cell to raw physical pixels — calibrated from a
	// live open-cube shot (2026-07-18): the mod EXPANDS the cube to 12x8 (~96 cells,
	// vanilla is 12) — more room than the backpack, and it travels with him. Grid anchored
	// (196,237), ~48x47px cells. The 'h' UI binding opens cube AND backpack together.
	cubePixel := func(gx, gy int) (int, int) {
		return 196 + gx*48, 237 + gy*47
	}
	// cubeStash: move keeper-quality items backpack -> cube via left-click pick+place,
	// closed-loop per item. Runs ANYWHERE — backpack pressure stops meaning a town trip.
	cubeStash := func() int {
		if *cubeKey == "" {
			return 0
		}
		d := gr.GetData()
		keeper := func(it data.Item) bool {
			switch it.Quality {
			case item.QualityMagic, item.QualityRare, item.QualitySet, item.QualityUnique, item.QualityCrafted:
			default:
				return false
			}
			n := string(it.Name)
			if strings.Contains(n, "Charm") || strings.Contains(n, "Tome") || strings.Contains(n, "Potion") ||
				strings.Contains(n, "INVALID") || n == "" || n == "Jawbone" || n == "Eye" || n == "Scalp" {
				return false // charms only work FROM the backpack; the rest are the never-touch set
			}
			return it.Identified
		}
		var move []data.Item
		for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
			if keeper(it) {
				move = append(move, it)
			}
		}
		if len(move) == 0 {
			return 0
		}
		occ := map[[2]int]bool{}
		for _, it := range d.Inventory.ByLocation(item.LocationCube) {
			for dx := 0; dx < 2; dx++ {
				for dy := 0; dy < 3; dy++ {
					occ[[2]int{it.Position.X + dx, it.Position.Y + dy}] = true
				}
			}
		}
		nextFree := func() (int, int, bool) {
			for y := 0; y < 8; y++ {
				for x := 0; x < 12; x++ {
					if !occ[[2]int{x, y}] {
						return x, y, true
					}
				}
			}
			return 0, 0, false
		}
		moveStop()
		hid.PressKey(hid.GetASCIICode(*cubeKey))
		time.Sleep(700 * time.Millisecond)
		stashed := 0
		for i, it := range move {
			if i >= 8 {
				break
			}
			fx, fy, okc := nextFree()
			if !okc {
				logger.Info("cube: full")
				break
			}
			ix, iy := invPixel(it.Position.X, it.Position.Y)
			uiClick(ix, iy) // pick from backpack
			time.Sleep(350 * time.Millisecond)
			px, py := cubePixel(fx, fy)
			uiClick(px, py) // place into cube
			time.Sleep(450 * time.Millisecond)
			gone := true
			for _, it2 := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				if it2.Position == it.Position && it2.Name == it.Name {
					gone = false
					break
				}
			}
			if gone {
				for dx := 0; dx < 2; dx++ {
					for dy := 0; dy < 3; dy++ {
						occ[[2]int{fx + dx, fy + dy}] = true
					}
				}
				stashed++
				logger.Info("cube: stashed", "name", string(it.Name), "cell", fmt.Sprintf("(%d,%d)", fx, fy))
			} else {
				// Pick or place missed; if the item rode the cursor, clicking its origin
				// cell puts it back. Stop the pass — coords may be drifting.
				uiClick(ix, iy)
				time.Sleep(300 * time.Millisecond)
				logger.Warn("cube: stash failed — returned item, stopping pass", "name", string(it.Name))
				break
			}
		}
		hid.PressKey(hid.GetASCIICode(*cubeKey))
		time.Sleep(300 * time.Millisecond)
		logger.Info("cube: stash pass done", "stashed", stashed, "candidates", len(move))
		return stashed
	}

	// findNpcMenu locates the gold-bordered NPC dialogue box (TALK/TRADE/CANCEL) in a
	// screenshot. The box anchors in WORLD space above the NPC, so its screen position moves
	// with the camera — measured at (960-1050,128-245) and (860-950,245-355) for the same
	// dialogue. Returns the box center-x and top-y; rows sit at top+49 (TALK), +73 (TRADE),
	// +97 (CANCEL) in raw physical pixels.
	findNpcMenu := func(img image.Image) (int, int, int, bool) {
		b := img.Bounds()
		type pt struct{ x, y int }
		var golds []pt
		for y := 60; y < b.Dy()*2/3; y++ { // 1px steps: the border lines are ~2px, 2px sampling missed them
			for x := b.Dx() / 4; x < b.Dx()*3/4; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				r8, g8, b8 := int(r>>8), int(g>>8), int(bl>>8)
				if r8 > 140 && g8 > 100 && b8 < 120 && r8 >= g8 && g8 > b8+20 {
					golds = append(golds, pt{x, y})
				}
			}
		}
		if len(golds) < 80 {
			return 0, 0, 0, false
		}
		// Median-anchored cluster, then a tight bounding box of members.
		xs := make([]int, len(golds))
		ys := make([]int, len(golds))
		for i, p := range golds {
			xs[i], ys[i] = p.x, p.y
		}
		sort.Ints(xs)
		sort.Ints(ys)
		mx, my := xs[len(xs)/2], ys[len(ys)/2]
		minX, maxX, minY, maxY := 1<<30, 0, 1<<30, 0
		n := 0
		for _, p := range golds {
			if abs(p.x-mx) <= 80 && abs(p.y-my) <= 90 {
				minX, maxX = min(minX, p.x), max(maxX, p.x)
				minY, maxY = min(minY, p.y), max(maxY, p.y)
				n++
			}
		}
		if n < 30 || maxX-minX < 60 || maxX-minX > 160 || maxY-minY < 70 || maxY-minY > 160 {
			return 0, 0, 0, false
		}
		// BORDER TEST: the dialogue is a gold RECTANGLE — its edges must be populated lines,
		// not a diffuse glow (a torch cluster false-positived here and clicked open ground).
		topN, botN, leftN, rightN := 0, 0, 0, 0
		for _, p := range golds {
			if abs(p.x-mx) > 80 || abs(p.y-my) > 90 {
				continue
			}
			if abs(p.y-minY) <= 4 {
				topN++
			}
			if abs(p.y-maxY) <= 4 {
				botN++
			}
			if abs(p.x-minX) <= 4 {
				leftN++
			}
			if abs(p.x-maxX) <= 4 {
				rightN++
			}
		}
		if topN+botN+leftN+rightN < 40 {
			return 0, 0, 0, false
		}
		// DARK-INTERIOR test: the menu body is near-black; outdoor scenery behind torch glare
		// is not. Mean brightness of the inner region must be low.
		sum, cnt := 0, 0
		for y := minY + 8; y < maxY-8; y += 3 {
			for x := minX + 8; x < maxX-8; x += 3 {
				r, g, bl, _ := img.At(x, y).RGBA()
				sum += int(r>>8) + int(g>>8) + int(bl>>8)
				cnt++
			}
		}
		if cnt == 0 || sum/(cnt*3) > 85 {
			return 0, 0, 0, false
		}
		// TEXT-ROW test: the menu has 3-4 bright text bands (AKARA/TALK/TRADE/CANCEL). Count
		// interior rows holding >=6 bright pixels; pottery/torch clusters can't produce them.
		bands, inBand := 0, false
		for y := minY + 6; y < maxY-4; y++ {
			bright := 0
			for x := minX + 8; x < maxX-8; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				if r>>8 > 150 && g>>8 > 140 && bl>>8 > 110 {
					bright++
				}
			}
			if bright >= 6 {
				if !inBand {
					bands++
					inBand = true
				}
			} else {
				inBand = false
			}
		}
		if bands < 3 {
			return 0, 0, 0, false
		}
		return (minX + maxX) / 2, minY, maxY - minY, true
	}

	// findAppearedBox diffs two screenshots (camera static while the char stands still) and
	// returns the bounding box of the largest dense cluster of changed pixels, if it is
	// menu-sized. Differential detection: an APPEARING dialogue is a big contiguous change;
	// torch flicker is small and scattered. Kills the pottery-false-positive class entirely.
	findAppearedBox := func(before, after image.Image) (int, int, int, bool) {
		b := after.Bounds()
		const cell = 40
		cw, ch := b.Dx()/cell+1, b.Dy()/cell+1
		density := make([]int, cw*ch)
		for y := 40; y < b.Dy()-200; y += 2 {
			for x := 100; x < b.Dx()-100; x += 2 {
				r1, g1, b1, _ := before.At(x, y).RGBA()
				r2, g2, b2, _ := after.At(x, y).RGBA()
				d := abs(int(r1>>8)-int(r2>>8)) + abs(int(g1>>8)-int(g2>>8)) + abs(int(b1>>8)-int(b2>>8))
				if d > 90 {
					density[(y/cell)*cw+x/cell]++
				}
			}
		}
		bi, bv := -1, 0
		for i, v := range density {
			if v > bv {
				bi, bv = i, v
			}
		}
		if bi < 0 || bv < 60 { // a menu cell holds ~400 changed samples; flicker ~tens
			return 0, 0, 0, false
		}
		// Flood adjacent dense cells into a bounding box.
		cx0, cy0 := bi%cw, bi/cw
		minCX, maxCX, minCY, maxCY := cx0, cx0, cy0, cy0
		for expand := 0; expand < 6; expand++ {
			grew := false
			for _, c := range [][2]int{{minCX - 1, cy0}, {maxCX + 1, cy0}, {cx0, minCY - 1}, {cx0, maxCY + 1}} {
				x, y := c[0], c[1]
				if x >= 0 && y >= 0 && x < cw && y < ch && density[y*cw+x] > 40 {
					minCX, maxCX = min(minCX, x), max(maxCX, x)
					minCY, maxCY = min(minCY, y), max(maxCY, y)
					grew = true
				}
			}
			if !grew {
				break
			}
		}
		w := (maxCX - minCX + 1) * cell
		h := (maxCY - minCY + 1) * cell
		if w < 60 || w > 220 || h < 70 || h > 220 {
			return 0, 0, 0, false
		}
		return minCX*cell + w/2, minCY * cell, h, true
	}

	// -identify: exercise the identify errand standalone.
	if *idProbe {
		n := identifyErrand()
		logger.Info("identify probe done", "identified", n)
		if f, err := os.Create(shotPath("identify.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		return
	}

	// -npcclick "nameID,dx,dy": NPC BLIND-CLICK calibration. Hover never fires for town NPCs on
	// this build (hovergrid: zero hits over any townsfolk), so interaction must be a blind
	// interactClick at the body's screen position + a calibrated offset, verified by SCREENSHOT
	// (OpenMenus is stable garbage). Walks to the NPC first; the shot is npcclick.png.
	if *npcClick != "" {
		var wantID, cdx, cdy, rowX, rowY int
		nParsed, _ := fmt.Sscanf(*npcClick, "%d,%d,%d,%d,%d", &wantID, &cdx, &cdy, &rowX, &rowY)
		if nParsed < 3 {
			logger.Error("-npcclick wants 'nameID,dx,dy[,rowX,rowY]'", "got", *npcClick)
			return
		}
		_ = gr.FetchMapData()
		var t data.Monster
		have := false
		for i := 0; i < 100; i++ {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			for _, m := range d.Monsters {
				if int(m.Name) == wantID {
					t, have = m, true
					break
				}
			}
			if !have {
				logger.Error("npcclick: unit not in live table", "want", wantID)
				return
			}
			if chebyshev(me, t.Position) <= 8 {
				break
			}
			sx, sy := screenPointToward(me, t.Position.X-me.X, t.Position.Y-me.Y)
			walkToHold(sx, sy, 150)
			time.Sleep(150 * time.Millisecond)
		}
		d := gr.GetData()
		me := d.PlayerUnit.Position
		bx, by := gameToScreen(gr, me.X, me.Y, t.Position.X, t.Position.Y)
		logger.Info("npcclick: clicking", "name", wantID, "unitID", t.UnitID,
			"bodyScreen", fmt.Sprintf("(%d,%d)", bx, by), "offset", fmt.Sprintf("(%d,%d)", cdx, cdy),
			"npcPos", fmt.Sprintf("(%d,%d)", t.Position.X, t.Position.Y), "myPos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
		// CLOSED LOOP: try offsets until the menu VERIFIABLY opens (finder with border test);
		// only then click the requested row. Every miss walks the char a little, so re-read
		// positions fresh each attempt.
		menuCx, menuTop, menuH, menuOpen := findNpcMenu(gr.Screenshot())
		if !menuOpen && cdx != 99 {
			anchor := gr.GetData().PlayerUnit.Position // stable geometry: return here after any miss-walk
			// Aim bias measured from failure screenshots: predicted feet land ~20px LEFT of the
			// sprite; the hitbox is the torso (over feet, not legs). Bias right and up.
			offs := [][2]int{{cdx, cdy}, {28, -44}, {16, -44}, {40, -44}, {28, -28}, {16, -60}, {40, -60}, {28, -60}, {16, -28}, {40, -28}}
			// STILLNESS GATE: a click issued while the character is still moving is only a new
			// move order — it never hit-tests the NPC. Both successful opens tonight were
			// first-click-while-idle; every failure clicked mid-walk. Wait for three stable
			// position reads before each attempt.
			waitStill := func() data.Position {
				moveStop()
				last := gr.GetData().PlayerUnit.Position
				stable := 0
				for i := 0; i < 30 && stable < 3; i++ {
					time.Sleep(150 * time.Millisecond)
					cur := gr.GetData().PlayerUnit.Position
					if cur == last {
						stable++
					} else {
						stable = 0
					}
					last = cur
				}
				return last
			}
			for _, off := range offs {
				mee := waitStill()
				if chebyshev(mee, anchor) > 2 { // a previous miss walked us — restore the vantage
					for i := 0; i < 12 && chebyshev(gr.GetData().PlayerUnit.Position, anchor) > 2; i++ {
						mv := gr.GetData().PlayerUnit.Position
						sx2, sy2 := screenPointToward(mv, anchor.X-mv.X, anchor.Y-mv.Y)
						walkToHold(sx2, sy2, 120)
						time.Sleep(160 * time.Millisecond)
					}
					mee = waitStill()
				}
				var cur data.Monster
				for _, m := range gr.GetData().Monsters {
					if int(m.Name) == wantID {
						cur = m
						break
					}
				}
				bx2, by2 := gameToScreen(gr, mee.X, mee.Y, cur.Position.X, cur.Position.Y)
				logger.Info("npcclick: try", "offset", fmt.Sprintf("(%d,%d)", off[0], off[1]),
					"click", fmt.Sprintf("(%d,%d)", bx2+off[0], by2+off[1]))
				beforeImg := gr.Screenshot()
				interactClick(bx2+off[0], by2+off[1])
				time.Sleep(600 * time.Millisecond)
				if menuCx, menuTop, menuH, menuOpen = findAppearedBox(beforeImg, gr.Screenshot()); menuOpen {
					break
				}
			}
		}
		logger.Info("npcclick: menu state", "open", menuOpen, "center", menuCx, "top", menuTop, "h", menuH)
		if nParsed >= 5 && rowY == 999 { // magic: keyboard path — Down to TRADE, Enter to select
			logger.Info("npcclick: keyboard row select (Down, Enter)")
			hid.PressKey(hid.GetASCIICode("down"))
			time.Sleep(350 * time.Millisecond)
			hid.PressKey(hid.GetASCIICode("enter"))
			time.Sleep(1200 * time.Millisecond)
		} else if nParsed >= 5 && menuOpen {
			// Rows scale with the box: title/TALK/TRADE/CANCEL sit at ~19/40/60/81% of height.
			// rowY is now a PERCENT (40=TALK, 60=TRADE, 81=CANCEL).
			ry := menuTop + menuH*rowY/100
			preP := gr.GetData().PlayerUnit.Position
			logger.Info("npcclick: clicking row", "pct", rowY, "clickY", ry)
			uiClick(menuCx, ry)
			time.Sleep(400 * time.Millisecond)
			postP := gr.GetData().PlayerUnit.Position
			logger.Info("npcclick: row result", "walked", chebyshev(preP, postP) > 1,
				"myPos", fmt.Sprintf("(%d,%d)", postP.X, postP.Y))
			time.Sleep(900 * time.Millisecond)
		}
		if f, err := os.Create(shotPath("npcclick.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		d2 := gr.GetData()
		logger.Info("npcclick: after", "area", int(d2.PlayerUnit.Area),
			"myPos", fmt.Sprintf("(%d,%d)", d2.PlayerUnit.Position.X, d2.PlayerUnit.Position.Y),
			"shot", shotPath("npcclick.png"))
		return
	}

	// -uiclick x,y: isolate the panel-click primitive. Every "the panel didn't respond" failure so
	// far has been diagnosed by INFERENCE from a downstream effect that didn't happen (no travel),
	// which produced three different wrong root causes. This clicks a target whose response is
	// unambiguous and visible in a screenshot, with nothing else in the loop.
	// (-wpcal -uiclick composes: wpcal opens the panel first, then clicks. Don't steal that.)
	if *uiClickAt != "" && !*wpCal {
		if *moveKey == "" {
			logger.Error("-uiclick needs -move e")
			return
		}
		var ux, uy int
		if _, err := fmt.Sscanf(*uiClickAt, "%d,%d", &ux, &uy); err != nil {
			logger.Error("-uiclick wants \"x,y\" in CLIENT coords", "got", *uiClickAt, "err", err)
			return
		}
		if f, err := os.Create(shotPath("uiclick_before.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		_, blBefore := panelLitRow()
		logger.Info("uiclick: clicking", "client", fmt.Sprintf("(%d,%d)", ux, uy),
			"panelOpenBefore", blBefore > 25, "dpiScale", *dpiScale, "panelFn", *panelFn,
			"win", fmt.Sprintf("(%d,%d)", gr.WindowLeftX, gr.WindowTopY),
			"gameArea", fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY))
		uiClick(ux, uy)
		time.Sleep(600 * time.Millisecond)
		_, blAfter := panelLitRow()
		if f, err := os.Create(shotPath("uiclick_after.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		logger.Info("uiclick: RESULT", "panelOpenBefore", blBefore > 25, "panelOpenAfter", blAfter > 25,
			"changed", (blBefore > 25) != (blAfter > 25), "bluenessBefore", blBefore, "bluenessAfter", blAfter,
			"shots", shotPath("uiclick_before.png")+" / "+shotPath("uiclick_after.png"))
		return
	}

	// -wpcal: WAYPOINT CALIBRATION ORACLE. Opens the WP panel and reports which row is LIT (the
	// blue compass marks the area she is standing in) — then clicks NOTHING and travels nowhere.
	//
	// Why this exists: the shipped map contradicted itself. panelLitRow scans rows at y=178+41*r,
	// while -wpgoto clicked y=96+41*row — 82px (exactly 2 rows) apart, both called "row". In
	// lit-space the table's row 0 -> y=130 and row 1 -> y=137 land ABOVE the first row (178), i.e.
	// outside the list, which is exactly the old "panel closes but doesn't travel" symptom. That
	// map was force-fit to a single observation and declared verified. So: measure, don't infer.
	// Standing in a known area and reading which row lights up is a zero-click ground truth.
	if *wpCal {
		if *moveKey == "" {
			logger.Error("-wpcal needs -move e")
			return
		}
		var wp data.Object
		var me data.Position
		best, have := 1<<30, false
		for fetch := 0; fetch < 12 && !have; fetch++ {
			_ = gr.FetchMapData()
			d := gr.GetData()
			me = d.PlayerUnit.Position
			for _, o := range d.Objects {
				if o.IsWaypoint() && o.ID != 0 {
					if dd := chebyshev(me, o.Position); dd < best {
						best, wp, have = dd, o, true
					}
				}
			}
			if !have {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if !have || best > 200 {
			logger.Error("wpcal: no waypoint within range — walk her near one first", "nearestWPdist", best)
			return
		}
		curArea := int(gr.GetData().PlayerUnit.Area)
		opened := false
		for tryOpen := 0; tryOpen < 6 && !opened; tryOpen++ {
			for i := 0; i < 20; i++ {
				me = gr.GetData().PlayerUnit.Position
				if chebyshev(me, wp.Position) <= 3 {
					break
				}
				sx, sy := screenPointToward(me, wp.Position.X-me.X, wp.Position.Y-me.Y)
				walkToHold(sx, sy, 130)
			}
			hoverPickClick(wp.Position, wp.ID)
			time.Sleep(700 * time.Millisecond)
			if _, bl := panelLitRow(); bl > 25 {
				opened = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !opened {
			logger.Error("wpcal: failed to open the waypoint panel (clear monsters off the WP first)")
			return
		}
		// Per-row blueness: the lit row IS curArea's row. That single pair (curArea -> litRow),
		// collected from a few areas, is the whole calibration — no formula guessing required.
		lit, bl := panelLitRow()
		img := gr.Screenshot()
		var blues []string
		for r := 0; r < 8; r++ {
			ry := 178 + 41*r
			var sum, n float64
			for y := ry - 8; y <= ry+8; y++ {
				for x := 142; x <= 170; x++ {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					sum += float64(int(bb>>8) - (int(rr>>8)+int(gg>>8))/2)
					n++
				}
			}
			blues = append(blues, fmt.Sprintf("r%d(y=%d):%.1f", r, ry, sum/n))
		}
		logger.Info("wpcal: GROUND TRUTH", "currentArea", curArea, "litRow", lit, "blueness", bl,
			"rowY", 178+41*lit, "perRow", strings.Join(blues, " "))
		if shot := shotPath(fmt.Sprintf("wpcal_area%d.png", curArea)); true {
			if f, err := os.Create(shot); err == nil {
				_ = png.Encode(f, img)
				f.Close()
				logger.Info("wpcal: screenshot", "path", shot)
			}
		}
		// -wpcal -aimsweep: measure the ABSOLUTE cursor mapping against the game's own reaction.
		//
		// aimPanel computes physical = (windowOrigin + client) * dpiScale and trusts it. Nothing has
		// ever tested that: force-move only needs a DIRECTION, and hoverPickClick sweeps +-80px until
		// the game reports a hover, so it succeeds THROUGH any mapping error. Meanwhile the panel's X
		// (which hit-tests off the message lParam) responds while the rows (which hit-test off the
		// polled cursor) do not — which says the mapping is wrong by some offset/scale.
		//
		// So ask the game: park the cursor at a series of aim-Ys and see which row IT highlights.
		// aimY -> highlighted row IS the mapping, measured rather than derived.
		if *aimSweep {
			rowLum := func(im image.Image, r int) float64 {
				ry := 178 + 41*r
				var sum, n float64
				for y := ry - 12; y <= ry+12; y++ {
					for x := 190; x <= 410; x++ {
						rr, gg, bb, _ := im.At(x, y).RGBA()
						sum += float64((int(rr>>8) + int(gg>>8) + int(bb>>8)) / 3)
						n++
					}
				}
				if n == 0 {
					return 0
				}
				return sum / n
			}
			// Baseline with the cursor parked far off the panel, so we compare against "nothing
			// hovered" instead of assuming rows are equally bright (locked rows are dimmer).
			aimPanel(760, 440)
			time.Sleep(250 * time.Millisecond)
			base := make([]float64, 8)
			bimg := gr.Screenshot()
			for r := 0; r < 8; r++ {
				base[r] = rowLum(bimg, r)
			}
			logger.Info("aimsweep: baseline captured (cursor parked off-panel)")
			for aimY := 60; aimY <= 470; aimY += 10 {
				aimPanel(250, aimY)
				time.Sleep(90 * time.Millisecond)
				im := gr.Screenshot()
				bestR, bestD := -1, 2.0 // require a real delta, not noise
				var deltas []string
				for r := 0; r < 8; r++ {
					d := rowLum(im, r) - base[r]
					deltas = append(deltas, fmt.Sprintf("r%d:%+.1f", r, d))
					if d > bestD {
						bestR, bestD = r, d
					}
				}
				if bestR >= 0 {
					logger.Info("aimsweep: HIT", "aimY", aimY, "highlightedRow", bestR,
						"rowY", 178+41*bestR, "delta", fmt.Sprintf("%.1f", bestD),
						"all", strings.Join(deltas, " "))
				}
			}
			restorePanel()
			logger.Info("aimsweep: DONE — aimY->highlightedRow is the absolute mapping; compare aimY against rowY")
			return
		}

		// -wpcal -uiclick x,y: with the panel PROVEN open (blueness above), click a target and see
		// whether the panel reacts at all. Tests the click primitive against a known-open UI in one
		// run — the previous attempt clicked a panel that had already closed and proved nothing.
		if *uiClickAt != "" {
			var ux, uy int
			if _, err := fmt.Sscanf(*uiClickAt, "%d,%d", &ux, &uy); err != nil {
				logger.Error("-uiclick wants \"x,y\"", "got", *uiClickAt)
				return
			}
			logger.Info("wpcal: clicking with panel PROVEN open", "client", fmt.Sprintf("(%d,%d)", ux, uy))
			uiClick(ux, uy)
			time.Sleep(700 * time.Millisecond)
			lit2, bl2 := panelLitRow()
			if f, err := os.Create(shotPath("wpcal_click_after.png")); err == nil {
				_ = png.Encode(f, gr.Screenshot())
				f.Close()
			}
			logger.Info("wpcal: CLICK RESULT", "areaBefore", curArea,
				"areaAfter", int(gr.GetData().PlayerUnit.Area),
				"panelOpenBefore", true, "panelOpenAfter", bl2 > 25,
				"litRowAfter", lit2, "bluenessAfter", bl2,
				"shot", shotPath("wpcal_click_after.png"))
			return
		}
		restorePanel()
		return
	}

	// -wpgoto: travel to an AREA via the waypoint panel. area->row is measured, NOT derived:
	// each entry comes from standing in that area and reading which row lights up (-wpcal).
	// clickY = 178 + 41*row, the SAME formula panelLitRow scans with — one map, not two.
	if *wpGoto != 0 {
		if *moveKey == "" {
			logger.Error("-wpgoto needs -move e")
			return
		}
		// Act II area -> row index, in the SAME row-space panelLitRow scans.
		//
		// PROVENANCE (be honest about which of these is evidence):
		//   40 Lut Gholein -> row 0  MEASURED 2026-07-17 by -wpcal standing in area 40 (blue compass
		//                            lit r0 at y=178, blueness 59.7 vs <=1.5 elsewhere), and TRAVELLED
		//                            TO from Dry Hills (42 -> 40 ARRIVED).
		//   42 Dry Hills   -> row 2  MEASURED by -wpcal standing in area 42 (lit r2 at y=260), and
		//                            TRAVELLED TO from town (40 -> 42 ARRIVED). Round trip, both
		//                            directions, two different rows.
		//   48, 43                  ASSUMED from vanilla Act II order. Consistent with the two
		//                            measurements, NOT independently verified. Confirm by travelling
		//                            there and running -wpcal — do not promote them on vibes.
		// Rows 3/5/6/7 (Halls of Dead L2, Lost City, Palace Cellar L1, Arcane Sanctuary) and Acts
		// I/III are unmapped — add them from -wpcal readings, never from vanilla order alone.
		areaRow := map[int]int{
			40: 0, // Lut Gholein (town)  [MEASURED]
			48: 1, // Sewers Level 2      [assumed]
			42: 2, // Dry Hills           [assumed]
			43: 4, // Far Oasis           [assumed]
		}
		row, ok := areaRow[*wpGoto]
		if !ok {
			logger.Error("wpgoto: area not in the WP row table yet — travel there once and run -wpcal", "area", *wpGoto)
			return
		}
		// ONE row-space, shared with panelLitRow. The previous formula was clickY = 96 + 41*row
		// with row 0 hand-clamped to 130 — 82px (exactly 2 rows) above this one. In this space its
		// rows 0 and 1 mapped to y=130/137, i.e. ABOVE the first row at 178 and outside the list
		// (the old "panel closes but doesn't travel"), while its row 2 (Dry Hills) landed on y=178
		// which is TOWN's row. So -wpgoto travelled to town regardless of the destination asked
		// for, and the single "verified round-trip" only ever exercised that one accidental row.
		// The clamp was the tell: a formula force-fit to one data point.
		clickY := 178 + 41*row
		var wp data.Object
		var me data.Position
		best, have := 1<<30, false
		for fetch := 0; fetch < 12 && !have; fetch++ {
			_ = gr.FetchMapData()
			d := gr.GetData()
			me = d.PlayerUnit.Position
			for _, o := range d.Objects {
				if o.IsWaypoint() && o.ID != 0 {
					if dd := chebyshev(me, o.Position); dd < best {
						best, wp, have = dd, o, true
					}
				}
			}
			if !have {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if !have || best > 200 {
			logger.Error("wpgoto: no waypoint within range", "nearestWPdist", best)
			return
		}
		if int(gr.GetData().PlayerUnit.Area) == *wpGoto {
			logger.Info("wpgoto: already at destination", "area", *wpGoto)
			return
		}
		opened := false
		for tryOpen := 0; tryOpen < 6 && !opened; tryOpen++ {
			for i := 0; i < 20; i++ {
				me = gr.GetData().PlayerUnit.Position
				if chebyshev(me, wp.Position) <= 3 {
					break
				}
				sx, sy := screenPointToward(me, wp.Position.X-me.X, wp.Position.Y-me.Y)
				walkToHold(sx, sy, 130)
			}
			hoverPickClick(wp.Position, wp.ID)
			time.Sleep(700 * time.Millisecond)
			if _, bl := panelLitRow(); bl > 25 {
				opened = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !opened {
			logger.Error("wpgoto: failed to open the waypoint panel")
			return
		}
		areaBefore := int(gr.GetData().PlayerUnit.Area)
		logger.Info("wpgoto: selecting", "destArea", *wpGoto, "row", row, "clickY", clickY, "areaBefore", areaBefore)
		if f, err := os.Create(shotPath("wpgoto_before.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		uiClick(200, clickY)
		for i := 0; i < 24; i++ {
			time.Sleep(300 * time.Millisecond)
			if a := int(gr.GetData().PlayerUnit.Area); a != areaBefore {
				if a == *wpGoto {
					logger.Info("wpgoto: ARRIVED", "area", a)
				} else {
					logger.Warn("wpgoto: traveled to WRONG area", "want", *wpGoto, "got", a)
				}
				return
			}
		}
		// Capture WHY. The three failures look identical from the area check but are different bugs:
		// panel still open + row highlighted = the click was seen but not accepted; panel still open
		// + nothing highlighted = the click missed the row; panel gone = the click landed outside and
		// dismissed it. Guessing between them is what produced the last three wrong root causes.
		lit2, bl2 := panelLitRow()
		if f, err := os.Create(shotPath("wpgoto_after.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		logger.Error("wpgoto: no area change", "stillArea", int(gr.GetData().PlayerUnit.Area),
			"panelStillOpen", bl2 > 25, "litRowAfter", lit2, "bluenessAfter", bl2,
			"shots", shotPath("wpgoto_before.png")+" / "+shotPath("wpgoto_after.png"))
		return
	}

	// -wpaim: AIM ORACLE. Open the WP panel, then sweep the patched cursor down the 8 nominal
	// destination-row Y's (WITHOUT clicking) and, at each, screenshot + measure the luminance of
	// every row band. The row that brightens (hover highlight) at each aim-Y reveals the true
	// export + affine map (aim-Y -> highlighted row) with zero clicks and zero travel. If NO row
	// ever brightens for -panelfn phys, the panel isn't driven by GetPhysicalCursorPos (try pos/info);
	// if none brighten for ANY export, the panel is lParam-driven (uiClick now sends client lParam).
	if *wpAim {
		if *moveKey == "" {
			logger.Error("-wpaim needs -move e")
			return
		}
		// FetchMapData is flaky (populates d.Objects only on a successful map-seed read) — retry
		// until the WP appears, like -wpprobe's loop does.
		var wp data.Object
		var me data.Position
		best, have := 1<<30, false
		for fetch := 0; fetch < 12 && !have; fetch++ {
			_ = gr.FetchMapData()
			d := gr.GetData()
			me = d.PlayerUnit.Position
			for _, o := range d.Objects {
				if o.IsWaypoint() && o.ID != 0 {
					if dd := chebyshev(me, o.Position); dd < best {
						best, wp, have = dd, o, true
					}
				}
			}
			if !have {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if !have || best > 200 {
			logger.Error("wpaim: no waypoint within range", "nearestWPdist", best)
			return
		}
		opened := false
		for tryOpen := 0; tryOpen < 6 && !opened; tryOpen++ {
			for i := 0; i < 20; i++ {
				me = gr.GetData().PlayerUnit.Position
				if chebyshev(me, wp.Position) <= 3 {
					break
				}
				sx, sy := screenPointToward(me, wp.Position.X-me.X, wp.Position.Y-me.Y)
				walkToHold(sx, sy, 130)
			}
			if hoverPickClick(wp.Position, wp.ID) {
				opened = true
			} else {
				logger.Info("wpaim: WP open retry", "attempt", tryOpen+1)
				time.Sleep(300 * time.Millisecond)
			}
		}
		if !opened {
			logger.Error("wpaim: failed to open the waypoint panel")
			return
		}
		time.Sleep(900 * time.Millisecond)
		// The panel marks the CURRENT area's row with a lit BLUE compass icon (~x155). Detecting
		// which row is blue tells us currentArea -> row deterministically, with ZERO travel and no
		// dependence on the cursor (which the panel ignores for hover). Accumulate this across
		// areas she visits to build the area<->row half of the map; the click-Y transform is the
		// other half (measured separately via -wptown travels).
		rowY := func(r int) int { return 178 + 41*r }
		// blueness = how "lit blue compass" a row's icon column is (high B, low R/G).
		blueness := func(img image.Image, ry int) float64 {
			var sum, n float64
			for y := ry - 8; y <= ry+8; y++ {
				for x := 142; x <= 170; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					sum += float64(int(b>>8) - (int(r>>8)+int(g>>8))/2)
					n++
				}
			}
			if n == 0 {
				return 0
			}
			return sum / n
		}
		img := gr.Screenshot()
		blues := make([]int, 8)
		bestBlue, litRow := -1e9, -1
		for r := 0; r < 8; r++ {
			bl := blueness(img, rowY(r))
			blues[r] = int(bl)
			if bl > bestBlue {
				bestBlue, litRow = bl, r
			}
		}
		curArea := int(gr.GetData().PlayerUnit.Area)
		logger.Info("wpaim: LITROW", "currentArea", curArea, "litRow", litRow,
			"blueness", fmt.Sprintf("%v", blues), "win", fmt.Sprintf("(%d,%d)", gr.WindowLeftX, gr.WindowTopY))
		if f, err := os.Create(shotPath(fmt.Sprintf("wpaim_area%d.png", curArea))); err == nil {
			_ = png.Encode(f, img)
			f.Close()
		}
		restorePanel()
		logger.Info("wpaim: DONE — read wpaim_<fn>_r*.png; brightRow at each aimRow reveals the aim map")
		return
	}

	// -wptown: waypoint-travel to Act2 town (Lut Gholein). Panel layout mapped from a live
	// screenshot (853x480 client space): Act tabs at y~147 (II ~x237), destination rows start
	// at y~178 spaced ~41px; Lut Gholein is Act2 row 0. Click WP -> panel -> Act II -> town row,
	// confirm by PlayerUnit.Area.
	if *wpTown {
		if *moveKey == "" {
			logger.Error("-wptown needs -move e")
			return
		}
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		_ = gr.FetchMapData()
		d := gr.GetData()
		me := d.PlayerUnit.Position
		var wp data.Object
		best, have := 1<<30, false
		for _, o := range d.Objects {
			if o.IsWaypoint() && o.ID != 0 {
				if dd := chebyshev(me, o.Position); dd < best {
					best, wp, have = dd, o, true
				}
			}
		}
		if !have || best > 120 {
			logger.Error("wptown: no waypoint within range", "nearestWPdist", best)
			return
		}
		// Walk onto the WP if we're not already on it.
		for i := 0; i < 30; i++ {
			me = gr.GetData().PlayerUnit.Position
			if chebyshev(me, wp.Position) <= 4 {
				break
			}
			sx, sy := screenPointToward(me, wp.Position.X-me.X, wp.Position.Y-me.Y)
			walkToHold(sx, sy, 150)
		}
		areaBefore := int(gr.GetData().PlayerUnit.Area)
		logger.Info("wptown: opening WP", "wpDist", best, "areaBefore", areaBefore)
		// Opening the WP (hoverPickClick) is intermittent under a monster swarm — the label can be
		// briefly occluded. Retry: re-walk onto the WP and re-sweep until the panel opens (screenshot
		// confirms) or we exhaust attempts. Confirm via a fresh screenshot's presence of the panel is
		// hard headless, so we retry a fixed number and proceed; the area-change check is the ground truth.
		opened := false
		for tryOpen := 0; tryOpen < 6 && !opened; tryOpen++ {
			for i := 0; i < 20; i++ {
				me = gr.GetData().PlayerUnit.Position
				if chebyshev(me, wp.Position) <= 3 {
					break
				}
				sx, sy := screenPointToward(me, wp.Position.X-me.X, wp.Position.Y-me.Y)
				walkToHold(sx, sy, 130)
			}
			hoverPickClick(wp.Position, wp.ID)
			time.Sleep(700 * time.Millisecond)
			// VERIFY the panel is actually open (blue compass present) before trusting the click —
			// hoverPickClick can return true without the panel opening, which silently wastes the run.
			if lr, bl := panelLitRow(); bl > 25 {
				opened = true
				logger.Info("wptown: panel open confirmed", "litRow", lr, "blueness", int(bl))
				break
			}
			logger.Info("wptown: WP open retry (panel not detected)", "attempt", tryOpen+1)
			time.Sleep(300 * time.Millisecond)
		}
		if !opened {
			logger.Error("wptown: failed to open the waypoint panel after retries")
			return
		}
		time.Sleep(300 * time.Millisecond)
		// Screenshot the freshly-opened panel so calibration is against the ACTUAL state.
		if f, err := os.Create(shotPath("wp_panel.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		if *wpTab {
			uiClick(237, 147) // Act II tab
		}
		uiClick(*wpCol, *wpRow) // destination click (calibratable via -wpcol/-wprow)
		// Diagnostic: also capture an AFTER screenshot so we can see whether the click landed on
		// the panel at all (does the row highlight / did anything change).
		time.Sleep(300 * time.Millisecond)
		if f, err := os.Create(shotPath("wp_after.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		// Wait for the loading screen + area change.
		arrived := false
		for i := 0; i < 24; i++ {
			time.Sleep(300 * time.Millisecond)
			if a := int(gr.GetData().PlayerUnit.Area); a != areaBefore {
				logger.Info("wptown: ARRIVED", "area", a)
				arrived = true
				break
			}
		}
		if !arrived {
			logger.Error("wptown: no area change — panel coords may be off; check a -screenshot", "stillArea", int(gr.GetData().PlayerUnit.Area))
		}
		return
	}

	// -screenshot: capture the D2R window to a PNG (for mapping UI panel layouts).
	if *screenshot != "" {
		img := gr.Screenshot()
		f, err := os.Create(*screenshot)
		if err != nil {
			logger.Error("screenshot: create failed", "err", err)
			return
		}
		if err := png.Encode(f, img); err != nil {
			logger.Error("screenshot: encode failed", "err", err)
		}
		f.Close()
		logger.Info("screenshot saved", "path", *screenshot, "size", fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY))
		return
	}

	// -wpat: crack the waypoint. Assumes the character is standing on/next to the WP. Dumps nearby
	// selectable objects (to identify the mod's WP object id), clicks the nearest via the proven
	// hoverPickClick, then reports panel-open signals so we learn which one is reliable here.
	if *wpAt {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		_ = gr.FetchMapData()
		d := gr.GetData()
		me := d.PlayerUnit.Position
		logger.Info("wpat: player", "area", int(d.PlayerUnit.Area), "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
			"availWPs", fmt.Sprintf("%v", d.PlayerUnit.AvailableWaypoints))
		// Dump EVERY object near the player (not just Selectable) closest-first — the WP may not
		// be flagged Selectable/IsWaypoint on this mod, so we need to see everything to find it.
		type objDist struct {
			o data.Object
			d int
		}
		var near []objDist
		for _, o := range d.Objects {
			dd := chebyshev(me, o.Position)
			if dd <= 60 {
				near = append(near, objDist{o, dd})
			}
		}
		sort.Slice(near, func(i, j int) bool { return near[i].d < near[j].d })
		for _, od := range near {
			o := od.o
			logger.Info("wpat: obj", "name", int(o.Name), "id", int(o.ID), "dist", od.d,
				"selectable", o.Selectable, "isWaypoint", o.IsWaypoint(), "mode", int(o.Mode),
				"pos", fmt.Sprintf("(%d,%d)", o.Position.X, o.Position.Y))
		}
		var wp data.Object
		best, have := 1<<30, false
		for _, od := range near {
			if od.o.ID != 0 && od.d < best {
				best, wp, have = od.d, od.o, true
			}
		}
		if !have {
			logger.Info("wpat: NO object near player at all — is FetchMapData populating d.Objects?")
			return
		}
		// Prefer an IsWaypoint-flagged object (the WP is selectable=false + isWaypoint=true).
		for _, od := range near {
			if od.o.IsWaypoint() && od.o.ID != 0 {
				wp = od.o
				break
			}
		}
		logger.Info("wpat: clicking WP", "name", int(wp.Name), "id", int(wp.ID), "isWaypoint", wp.IsWaypoint(), "dist", chebyshev(me, wp.Position))
		clicked := hoverPickClick(wp.Position, wp.ID)
		time.Sleep(800 * time.Millisecond)
		// Aim the in-game cursor at the INTENDED town row (250,178) so the screenshot shows where
		// the click actually lands — reveals any offset between screenshot pixels and click target.
		hid.AimPhysical(250, 178)
		time.Sleep(250 * time.Millisecond)
		if f, err := os.Create(shotPath("wp_panel_probe.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("wpat: screenshot saved (cursor aimed at 250,178 = intended town row)")
		}
		d2 := gr.GetData()
		logger.Info("wpat: AFTER click", "clicked", clicked,
			"openMenus", fmt.Sprintf("%+v", d2.OpenMenus),
			"availWPs", fmt.Sprintf("%v", d2.PlayerUnit.AvailableWaypoints),
			"area", int(d2.PlayerUnit.Area))
		logger.Info("wpat: done — if a WP panel opened, note which signal changed (AvailableWaypoints populated / OpenMenus.Waypoint / etc.)")
		return
	}

	// -clicktest: is the in-game LEFT-click even registering? Aim interactClick at the nearest
	// monster and watch player Mode + monster life. If she attacks (mode->attack, or monster
	// takes damage), left-click WORKS and loot fails on hitbox/pickup-precedence; if nothing
	// ever happens, the left button isn't being read (input-path RE, like the cursor). We test
	// in BOTH werewolf and human form to isolate the attack-on-left / form question.
	if *clickTest {
		test := func(label string) {
			for i := 0; i < 5; i++ {
				d := gr.GetData()
				me := d.PlayerUnit.Position
				var mon data.Monster
				best, have := 1<<30, false
				for _, m := range d.Monsters.Enemies() {
					if m.Stats[stat.Life] <= 0 {
						continue
					}
					if dd := chebyshev(me, m.Position); dd < best {
						best, mon, have = dd, m, true
					}
				}
				if !have {
					logger.Info("clicktest: no monster in range", "phase", label)
					time.Sleep(500 * time.Millisecond)
					continue
				}
				if best > 25 { // close in first so the target is on-screen
					sx, sy := screenPointToward(me, mon.Position.X-me.X, mon.Position.Y-me.Y)
					walkToHold(sx, sy, 200)
					continue
				}
				sx, sy := gameToScreen(gr, me.X, me.Y, mon.Position.X, mon.Position.Y)
				lifeBefore := mon.Stats[stat.Life]
				modeBefore := int(d.PlayerUnit.Mode)
				interactClick(sx, sy)
				time.Sleep(250 * time.Millisecond)
				d2 := gr.GetData()
				lifeAfter := lifeBefore
				if m2, ok := d2.Monsters.FindByID(mon.UnitID); ok {
					lifeAfter = m2.Stats[stat.Life]
				}
				logger.Info("clicktest", "phase", label, "monDist", best,
					"screen", fmt.Sprintf("(%d,%d)", sx, sy),
					"playerMode", fmt.Sprintf("%d->%d", modeBefore, int(d2.PlayerUnit.Mode)),
					"monLife", fmt.Sprintf("%d->%d", lifeBefore, lifeAfter),
					"attacked", int(d2.PlayerUnit.Mode) != modeBefore || lifeAfter < lifeBefore)
			}
		}
		// Item phase: the actual loot target. Fetch so ground items populate, walk to the
		// nearest, click it, and log player Mode (attack-mode => she's swinging at the ground,
		// not picking up) + whether the item disappeared.
		testItems := func(label string) {
			cfg.Game.Difficulty = difficulty.Difficulty(*diff)
			_ = gr.FetchMapData()
			for i := 0; i < 6; i++ {
				d := gr.GetData()
				me := d.PlayerUnit.Position
				var it data.Item
				best, have := 1<<30, false
				for _, x := range d.Inventory.ByLocation(item.LocationGround) {
					if dd := chebyshev(me, x.Position); dd < best {
						best, it, have = dd, x, true
					}
				}
				if !have {
					logger.Info("clicktest: no ground item", "phase", label)
					return
				}
				// Stand a couple tiles OFF the item — its clickable label renders above the sprite,
				// and when you're on top of it the label is hidden under the character.
				if best <= 2 {
					sx, sy := screenPointToward(me, me.X-it.Position.X, me.Y-it.Position.Y)
					walkToHold(sx, sy, 150)
					continue
				}
				if best > 5 {
					sx, sy := screenPointToward(me, it.Position.X-me.X, it.Position.Y-me.Y)
					walkToHold(sx, sy, 220)
					continue
				}
				sx, sy := gameToScreen(gr, me.X, me.Y, it.Position.X, it.Position.Y)
				modeBefore := int(d.PlayerUnit.Mode)
				hoverFound := hoverPickClick(it.Position, it.UnitID)
				hx, hy := sx, sy
				time.Sleep(300 * time.Millisecond)
				d2 := gr.GetData()
				gone := true
				for _, x := range d2.Inventory.ByLocation(item.LocationGround) {
					if x.UnitID == it.UnitID {
						gone = false
						break
					}
				}
				logger.Info("clicktest: ITEM", "phase", label, "name", string(it.Name), "dist", best,
					"itemWorld", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y),
					"groundScreen", fmt.Sprintf("(%d,%d)", sx, sy),
					"hoverFound", hoverFound, "hoverScreen", fmt.Sprintf("(%d,%d)", hx, hy),
					"playerMode", fmt.Sprintf("%d->%d", modeBefore, int(d2.PlayerUnit.Mode)),
					"pickedUp", gone)
				if gone {
					return
				}
			}
		}
		logger.Info("clicktest: HUMAN form (monsters)")
		test("human")
		logger.Info("clicktest: ITEMS in human form")
		testItems("human-item")
		logger.Info("clicktest: WEREWOLF form")
		castSelf(*werewolf)
		test("werewolf")
		logger.Info("clicktest: ITEMS in werewolf form")
		testItems("werewolf-item")
		logger.Info("clicktest: done — pickedUp=true => left-click works; playerMode change on item => attacking not picking up; all-fail+no-mode-change => input-path RE")
		return
	}

	if *nav {
		cfg.Game.Difficulty = difficulty.Difficulty(*diff)
		// Live grid reads room collision directly (no FetchMapData); koolo-map fallback needs it,
		// and object interaction (-objects) needs it too (d.Objects is empty until a fetch).
		if !*liveGrid || *objects {
			logger.Info("nav: fetching map data...")
			if err := gr.FetchMapData(); err != nil {
				logger.Error("nav: FetchMapData failed", "err", err)
			}
		}
		if g, ok := acquireGrid(); ok {
			navGrid = g
			navi = NewNavigator(g)
			alignedArea = int(gr.GetData().PlayerUnit.Area)
			walkables, walkCentroid = computeWalk(g)
			buildMapNavi()
			p := gr.GetData().PlayerUnit.Position
			logger.Info("nav: ENABLED", "area", alignedArea, "live", *liveGrid,
				"origin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY),
				"walkables", len(walkables), "playerWalkable", g.IsWalkable(p),
				"mapNavi", mapNavi != nil)
		} else {
			logger.Warn("nav: could not acquire grid — staying heuristic")
		}
	}

	inWerewolf := func() bool {
		return gr.GetData().PlayerUnit.States.HasState(state.Wolf)
	}

	// Survival: potion quaffing + chicken/death detection. Persistent last-drink timestamps so
	// cooldowns survive across ticks.
	var lastHeal, lastMana, lastRejuv time.Time

	// beltHealPos finds the first belt column holding ANYTHING drinkable for HP: vanilla healing
	// potions, rejuvs, or the mod's Herb. Reimagined replaces starter potions with Herbs, so the
	// name-based potion matcher saw an EMPTY belt while 4 herbs sat in it — the char starved at
	// 20hp with a full belt for 4 hours (measured). Match generously; a wasted sip is cheap.
	beltHealPos := func(d game.Data) (data.Position, bool) {
		if p, ok := d.Inventory.Belt.GetFirstPotion(data.HealingPotion); ok {
			return p, true
		}
		if p, ok := d.Inventory.Belt.GetFirstPotion(data.RejuvenationPotion); ok {
			return p, true
		}
		for _, i := range d.Inventory.Belt.Items {
			n := strings.ToLower(string(i.Name))
			if (strings.Contains(n, "herb") || strings.Contains(n, "potion")) &&
				i.Position.Y == 0 && i.Position.X >= 0 && i.Position.X <= 3 {
				return i.Position, true
			}
		}
		return data.Position{}, false
	}

	// drink presses the belt column holding the first potion of potType, respecting -potcd.
	// Returns false (no-op) if on cooldown or the potion isn't on the belt. The healing path
	// accepts any HP-drinkable (incl. mod herbs) via beltHealPos.
	drink := func(d game.Data, potType data.PotionType, last *time.Time) bool {
		if time.Since(*last) < time.Duration(*potcd)*time.Millisecond {
			return false
		}
		pos, ok := d.Inventory.Belt.GetFirstPotion(potType)
		if !ok && potType == data.HealingPotion {
			pos, ok = beltHealPos(d)
		}
		if !ok || pos.X < 0 || pos.X > 3 {
			return false
		}
		hid.PressKey(beltKeys[pos.X])
		*last = time.Now()
		return true
	}
	// drinkCol is the -hpcol/-mpcol fixed-column fallback: press a belt column directly instead
	// of auto-detecting the potion type, for setups where GetFirstPotion's name match is flaky.
	drinkCol := func(col int, last *time.Time) bool {
		if time.Since(*last) < time.Duration(*potcd)*time.Millisecond {
			return false
		}
		if col < 0 || col > 3 {
			return false
		}
		hid.PressKey(beltKeys[col])
		*last = time.Now()
		return true
	}
	// gameAlive asks the OS whether D2R still exists, instead of inferring it from game memory that
	// stops existing at the same moment. STILL_ACTIVE (259) is the only "yes".
	gameAlive := func() bool {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			return false
		}
		defer windows.CloseHandle(h)
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return false
		}
		return code == 259 // STILL_ACTIVE
	}
	survivalTick := func(d game.Data) survivalStatus {
		hp := d.PlayerUnit.HPPercent()
		mp := d.PlayerUnit.MPPercent()
		if hp <= 0 || d.PlayerUnit.Mode == mode.Death || d.PlayerUnit.Mode == mode.Dead {
			// Before calling this a death, rule out the two ways it can be a lie: the process is
			// gone (all reads return zero), or we are simply not in a game (menus/loading, Area 0).
			// Mode is NOT sufficient — a zeroed read yields Mode 0 too.
			if !gameAlive() {
				return StatusGameGone
			}
			if d.PlayerUnit.Area == 0 {
				return StatusGameGone
			}
			return StatusDead
		}
		if hp <= *chicken {
			// DRINK BEFORE FLEEING. Chicken returned before the drink branches ever ran, so in
			// the band hp <= chicken the bot fled forever while holding a full belt (measured:
			// hp pinned at 20 with 4 herbs). If anything drinkable exists, quaff it and stay in
			// the fight this tick — re-evaluate after the potion lands.
			if drink(d, data.HealingPotion, &lastHeal) {
				logger.Info("drink", "kind", "emergency", "hp", hp, "mp", mp)
				return StatusNormal
			}
			return StatusChicken
		}
		// Rejuv-first: it restores both HP and MP, so prefer it when either is low.
		if hp <= *hppct || mp <= *mppct {
			if drink(d, data.RejuvenationPotion, &lastRejuv) {
				logger.Info("drink", "kind", "rejuv", "hp", hp, "mp", mp)
			}
		}
		if hp <= *hppct {
			ok := false
			if *hpcol >= 0 {
				ok = drinkCol(*hpcol, &lastHeal)
			} else {
				ok = drink(d, data.HealingPotion, &lastHeal)
			}
			if ok {
				logger.Info("drink", "kind", "heal", "hp", hp, "mp", mp)
			}
		}
		if mp <= *mppct {
			ok := false
			if *mpcol >= 0 {
				ok = drinkCol(*mpcol, &lastMana)
			} else {
				ok = drink(d, data.ManaPotion, &lastMana)
			}
			if ok {
				logger.Info("drink", "kind", "mana", "hp", hp, "mp", mp)
			}
		}
		return StatusNormal
	}

	const meleeRange = 18 // bite reach: the werewolf lunges the last step, so bite early and
	// with hysteresis — a bit of extra reach stops the overshoot-and-turn-around dance right at
	// the engagement threshold (esp. vs fleeing Fallen; one Rabies bite spreads through the pack).
	const swingRange = 4 // normal-attack reach: walk all the way in before swinging.

	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	logger.Info("farming (rabies werewolf)", "for_seconds", *seconds,
		"gameArea", fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY))
	var targetID data.UnitID
	shiftCheck := 0
	lastDist := 1 << 30
	stuckCount := 0
	var progressPos data.Position
	progressAt := time.Now()
	breakDir := 0
	var posHist []data.Position // recent positions, to detect being confined to a thin strip
	breakoutTick := 0
	blacklist := map[data.UnitID]time.Time{} // unreachable targets we've given up on, briefly
	var exploreDest data.Position
	var exploreSince time.Time
	var gotoCross [][2]data.Position // (approach, cross) candidates for the current goto border
	gotoFailedAt := map[int]time.Time{} // candidate index -> last time it stalled out
	gotoIdx := 0
	gotoTryAt := time.Now()
	var gotoBorderSeekLog time.Time
	var gotoGridRefresh time.Time
	var gotoLastPos data.Position
	var entranceContactStart time.Time
	var mapPointContactAt time.Time // map-point steering: time standing AT an exit that won't fire
	exitClickTry := 0               // offset cycle for exit-tile clicks (sprite, not feet)
	var exitParkStart time.Time
	exitRingIdx := 0
	// Exit-seek GOAL-progress watchdog: the 5s/2-tile freeze watchdog misses treadmills — the
	// bot jiggles ±3 tiles against a wall pocket (resetting the freeze timer) while dist-to-exit
	// sits flat (measured: 9 minutes parked at dist=701, run burned). Track best dist ever
	// achieved toward the current exit; no improvement for 40s -> detour to a frontier so new
	// rooms load elsewhere; still no improvement 60s after that -> abandon the stop.
	exitSeekBestDist := 1 << 30
	var exitSeekBestAt time.Time
	var exitSeekExit data.Position
	var exitDetourDest data.Position
	var exitDetourUntil time.Time
	// Explore commitment: best-dist-so-far toward exploreDest; re-pick only on arrival or when
	// progress stalls (frontier re-selection mid-walk sent the bot straight back over its own
	// footsteps — measured at the end of run 2).
	exploreBestDist := 1 << 30
	var exploreBestAt time.Time
	gotoProgressAt := time.Now()
	var lastGotoTickAt time.Time    // last tick the goto block actually ran (resume-reset)
	var lastExploreTickAt time.Time // ditto for explore
	var badDests []data.Position // explore destinations that turned out unreachable (walled off)
	chickenStreak := 0           // consecutive Chicken ticks, so a one-frame HP dip doesn't spam TP
	tpDone := false              // emergency TP already fired for the current chicken episode
	// TOWN-TRIP state machine (control command "tp"; -tp key must be bound to the TP tome).
	// 1=cast, 2=enter field portal, 3=in town (errand hook + return), 0=idle.
	tpPhase := 0
	tpParkInTown := false // 'town' control command: stop at phase 3, skip the return
	tpErrandsDone := false // one errand pass per trip
	var lastAutoTrip time.Time
	tpWalkBack := 0 // walk-fallback: area to hike back to when the mod refused the portal
	var lastCubeStash time.Time
	stepHold := false          // 'hold' control command: freeze all behaviors for step-mode calibration
	var runStepCommand func(string)
	var runStepBatch func(string, int)
	var finishStepResult func()
	tpVendorWanted := false // akara control command: include the vendor errand
	tpOrigArea := 0
	var tpPhaseAt, tpTripStart time.Time
	tpRecast := false
	lootBlacklist := map[data.UnitID]time.Time{} // value = EXPIRY (click-fail 15s, unreachable 45s)
	lootAttempts := map[data.UnitID]int{}
	var lastTick time.Time // global tick floor anchor
	// Loot-approach progress tracking: straight-line aim walked into maze walls forever
	// (unique ShortBow in the Den, dist=6, 3ms cadence) — unreachable loot must time out.
	var lootApproachID data.UnitID
	lootApproachBest := 1 << 30
	var lootApproachAt, lootApproachLog time.Time
	objBlacklist := map[data.UnitID]time.Time{} // objects already used (so we don't re-open)
	wpTouched := map[data.UnitID]bool{}         // waypoints activated (or attempted) this run

	// curGoto is the LIVE travel target: normally the -goto flag, but death recovery retargets
	// it at the death area so the corpse run rides the same border-crossing machinery.
	curGoto := *gotoArea
	// Death state survives bot restarts: Corpse.Found only reads in the corpse's own area, so a
	// fresh process in town has NO way to know a corpse is waiting two zones away. The file is
	// written on death, cleared on recovery or give-up.
	deathStateFile := filepath.Join("logs", "death_state.txt")
	deathHuntArea := 0
	var deathSeekStart time.Time
	var deathHuntPos data.Position
	if st, err := os.Stat(deathStateFile); err == nil && time.Since(st.ModTime()) > 30*time.Minute {
		_ = os.Remove(deathStateFile) // stale — a death this old has either been recovered or expired
	}
	if b, err := os.ReadFile(deathStateFile); err == nil {
		var a, x, y int
		if n, _ := fmt.Sscanf(string(b), "%d %d %d", &a, &x, &y); n >= 1 && a != 0 {
			curGoto = a
			deathHuntArea = a
			deathHuntPos = data.Position{X: x, Y: y}
			logger.Info("resuming corpse recovery from a previous run", "deathArea", a,
				"deathPos", fmt.Sprintf("(%d,%d)", x, y))
		}
	}
	var corpseRecoverStart time.Time // zero = not currently recovering
	var corpseGiveUp time.Time       // set when a recovery attempt timed out (retry cooldown)
	var corpseLogAt time.Time
	corpseSweepFails := 0
	corpseSweepIdx := 0 // resumable-sweep cursor: which probe point the label hunt continues from
	corpseGoneStreak := 0
	autoSkillAt := time.Time{}
	progressTarget := 0
	// NOTE: 17 is Burial Grounds. The old table had 9 here believing it was Burial — but 9 is
	// CAVE LEVEL 1 (d2go area.go), so the route marched into the cave all night (two deaths,
	// hours of "lost near y~5380" = the cave entrance) while actual Burial Grounds visits —
	// including a clean Blood Raven kill — were walk-through-entrance ACCIDENTS.
	progressRoute := []int{8, 3, 17, 4, 5, 6} // Den of Evil, Cold Plains, Burial Grounds, Stony Field, Dark Wood, Black Marsh
	routeIdx := 0
	routeStateFile := filepath.Join("logs", "route_state.txt")
	if b, err := os.ReadFile(routeStateFile); err == nil {
		var ri int
		if n, _ := fmt.Sscanf(string(b), "%d", &ri); n == 1 && ri >= 0 && ri < len(progressRoute) {
			routeIdx = ri
		}
	}
	lastEnemySeen := time.Now()
	summonAt := time.Time{}
	summonCastPets := -1
	summonFails := 0
	var summonDisabledUntil time.Time
	var golemAt time.Time
	lastPosture := "engage"
	var postureHeldAt time.Time // posture hysteresis dwell anchor
	autoStatAt := time.Time{}
	var corpseTargetPos data.Position
	// meleeSwing: a left-click attack that actually CONNECTS. A blind interactClick at the
	// monster's computed feet position reads as "walk here" whenever the sprite isn't exactly
	// there — the char shuffles in place instead of swinging (observed live). Sweep until the
	// game itself reports the target hovered, then click THAT point; blind-click as last resort.
	meleeSwing := func(targetID data.UnitID, world data.Position) bool {
		moveStop() // attack aim must not double as a walk order
		me := gr.GetData().PlayerUnit.Position
		bx, by := gameToScreen(gr, me.X, me.Y, world.X, world.Y)
		for _, dy := range []int{-12, 0, -24, -36, 8} {
			for _, dx := range []int{0, -10, 10, -20, 20} {
				px, py := bx+dx, by+dy
				hid.AimPhysical(px, py)
				time.Sleep(45 * time.Millisecond)
				hd := gr.GetData().HoverData
				if !hd.IsHovered || hd.UnitID != targetID {
					continue
				}
				interactClick(px, py)
				return true
			}
		}
		interactClick(bx, by) // last resort — may register as a move, but keeps pressure on
		return false
	}

	// fightThrough: when movement is blocked and something hostile is in reach, the smart move
	// is violence, not another wall-bounce — monsters body-block but are NOT in the collision
	// grid, so every nav watchdog misreads a zombie wall as open ground.
	fightThrough := func(d game.Data, me data.Position) bool {
		best := 1 << 30
		var tgt *data.Monster
		for i := range d.Monsters {
			m := &d.Monsters[i]
			if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead || m.IsGoodNPC() || m.IsPet() || m.IsMerc() {
				continue
			}
			if dd := chebyshev(me, m.Position); dd < best && dd <= 8 {
				best, tgt = dd, m
			}
		}
		if tgt == nil {
			return false
		}
		if *rabies != "" && d.PlayerUnit.MPPercent() >= *meleeBelow {
			sx, sy := gameToScreen(gr, me.X, me.Y, tgt.Position.X, tgt.Position.Y)
			hid.PressKey(hid.GetASCIICode(*rabies))
			time.Sleep(50 * time.Millisecond)
			hid.Click(game.RightButton, sx, sy)
			time.Sleep(150 * time.Millisecond)
			return true
		}
		meleeSwing(tgt.UnitID, tgt.Position)
		return true
	}

	// openBurst: corner escape that reads the room. The old fixed-rotation burst walked INTO
	// walls as often as away from them, so the bot visibly hugged corners while "recovering".
	// Sample the live grid in 8 world directions, score each by contiguous walkable clearance,
	// and walk toward daylight (with a small second-best jitter so a dead-end pocket's single
	// open ray doesn't trap us).
	openBurst := func(me data.Position) {
		type ray struct {
			angle float64
			score int
		}
		rays := make([]ray, 0, 8)
		for k := 0; k < 8; k++ {
			angle := float64(k) / 8.0 * 2 * math.Pi
			dx, dy := math.Cos(angle), math.Sin(angle)
			score := 0
			for step := 1; step <= 14; step++ {
				p := data.Position{X: me.X + int(dx*float64(step)+0.5), Y: me.Y + int(dy*float64(step)+0.5)}
				if navGrid == nil || !navGrid.IsWalkable(p) {
					break
				}
				score++
			}
			rays = append(rays, ray{angle, score})
		}
		sort.Slice(rays, func(i, j int) bool { return rays[i].score > rays[j].score })
		for burst, r := range rays[:2] { // best two open directions
			wdx := int(math.Cos(r.angle) * 20)
			wdy := int(math.Sin(r.angle) * 20)
			sx, sy := screenPointToward(me, wdx, wdy)
			hold := 280 + 120*burst
			walkToHold(sx, sy, hold)
			// SYNCHRONOUS on purpose: with the async hold, the next tick's carrot re-aim
			// overwrote the escape direction within ~10ms and the burst never got a frame
			// of actual walking (measured: 38 wedge stamps at one cell, position frozen —
			// the unstick mechanism itself was dead). An escape pulse OWNS its duration.
			time.Sleep(time.Duration(hold) * time.Millisecond)
		}
	}

	// navWalk: ONE deliberate navigation step toward dest, delegated to the Mover (mover.go) —
	// the movelab-winning LOS-lookahead executor with closed-loop stall handling. Planners are
	// re-pointed every call because nav setup / realign rebuild them. MoveBlocked means the
	// mover exhausted planner fallback + clearance escape — bring violence.
	// wedges: spots where the game refused passage the grid claimed walkable — recorded by
	// the stall watchdogs, cleared on area change. Fed to every planner (see mover.go doc).
	var wedges []data.Position
	var gotoAim data.Position // the travel aim this tick (candidate approach or mapped exit)
	mover := NewMover(gr, nil, nil, walkToHold, screenPointToward, openBurst)
	if *clickMove {
		// THE FISH CURE: far travel rides the game's own pathfinder via ground clicks
		// (retested 2026-07-18 after the DPI fix: exact arrivals, zero thrash — the old
		// 1/4-arrivals refutation was measured before the aim was fixed). Force-move
		// remains for the last tiles, combat micro-steps, and every fallback path.
		mover.SetClickMove(interactClick)
	}
	navWalk := func(me, dest data.Position) {
		mover.live, mover.full = navi, mapNavi
		// Obstacles for every mode: recorded refusals (the game said no) + static colliding
		// objects (chests, stalls, the well — they block but aren't in the tile grid).
		// Hostile monsters are deliberately NOT fed: they chase, so they invalidate every
		// plan the tick it's made — the contact override hands them to combat instead.
		obs := append([]data.Position(nil), wedges...)
		dd := gr.GetData()
		for i := range dd.Objects {
			o := &dd.Objects[i]
			if o.Desc().HasCollision && chebyshev(me, o.Position) < 70 {
				obs = append(obs, o.Position)
			}
		}
		mover.SetWedges(obs)
		if mover.Step(me, dest) == MoveBlocked {
			if fightThrough(gr.GetData(), me) {
				logger.Info("navWalk: blocked — fighting through")
				return
			}
			logger.Warn("navWalk: hard blocked", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
				"dest", fmt.Sprintf("(%d,%d)", dest.X, dest.Y))
		}
	}
	deathsSinceRecovery := 0

	// bigLeftChange counts changed samples in the left 45% of the screen between two shots —
	// the open-shop detector. A full vendor panel changes ~70k samples; wandering pets ~2k.
	bigLeftChange := func(before, after image.Image) int {
		b := after.Bounds()
		n := 0
		for y := 40; y < b.Dy()*55/100; y += 3 { // UPPER-left: the chat panel (bottom-left) must not count
			for x := 20; x < b.Dx()*45/100; x += 3 {
				r1, g1, b1, _ := before.At(x, y).RGBA()
				r2, g2, b2, _ := after.At(x, y).RGBA()
				if abs(int(r1>>8)-int(r2>>8))+abs(int(g1>>8)-int(g2>>8))+abs(int(b1>>8)-int(b2>>8)) > 60 {
					n++
				}
			}
		}
		return n
	}

	// vendorErrand: open Akara's shop (body-click -> Down -> Enter, user-verified keyboard nav),
	// SELL Normal-quality identified clutter by pick-up-and-drop-into-vendor (no ctrl needed —
	// the key stub is single-slot), screenshot the stock for buy-calibration, ESC out.
	vendorErrand := func() bool {
		const akaraID = 148
		// Find her: live unit, else walk toward the measured pen anchor until she loads in.
		anchor := data.Position{X: 5530, Y: 4690} // seed-bound fallback; logged if she never appears
		var ak data.Monster
		found := false
		for i := 0; i < 80; i++ {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			for _, m := range d.Monsters {
				if int(m.Name) == akaraID {
					ak, found = m, true
					break
				}
			}
			if found && chebyshev(me, ak.Position) <= 6 {
				break
			}
			tgt := anchor
			if found {
				tgt = ak.Position
			}
			navWalk(me, tgt)
			time.Sleep(150 * time.Millisecond)
		}
		if !found {
			logger.Warn("vendor: Akara never appeared near the anchor — wrong seed layout?")
			return false
		}
		moveStop()
		time.Sleep(400 * time.Millisecond)
		// Open the shop: body-click until the Down+Enter dance produces a big left-panel change.
		shopOpen := false
		for attempt, off := range [][2]int{{0, -20}, {16, -32}, {28, -44}, {0, -45}, {16, -60}, {24, -28}} {
			if attempt > 0 {
				// The one reproducible success clicked right after a FRESH approach — she
				// wanders, and stale vantage clicks keep missing. Step back and re-approach.
				dd0 := gr.GetData()
				me0 := dd0.PlayerUnit.Position
				away := data.Position{X: me0.X + 8, Y: me0.Y + 8}
				for i := 0; i < 8; i++ {
					navWalk(gr.GetData().PlayerUnit.Position, away)
					time.Sleep(150 * time.Millisecond)
				}
				for i := 0; i < 20; i++ {
					mv := gr.GetData().PlayerUnit.Position
					if chebyshev(mv, ak.Position) <= 5 {
						break
					}
					navWalk(mv, ak.Position)
					time.Sleep(150 * time.Millisecond)
				}
				moveStop()
				time.Sleep(400 * time.Millisecond)
			}
			d := gr.GetData()
			me := d.PlayerUnit.Position
			for _, m := range d.Monsters {
				if int(m.Name) == akaraID {
					ak = m
					break
				}
			}
			base := gr.Screenshot()
			bx, by := gameToScreen(gr, me.X, me.Y, ak.Position.X, ak.Position.Y)
			interactClick(bx+off[0], by+off[1])
			// THE MENU BYTE (UI_base-0xC, wide-window offset 0xF4): the true "menu open"
			// flag, found by diffing user-produced open/closed snapshots. Poll it instead
			// of sleeping blind — a miss is known in 1.2s, not guessed from pixels.
			menuOpen := false
			for i := 0; i < 12; i++ {
				if ub := gr.UIBytes(); len(ub) > 0xF4 && ub[0xF4] == 1 {
					menuOpen = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !menuOpen {
				logger.Info("vendor: body click didn't open the menu", "n", attempt,
					"offset", fmt.Sprintf("(%d,%d)", off[0], off[1]))
				continue
			}
			moveStop()
			// MENUS POLL GETKEYSTATE: a message-only Down never moves the highlight — hold
			// the override around a raw down/up (the force-move lesson applied to menus).
			_ = gi.OverrideGetKeyState(0x28) // VK_DOWN
			_ = gi.OverrideGetAsyncKeyState(0x28)
			hid.RawKeyDown(0x28)
			time.Sleep(220 * time.Millisecond)
			hid.RawKeyUp(0x28)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
			time.Sleep(200 * time.Millisecond)
			hid.PressKey(hid.GetASCIICode("enter"))
			time.Sleep(1000 * time.Millisecond)
			ch := bigLeftChange(base, gr.Screenshot())
			logger.Info("vendor: attempt", "n", attempt, "offset", fmt.Sprintf("(%d,%d)", off[0], off[1]),
				"menuByte", menuOpen, "leftChange", ch)
			if ch > 12000 {
				shopOpen = true
				break
			}
			// TRADE didn't take — collapse whatever is open (esc closes the whole menu
			// stack; safe here because SOMETHING is open, so no pause-menu trap).
			hid.PressKey(hid.GetASCIICode("esc"))
			time.Sleep(300 * time.Millisecond)
		}
		if !shopOpen {
			logger.Warn("vendor: shop never opened")
			return false
		}
		if f, err := os.Create(shotPath("akara_shop.png")); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("vendor: shop OPEN — stock screenshot saved", "path", shotPath("akara_shop.png"))
		}
		// SELL: Normal-quality identified clutter, pick-up-and-drop into the vendor grid.
		sold := 0
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
			if sold >= 8 || it.Quality != item.QualityNormal || !it.Identified {
				continue
			}
			n := string(it.Name)
			if strings.Contains(n, "INVALID") || strings.Contains(n, "Tome") || strings.Contains(n, "Charm") ||
				strings.Contains(n, "Potion") || strings.Contains(n, "Flag") || strings.Contains(n, "Topaz") ||
				n == "" || n == "Jawbone" || n == "Eye" || n == "Scalp" { // mod-remapped tomes/offhands — never again
				continue
			}
			ix, iy := invPixel(it.Position.X, it.Position.Y)
			uiClick(ix, iy) // pick to cursor
			time.Sleep(350 * time.Millisecond)
			uiClick(480, 500) // drop into the vendor grid = sell
			time.Sleep(450 * time.Millisecond)
			still := false
			for _, it2 := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				// UnitIDs may be unstable across reads — match by grid position + name.
				if it2.Position == it.Position && it2.Name == it.Name {
					still = true
					break
				}
			}
			if !still {
				sold++
				logger.Info("vendor: SOLD", "name", n)
			} else {
				logger.Warn("vendor: sell did not take — stopping the sell pass", "name", n)
				break
			}
		}
		// BUY via LEFT clicks: the right-click buy is deaf under every override tried,
		// but every LEFT click works on this shop (the sell pass proves it each trip).
		// D2R buys on pick-from-vendor-grid + place-into-inventory. Experiment target:
		// one spare TP tome from the measured Misc slot. freeCell shadows every item
		// 2x3 from its anchor — conservative, so the place never lands on a body.
		freeCell := func() (int, int, bool) {
			occ := map[[2]int]bool{}
			for _, it := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				for dx := 0; dx < 2; dx++ {
					for dy := 0; dy < 3; dy++ {
						occ[[2]int{it.Position.X + dx, it.Position.Y + dy}] = true
					}
				}
			}
			for y := 7; y >= 0; y-- {
				for x := 0; x < 10; x++ {
					if !occ[[2]int{x, y}] {
						return x, y, true
					}
				}
			}
			return 0, 0, false
		}
		invBefore := len(gr.GetData().Inventory.ByLocation(item.LocationInventory))
		if fx, fy, okc := freeCell(); okc {
			uiClick(178, 240) // Misc stock: TP tome slot (measured from akara_shop.png)
			time.Sleep(500 * time.Millisecond)
			px, py := invPixel(fx, fy)
			uiClick(px, py)
			time.Sleep(500 * time.Millisecond)
			invAfter := len(gr.GetData().Inventory.ByLocation(item.LocationInventory))
			logger.Info("vendor: BUY experiment (left-click pick+place)",
				"before", invBefore, "after", invAfter, "cell", fmt.Sprintf("(%d,%d)", fx, fy))
		} else {
			logger.Info("vendor: no free inventory cell — buy skipped")
		}
		moveStop()
		hid.PressKey(hid.GetASCIICode("esc")) // close the shop
		time.Sleep(400 * time.Millisecond)
		logger.Info("vendor: errand done", "sold", sold)
		return true
	}

	// runStepCommand: the STEP-MODE calibration harness. Each command executes ONE primitive,
	// then saves shots/step.png + logs position/area so the orchestrator can LOOK before the
	// next step (no more guess-sequences baked into rebuilds). Driven via the control file:
	//   echo hold > logs/control.txt          freeze behaviors
	//   echo "step shot" > logs/control.txt   screenshot only
	//   echo "step ui 1005,197"               panel click (raw physical)
	//   echo "step rui 1360,417"              panel RIGHT-click (raw physical)
	//   echo "step click 800,400"             world interactClick (client coords)
	//   echo "step key down"                  press a key by name
	//   echo "step npc 148,0,-20"             click a live unit's body+offset
	//   echo "step walk 5530,4690"            one navWalk burst toward world point
	//   echo resume                           release
	// stepResult accumulates command outcomes; flushed to logs/step_result.txt so the
	// orchestrator waits on the FILE instead of sleeping blind and grepping the log.
	var stepResult strings.Builder
	finishStepResult = func() {
		_ = os.WriteFile(filepath.Join("logs", "step_result.txt"), []byte(stepResult.String()), 0644)
		stepResult.Reset()
	}
	runStepBatch = func(cmd string, idx int) {
		fields := strings.SplitN(cmd, " ", 2)
		verb := fields[0]
		arg := ""
		if len(fields) > 1 {
			arg = fields[1]
		}
		var a, b, c int
		switch verb {
		case "shot":
			// screenshot below
		case "ui":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				uiClick(a, b)
			}
		case "rui":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				uiRightClick(a, b)
			}
		case "click":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				interactClick(a, b)
			}
		case "key":
			moveStop()
			hid.PressKey(hid.GetASCIICode(strings.TrimSpace(arg)))
		case "npc":
			if n, _ := fmt.Sscanf(arg, "%d,%d,%d", &a, &b, &c); n == 3 {
				d := gr.GetData()
				me := d.PlayerUnit.Position
				for _, m := range d.Monsters {
					if int(m.Name) == a {
						bx, by := gameToScreen(gr, me.X, me.Y, m.Position.X, m.Position.Y)
						logger.Info("step: npc body", "bodyScreen", fmt.Sprintf("(%d,%d)", bx, by),
							"click", fmt.Sprintf("(%d,%d)", bx+b, by+c))
						interactClick(bx+b, by+c)
						break
					}
				}
			}
		case "walk":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				me := gr.GetData().PlayerUnit.Position
				navWalk(me, data.Position{X: a, Y: b})
			}
		case "aim":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				moveStop()
				hid.AimPhysical(a, b)
				if *realCursor {
					win.SetCursorPos(int32(gr.WindowLeftX+a), int32(gr.WindowTopY+b))
				}
			}
		case "rawclick":
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				_ = gi.OverrideGetKeyState(0x01)
				_ = gi.OverrideGetAsyncKeyState(0x01)
				hid.LeftClickNoMove(a, b)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
			}
		case "ruii":
			// Right-click with GetCursorInfo aim — the shop may hit-test via the one cursor
			// export the default panel aim doesn't override.
			if n, _ := fmt.Sscanf(arg, "%d,%d", &a, &b); n == 2 {
				moveStop()
				px := int(float64(gr.WindowLeftX)*(*dpiScale)) + a
				py := int(float64(gr.WindowTopY)*(*dpiScale)) + b
				_ = gi.OverrideGetCursorInfo(px, py)
				_ = gi.OverridePhysicalCursorPos(px, py)
				time.Sleep(250 * time.Millisecond)
				_ = gi.OverrideGetKeyState(0x02)
				_ = gi.OverrideGetAsyncKeyState(0x02)
				hid.Click(game.RightButton, a, b)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
				time.Sleep(200 * time.Millisecond)
				_ = gi.RestoreGetCursorInfo()
				_ = gi.RestorePhysicalCursorPos()
			}
		case "keyhold":
			// Hold a key via the GetKeyState OVERRIDE (menus poll key state — synthetic
			// messages move nothing; this is the force-move lesson applied to menu nav).
			parts := strings.Fields(arg)
			if len(parts) >= 1 {
				ms := 200
				if len(parts) >= 2 {
					fmt.Sscanf(parts[1], "%d", &ms)
				}
				vk := hid.GetASCIICode(parts[0])
				moveStop()
				_ = gi.OverrideGetKeyState(vk)
				_ = gi.OverrideGetAsyncKeyState(vk)
				hid.RawKeyDown(vk)
				time.Sleep(time.Duration(ms) * time.Millisecond)
				hid.RawKeyUp(vk)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
			}
		case "mclick":
			// Modified panel click, SINGLE-key stubs: gks/gaks each read one key as held
			// around a client-coord click message (0 = leave that stub disabled). The matrix
			// probe for ctrl+click quick-sell: which poll does the modifier check read?
			//   mclick x,y,17,17  ctrl on both polls (does the click even register without 0x01?)
			//   mclick x,y,1,17   LButton on GetKeyState, ctrl on GetAsyncKeyState
			//   mclick x,y,17,1   the reverse
			var g1, g2 int
			if n, _ := fmt.Sscanf(arg, "%d,%d,%d,%d", &a, &b, &g1, &g2); n >= 3 {
				moveStop()
				aimPanel(a, b)
				hid.MouseMoveClient(a, b)
				time.Sleep(150 * time.Millisecond)
				if g1 != 0 {
					_ = gi.OverrideGetKeyState(byte(g1))
				}
				if g2 != 0 {
					_ = gi.OverrideGetAsyncKeyState(byte(g2))
				}
				time.Sleep(60 * time.Millisecond)
				hid.LeftClickNoMoveClient(a, b)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
			}
		case "mclick2":
			// Modified panel click, TWO-key stubs on BOTH polls: k1 and k2 all read held
			// everywhere — the full ctrl+click simulation (game sees VK_LBUTTON + VK_CONTROL
			// no matter which export it polls).  mclick2 x,y,1,17
			var k1, k2 int
			if n, _ := fmt.Sscanf(arg, "%d,%d,%d,%d", &a, &b, &k1, &k2); n == 4 {
				moveStop()
				aimPanel(a, b)
				hid.MouseMoveClient(a, b)
				time.Sleep(150 * time.Millisecond)
				_ = gi.OverrideGetKeyState2(byte(k1), byte(k2))
				_ = gi.OverrideGetAsyncKeyState2(byte(k1), byte(k2))
				time.Sleep(60 * time.Millisecond)
				hid.LeftClickNoMoveClient(a, b)
				_ = gi.RestoreGetKeyState2()
				_ = gi.RestoreGetAsyncKeyState2()
			}
		case "objects":
			dd := gr.GetData()
			mee := dd.PlayerUnit.Position
			for _, o := range dd.Objects {
				if chebyshev(mee, o.Position) <= 40 {
					stepResult.WriteString(fmt.Sprintf("obj name=%d id=%d sel=%v portal=%v pos=(%d,%d) dist=%d\n",
						int(o.Name), int(o.ID), o.Selectable, o.IsPortal(),
						o.Position.X, o.Position.Y, chebyshev(mee, o.Position)))
				}
			}
		case "uibytes":
			nm := strings.TrimSpace(arg)
			if nm == "" {
				nm = "uibytes"
			}
			ub := gr.UIBytes()
			_ = os.WriteFile(filepath.Join("logs", nm+".bin"), ub, 0644)
			stepResult.WriteString("uibytes saved logs/" + nm + ".bin" + string(rune(10)))
		case "skills":
			// Dump the live skill/binding reads — the ground truth for deriveCombat. If the
			// slot list is garbage on 3.2, the bindings block has the Life-stat disease.
			dd := gr.GetData()
			p := dd.PlayerUnit
			stepResult.WriteString(fmt.Sprintf("class=%v left=%d right=%d mp=%d%%\n",
				p.Class, int(p.LeftSkill), int(p.RightSkill), p.MPPercent()))
			for id, pts := range p.Skills {
				stepResult.WriteString(fmt.Sprintf("skill id=%d lvl=%d\n", int(id), pts.Level))
			}
			for i, sb := range dd.KeyBindings.Skills {
				if sb.SkillID != 0 || sb.Key1[0] != 0 {
					stepResult.WriteString(fmt.Sprintf("bind slot=%d skill=%d key1=[%d %d] key2=[%d %d]\n",
						i, int(sb.SkillID), sb.Key1[0], sb.Key1[1], sb.Key2[0], sb.Key2[1]))
				}
			}
		case "inv":
			loc := item.LocationType(strings.TrimSpace(arg))
			for _, it := range gr.GetData().Inventory.ByLocation(loc) {
				il := fmt.Sprintf("item loc=%s body=%s name=%q pos=(%d,%d) quality=%s identified=%v\n",
					string(loc), string(it.Location.BodyLocation), string(it.Name), it.Position.X, it.Position.Y, string(it.Quality), it.Identified)
				stepResult.WriteString(il)
				logger.Info("step: item", "loc", string(loc), "name", string(it.Name),
					"pos", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y))
			}
		default:
			logger.Warn("step: unknown verb", "verb", verb)
			return
		}
		time.Sleep(700 * time.Millisecond)
		shotName := "step.png"
		if idx > 0 {
			shotName = fmt.Sprintf("step_%d.png", idx)
		}
		if f, err := os.Create(shotPath(shotName)); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
		}
		d := gr.GetData()
		line := fmt.Sprintf("done [%s] pos=(%d,%d) area=%d shot=%s\n", cmd,
			d.PlayerUnit.Position.X, d.PlayerUnit.Position.Y, int(d.PlayerUnit.Area), shotName)
		stepResult.WriteString(line)
		logger.Info("step: done", "cmd", cmd, "shot", shotName)
	}
	runStepCommand = func(cmd string) {
		runStepBatch(cmd, 0)
		finishStepResult()
	}



	// Loot filter: what's worth walking to. Quality reads work on 3.2 (verified for player
	// stats; item quality is the same statlist machinery). Potions count as needed only while
	// the belt is short on that type — half the belt (2 columns) reserved per type.
	beltShortOn := func(d game.Data, pt data.PotionType) bool {
		n := 0
		for _, p := range d.Inventory.Belt.Items {
			if strings.Contains(string(p.Name), string(pt)) {
				n++
			}
		}
		return n < 2*d.Inventory.Belt.Rows()
	}
	lootWorthy := func(d game.Data, it data.Item) bool {
		switch it.Quality {
		case item.QualityUnique, item.QualitySet, item.QualityRare, item.QualityCrafted:
			return true
		}
		n := strings.ToLower(string(it.Name))
		if n == "gold" {
			return true
		}
		if strings.Contains(n, "rejuvenation") {
			return true // rejuvs always — they restore both pools and stack anywhere
		}
		if strings.Contains(n, "healingpotion") {
			return beltShortOn(d, data.HealingPotion)
		}
		if strings.Contains(n, "manapotion") {
			return beltShortOn(d, data.ManaPotion)
		}
		if strings.Contains(n, "herb") {
			// The mod's HP consumable — grab while the belt has room.
			return len(d.Inventory.Belt.Items) < 4*d.Inventory.Belt.Rows()
		}
		return false
	}
	nearAny := func(p data.Position, list []data.Position, r int) bool {
		for _, q := range list {
			if chebyshev(p, q) < r {
				return true
			}
		}
		return false
	}
	// INTENT LEDGER (the user's queue): one JOURNEY at a time. The loop re-decides every
	// tick with priorities implicit in block order — fine for instant actions (a drink,
	// a summon cast: 200ms, no steering), but two WALK-owning behaviors alternating wins
	// is exactly the back-and-forth thrash. A claim grants the walk to one intent for a
	// hold window; a different intent takes it early only by strictly outranking the
	// holder (ties would flap). Wired so far: critloot(70) > combat contact(60) > combat
	// engage(50) > loot(30) > objects(25). Survival drinking and corpse recovery run
	// above/outside the ledger. Every transition is logged — the tuning ledger.
	curIntent, curPrio := "", 0
	var intentUntil time.Time
	claim := func(name string, prio int, hold time.Duration) bool {
		now := time.Now()
		if name != curIntent && now.Before(intentUntil) && prio <= curPrio {
			return false
		}
		if name != curIntent {
			logger.Info("intent", "from", curIntent, "to", name, "prio", prio)
		}
		curIntent, curPrio, intentUntil = name, prio, now.Add(hold)
		return true
	}
	// groundHeal mirrors beltHealPos generosity for GROUND items: vanilla heals, rejuvs,
	// and the mod's Herb all count — the critical-pickup rule cares about drinkability,
	// not species.
	groundHeal := func(it data.Item) bool {
		n := strings.ToLower(string(it.Name))
		return strings.Contains(n, "healingpotion") || strings.Contains(n, "rejuvenation") ||
			strings.Contains(n, "herb")
	}
	// DAMAGE WATCHDOG state. In melee the ONLY give-up path was stuckCount — which the bite branch
	// resets on every tick — so a monster we cannot hurt was bitten until the run timer expired.
	// Track the engaged target's Life instead and bail when it stops dropping. Deliberately
	// cause-agnostic: poison immunity is the known case, but this also catches phasing, invuln
	// auras, healing, mod-added immunities, and whatever else we haven't thought of yet.
	var engagedID data.UnitID
	var engagedLife int
	var engagedAt time.Time
	var lastCurseAt time.Time   // curse cooldown — one Amp cast covers a pack for ~8s
	var lastUnstickAt time.Time // unstick pacing — see the freeze-breaker block
	var lastTownLog time.Time
	// packAssess is the combat oracle's arithmetic as a single authority: weighted enemy mass
	// within judgment range vs the army + our pools. Shared by the farm posture and the
	// travel fight-on-the-way gate.
	packAssess := func(d game.Data, me data.Position) (threat, strength, packN int, centroid data.Position) {
		for i := range d.Monsters {
			m := &d.Monsters[i]
			if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead || m.IsGoodNPC() || m.IsPet() || m.IsMerc() {
				continue
			}
			if dd := chebyshev(me, m.Position); dd <= 30 {
				w := 1
				if m.IsElite() {
					w += 2
				}
				if m.IsMonsterRaiser() {
					w += 2
				}
				threat += w
				centroid.X += m.Position.X
				centroid.Y += m.Position.Y
				packN++
			}
		}
		if packN > 0 {
			centroid.X /= packN
			centroid.Y /= packN
		}
		petsAlive := 0
		for i := range d.Monsters {
			if d.Monsters[i].IsPet() {
				petsAlive++
			}
		}
		strength = petsAlive*3 + 2 // the char himself is worth a couple of zombies
		if d.PlayerUnit.HPPercent() > 60 {
			strength += 2
		}
		return
	}
	var travelFightLog time.Time
	controlFile := filepath.Join("logs", "control.txt")
	_ = os.Remove(controlFile) // stale command from a previous session must not kill this run
	var lastControlCheck time.Time
mainLoop:
	for time.Now().Before(deadline) {
		// GLOBAL TICK FLOOR: continuous locomotion made every walk call non-blocking, so
		// any `walk; continue` branch spins the loop at CPU speed — unstick did 13,807
		// lines in one pocket, then the loot approach spun at 3ms the moment that was
		// paced. Whack-a-mole ends here: the loop has no business deciding faster than
		// the character moves. 80ms floor (~12Hz); the held force-move walks through it,
		// and slow branches (bites, panels) are unaffected.
		if dt := time.Since(lastTick); dt < 80*time.Millisecond {
			time.Sleep(80*time.Millisecond - dt)
		}
		lastTick = time.Now()
		// CONTROL CHANNEL: `echo exit > logs/control.txt` requests a graceful shutdown NOW —
		// the same atlas-save + deferred input-heal path as the -seconds deadline, so deploys
		// no longer wait out the clock (force-kill while D2R lives corrupts input patching;
		// this is the safe alternative).
		controlEvery := 2 * time.Second
		if stepHold {
			controlEvery = 250 * time.Millisecond // step mode: snappy command latency
		}
		if time.Since(lastControlCheck) > controlEvery {
			lastControlCheck = time.Now()
			if b, err := os.ReadFile(controlFile); err == nil {
				_ = os.Remove(controlFile)
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				if len(lines) > 1 { // batch: step commands only, numbered screenshots + one result file
					_ = os.Remove(filepath.Join("logs", "step_result.txt"))
					for bi, ln := range lines {
						ln = strings.TrimSpace(ln)
						if strings.HasPrefix(ln, "step ") {
							runStepBatch(strings.TrimPrefix(ln, "step "), bi+1)
						}
					}
					finishStepResult()
					continue
				}
				cmd := lines[0]
				switch cmd {
				case "exit":
					logger.Info("control: graceful exit requested")
					break mainLoop
				case "tp":
					if *tp == "" {
						logger.Warn("control: tp requested but -tp key not set")
					} else if tpPhase == 0 {
						logger.Info("control: town round-trip requested")
						tpPhase, tpTripStart, tpParkInTown, tpErrandsDone = 1, time.Now(), false, false
					}
				case "akara":
					// Town trip with the full errand suite (identify + vendor sell/screenshot).
					if *tp == "" {
						logger.Warn("control: akara requested but -tp key not set")
					} else if tpPhase == 0 {
						logger.Info("control: akara errand trip requested")
						tpPhase, tpTripStart, tpParkInTown, tpErrandsDone = 1, time.Now(), false, false
						tpVendorWanted = true
					}
				case "cube":
					logger.Info("control: cube stash requested")
					cubeStash()
				case "town":
					// TP to town and PARK (no auto-return) — the calibration-window maker:
					// get him to town, then 'exit' for a clean supervised probe session.
					if *tp == "" {
						logger.Warn("control: town requested but -tp key not set")
					} else if tpPhase == 0 {
						logger.Info("control: town-and-park requested")
						tpPhase, tpTripStart, tpParkInTown, tpErrandsDone = 1, time.Now(), true, false
					}
				case "hold":
					stepHold = true
					logger.Info("control: STEP MODE — behaviors frozen; 'step ...' commands accepted; 'resume' releases")
				case "resume":
					stepHold = false
					logger.Info("control: step mode released")
				case "":
				default:
					if strings.HasPrefix(cmd, "step ") {
						runStepCommand(strings.TrimSpace(strings.TrimPrefix(cmd, "step ")))
					} else {
						logger.Warn("control: unknown command", "cmd", cmd)
					}
				}
			}
		}
		if stepHold {
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if shiftCheck%6 == 0 && !inWerewolf() && chickenStreak == 0 {
			castSelf(*werewolf)
		}
		shiftCheck++

		d := gr.GetData()
		me := d.PlayerUnit.Position

		// TOWN TRIP: cast TP -> enter -> town (errand hook) -> return through the same portal.
		// Every step verifies by observed area/object change; every phase is time-boxed.
		if tpPhase != 0 {
			nearestPortal := func(r int) (data.Object, bool) {
				best, bd := data.Object{}, r+1
				for _, o := range d.Objects {
					if (o.IsPortal() || o.IsRedPortal()) && chebyshev(me, o.Position) < bd {
						best, bd = o, chebyshev(me, o.Position)
					}
				}
				return best, bd <= r
			}
			switch tpPhase {
			case 1: // cast at our feet
				if d.PlayerUnit.Area.IsTown() {
					logger.Info("towntrip: already in town — running errands only (no portal return)")
					tpOrigArea = 0 // sentinel: phase 3 ends without a return trip
					tpPhaseAt, tpPhase = time.Now(), 3
					continue
				}
				tpOrigArea = int(d.PlayerUnit.Area)
				castSelf(*tp)
				logger.Info("towntrip: cast", "area", tpOrigArea, "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
				tpPhaseAt, tpPhase, tpRecast = time.Now(), 2, false
			case 2: // the blue portal should appear beside us — click through it
				if d.PlayerUnit.Area.IsTown() {
					logger.Info("towntrip: arrived in town", "tookS", int(time.Since(tpTripStart).Seconds()))
					tpPhaseAt, tpPhase = time.Now(), 3
					continue
				}
				if p, ok := nearestPortal(20); ok {
					if !hoverPickClick(p.Position, p.ID) {
						sx, sy := gameToScreen(gr, me.X, me.Y, p.Position.X, p.Position.Y)
						interactClick(sx, sy)
					}
					time.Sleep(600 * time.Millisecond)
				} else if time.Since(tpPhaseAt) > 5*time.Second && !tpRecast {
					logger.Warn("towntrip: no portal appeared — recasting once")
					castSelf(*tp)
					tpPhaseAt, tpRecast = time.Now(), true
				} else if time.Since(tpPhaseAt) > 10*time.Second {
					// "I can't." — the mod REFUSES town portals in some areas (screenshot-
					// verified in the Den: red tome icon + the refusal chat line; vanilla
					// allows it there). Not a casting failure — an area rule. Fall back to
					// FEET: the goto machinery hikes to town, errands run, then hike back.
					logger.Warn("towntrip: no portal appeared (area TP-restricted?) — WALKING to town")
					tpWalkBack = tpOrigArea
					curGoto = 1 // Rogue Encampment; the hop router charts the exits
					tpPhaseAt, tpPhase = time.Now(), 4
				}
			case 3: // in town: run the errands, then return through our portal.
				if tpOrigArea == 0 && tpErrandsDone {
					logger.Info("towntrip: errands-only trip complete")
					tpPhase = 0
					continue
				}
				if tpParkInTown {
					logger.Info("towntrip: PARKED in town per control command")
					tpPhase, tpParkInTown = 0, false
					continue
				}
				if !tpErrandsDone {
					tpErrandsDone = true
					if n := identifyErrand(); n > 0 {
						logger.Info("towntrip: errands", "identified", n)
					}
					cubeStash() // keepers into the cube before the sell pass sees them
					if tpVendorWanted {
						tpVendorWanted = false
						vendorErrand()
					}
				}
				if tpWalkBack != 0 {
					// Walked trip: there is no portal to return through — hike back out.
					logger.Info("towntrip: errands done — walking back out", "area", tpWalkBack)
					curGoto = tpWalkBack
					tpPhaseAt, tpPhase = time.Now(), 5
					continue
				}
				if int(d.PlayerUnit.Area) == tpOrigArea {
					logger.Info("towntrip: ROUND TRIP COMPLETE",
						"totalS", int(time.Since(tpTripStart).Seconds()))
					tpPhase = 0
					continue
				}
				if time.Since(tpPhaseAt) < 2*time.Second {
					time.Sleep(200 * time.Millisecond) // let the town load settle
					continue
				}
				if time.Since(tpPhaseAt) > 25*time.Second {
					logger.Warn("towntrip: could not return through portal — staying in town for the guard")
					tpPhase = 0
					continue
				}
				if p, ok := nearestPortal(30); ok {
					if chebyshev(me, p.Position) > 5 {
						navWalk(me, p.Position)
						continue
					}
					if !hoverPickClick(p.Position, p.ID) {
						sx, sy := gameToScreen(gr, me.X, me.Y, p.Position.X, p.Position.Y)
						interactClick(sx, sy)
					}
					time.Sleep(600 * time.Millisecond)
				}
			case 4: // WALK-FALLBACK inbound: the goto machinery does the walking on this
				// tick (this phase does NOT consume it) — only watch for arrival. PIN the
				// destination every tick: goto failure paths reset curGoto to 0 and the
				// autoprogress block then re-asserts the farm route — which is exactly how
				// the first walk-to-town died two minutes in (targetArea flipped 1 -> 4).
				curGoto = 1
				// THE BAN IS PER-AREA: the moment the walk crosses out of the refusing
				// cave (Den -> Blood Moor), portals are legal again — recast and ride,
				// instead of hiking the remaining two areas on foot.
				if !d.PlayerUnit.Area.IsTown() && int(d.PlayerUnit.Area) != tpWalkBack &&
					time.Since(tpPhaseAt) > 3*time.Second {
					logger.Info("towntrip: left the TP-banned area — retrying the portal",
						"area", int(d.PlayerUnit.Area))
					tpOrigArea, tpWalkBack = int(d.PlayerUnit.Area), 0
					tpPhaseAt, tpPhase = time.Now(), 1
					continue
				}
				if d.PlayerUnit.Area.IsTown() {
					logger.Info("towntrip: reached town ON FOOT", "tookS", int(time.Since(tpTripStart).Seconds()))
					curGoto = 0
					tpPhaseAt, tpPhase = time.Now(), 3
					continue
				}
				if time.Since(tpTripStart) > 5*time.Minute {
					logger.Warn("towntrip: walk to town timed out — abandoning trip")
					curGoto = *gotoArea
					tpPhase, tpWalkBack = 0, 0
				}
			case 5: // WALK-FALLBACK outbound: same deal, feet belong to the goto machinery.
				curGoto = tpWalkBack // pinned against goto-failure resets, same as phase 4
				if int(d.PlayerUnit.Area) == tpWalkBack {
					logger.Info("towntrip: ROUND TRIP COMPLETE (on foot)",
						"totalS", int(time.Since(tpTripStart).Seconds()))
					curGoto = *gotoArea
					tpPhase, tpWalkBack = 0, 0
					continue
				}
				if time.Since(tpTripStart) > 12*time.Minute {
					logger.Warn("towntrip: walk back timed out — releasing to autoprogress")
					curGoto = *gotoArea
					tpPhase, tpWalkBack = 0, 0
				}
			}
			if tpPhase != 4 && tpPhase != 5 {
				continue
			}
		}

		// TOWN GUARD. Nothing stopped the farm loop from hunting and exploring in town: it would
		// pre-buff, shapeshift, and go looking for targets in Lut Gholein. Enemy filtering leans on
		// Monsters.Enemies() -> IsGoodNPC(), which is a HARDCODED VANILLA npc id list — precisely
		// the "ZERO vanilla assumptions" this file claims not to make. If the mod remapped a
		// townsfolk id, Charsi becomes a valid target. Area.IsTown() is read live from the game and
		// cannot be wrong about the mod. -goto is exempt: leaving town is the one town errand the
		// loop currently knows how to run.
		// AUTOPROGRESS-FROM-TOWN: the guard below parks any run that STARTS in town — curGoto
		// is 0 and the autoprogress block lives AFTER this guard's `continue`, so it never gets
		// to assert a target. (Cost a night: once an errand session left the char at the
		// campfire, every subsequent run began parked and ended parked.) Assert the route
		// target here, before the guard reads curGoto.
		if d.PlayerUnit.Area.IsTown() && curGoto == 0 && *autoProgress && *gotoArea == 0 {
			for routeIdx < len(progressRoute)-1 && progressRoute[routeIdx] == 8 &&
				d.Quests[quest.Act1DenOfEvil].Completed() {
				routeIdx++
				_ = os.WriteFile(routeStateFile, []byte(fmt.Sprintf("%d", routeIdx)), 0644)
				logger.Info("autoprogress: Den quest complete — skipping the Den", "next", progressRoute[routeIdx])
			}
			curGoto, progressTarget = progressRoute[routeIdx], progressRoute[routeIdx]
			logger.Info("autoprogress: starting in town — heading out", "area", curGoto, "routeIdx", routeIdx)
		}
		if d.PlayerUnit.Area.IsTown() && curGoto == 0 {
			// NAKED-IN-TOWN = a death this process never saw (probe-time death, crash, esc by
			// hand). The corpse holds the gear and Corpse.Found can't see across areas, so hunt:
			// walk the adjacent non-town areas until the corpse turns up. At current progression
			// that's one hop; the corpse block preempts the moment Found flips.
			if len(d.Inventory.ByLocation(item.LocationEquipped)) == 0 {
				if hunt := nakedCorpseHunt(d); hunt != 0 {
					logger.Warn("naked in town with no goto — corpse hunt", "tryArea", int(hunt))
					curGoto = int(hunt)
					continue
				}
			}
			if time.Since(lastTownLog) > 10*time.Second {
				logger.Info("in town — combat/explore suppressed (pass -goto <area> to travel out)",
					"area", int(d.PlayerUnit.Area), "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
				lastTownLog = time.Now()
			}
			time.Sleep(400 * time.Millisecond)
			continue
		}

		// Gait: WALK when any enemy is close (running = 0 defense), RUN when the area is clear
		// so travel/exploration stays fast. Also makes engagement more deliberate.
		ensureGait(!anyEnemyWithin(d, me, *dangerRange))

		switch survivalTick(d) {
		case StatusGameGone:
			// Distinct from death on purpose: this is the signal a resumable orchestrator needs to
			// tell "relaunch and continue" from "the run is over", and it keeps crash postmortems
			// honest. Do NOT touch D2R's memory on the way out — there is no D2R.
			logger.Error("D2R IS GONE — game exited/crashed (this is NOT a character death)",
				"lastPos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
				"lastArea", int(d.PlayerUnit.Area),
				"hint", "run crashwatch.ps1 for the exit code")
			break mainLoop
		case StatusDead:
			if *hardcore {
				logger.Error("DEAD (hardcore) — stopping", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
				break mainLoop
			}
			deathsSinceRecovery++
			if deathsSinceRecovery >= 5 {
				logger.Error("5 deaths without a successful corpse recovery — stopping so the night isn't a death loop")
				break mainLoop
			}
			deathArea := int(d.PlayerUnit.Area)
			logger.Warn("DEAD — softcore recovery begins", "area", deathArea,
				"pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
			time.Sleep(2500 * time.Millisecond) // death animation / screen settle
			respawnKey := ""
			for _, k := range []string{"esc", "enter", "esc"} {
				moveStop()
				hid.PressKey(hid.GetASCIICode(k))
				logger.Info("death: respawn input sent", "key", k)
				for w := 0; w < 30; w++ {
					time.Sleep(200 * time.Millisecond)
					d2 := gr.GetData()
					m2 := d2.PlayerUnit.Mode
					if d2.PlayerUnit.HPPercent() > 0 && m2 != mode.Death && m2 != mode.Dead &&
						d2.PlayerUnit.Area.IsTown() {
						respawnKey = k
						break
					}
				}
				if respawnKey != "" {
					break
				}
			}
			if respawnKey == "" {
				d2 := gr.GetData()
				logger.Error("death: NO respawn input worked — stopping for postmortem",
					"mode", int(d2.PlayerUnit.Mode), "hp", d2.PlayerUnit.HPPercent(),
					"area", int(d2.PlayerUnit.Area))
				break mainLoop
			}
			logger.Info("death: respawned in town", "via", respawnKey)
			// Rebuild what needs no corpse BEFORE marching out: the golem is free company for
			// the naked corpse-run (summons died with us; skeletons must wait for field corpses).
			if *golemKey != "" {
				time.Sleep(700 * time.Millisecond) // let the town load settle before casting
				castSelf(*golemKey)
				logger.Info("death: golem raised for the corpse-run")
			}
			_ = os.WriteFile(deathStateFile, []byte(fmt.Sprintf("%d %d %d", deathArea, me.X, me.Y)), 0644)
			deathHuntArea, deathHuntPos = deathArea, me
			curGoto = deathArea // ride the goto machinery back to the corpse
			targetID, engagedID = 0, 0
			chickenStreak = 0
			tpDone = false
			continue
		case StatusChicken:
			// SOFTCORE ESCAPE VALVE: with no potions to drink and no enemy in danger range,
			// fleeing forever just freezes the run at low HP (no natural regen). Resume normal
			// behavior instead — if it ends in death, death recovery respawns at FULL hp, so
			// death is effectively the healing mechanic for a broke naked character.
			if !*hardcore {
				_, hasHeal := beltHealPos(d)
				// NO drinkables -> NO chicken, unconditionally. The earlier threat-gated version
				// produced a 4-HOUR limit cycle at the dangerRange boundary: chase pulls the
				// monster into range -> chicken flees -> monster leaves range -> valve resumes ->
				// chase again; hp pinned at 20, zero kills, zero deaths, zero progress. A broke
				// softcore char just fights: worst case is death, and death respawns at FULL hp.
				if !hasHeal {
					if time.Since(lastTownLog) > 10*time.Second {
						logger.Warn("low HP, no potions — chicken disabled (softcore: death respawn is the healer)")
						lastTownLog = time.Now()
					}
					chickenStreak = 0
					break
				}
			}
			chickenStreak++
			targetID = 0
			nearestDist := 1 << 30
			var nearestMon data.Monster
			haveMon := false
			for _, m := range d.Monsters.Enemies() {
				if m.Stats[stat.Life] <= 0 {
					continue
				}
				if dd := chebyshev(me, m.Position); dd < nearestDist {
					nearestDist, nearestMon, haveMon = dd, m, true
				}
			}
			dx, dy := 0, 0
			if haveMon {
				dx, dy = me.X-nearestMon.Position.X, me.Y-nearestMon.Position.Y
			}
			// Bias the flee vector toward open ground so we don't run into a wall/corner.
			dx += (walkCentroid.X - me.X) / 4
			dy += (walkCentroid.Y - me.Y) / 4
			sx, sy := screenPointToward(me, dx, dy)
			logger.Info("chicken: fleeing", "hp", d.PlayerUnit.HPPercent(),
				"pos", fmt.Sprintf("(%d,%d)", me.X, me.Y), "streak", chickenStreak)
			walkToHold(sx, sy, 320)
			// Merc: note-only for now — d.MercHPPercent() could gate a separate merc-rescue/TP
			// path, but the run-away below already pulls the merc along; not implemented.
			if *tp != "" && chickenStreak >= 3 && !tpDone {
				if inWerewolf() {
					castSelf(*werewolf) // toggle out of werewolf form so the TP tome can be selected
				}
				moveStop()
				hid.PressKey(hid.GetASCIICode(*tp))
				time.Sleep(250 * time.Millisecond)
				hid.Click(game.RightButton, cx, cy)
				tpDone = true
			}
			continue
		default:
			chickenStreak, tpDone = 0, false
		}

		// Crossed into a new area (e.g. walked through a gate)? Re-align to the new level's live
		// origin and rebuild the walkable set — this is what makes multi-area travel work.
		if navGrid != nil && int(d.PlayerUnit.Area) != alignedArea {
			if *objects {
				_ = gr.FetchMapData() // repopulate d.Objects for the new area
			}
			if g, ok := acquireGrid(); ok {
				navGrid = g
				navi = NewNavigator(g)
				alignedArea = int(d.PlayerUnit.Area)
				walkables, walkCentroid = computeWalk(g)
				buildMapNavi()
				posHist, exploreDest, badDests, gotoCross = nil, data.Position{}, nil, nil
				wedges, gotoAim = nil, data.Position{}
				exploreBestDist, exploreBestAt = 1<<30, time.Now()
				exitSeekExit, exitSeekBestDist = data.Position{}, 1<<30
				exitDetourDest, exitDetourUntil = data.Position{}, time.Time{}
				gotoIdx = 0
				entranceContactStart = time.Time{} // transitioned — reset the contact timer
				progressAt = time.Now()
				logger.Info("nav: re-aligned new area", "area", alignedArea,
					"origin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY), "walkables", len(walkables))
			}
		}

		// AUTO-PROGRESS: farm along the act-1 route, advancing when the current area runs dry
		// (no enemy inside the farm radius for ~2.5 min while standing in the target area).
		// Route position persists across runs so a restart doesn't re-clear the Den. Never
		// fights the death recovery's retarget: it only asserts when curGoto is idle or already
		// the progression target.
		if *autoProgress && *gotoArea == 0 && tpPhase == 0 {
			if anyEnemyWithin(d, me, *radius) {
				lastEnemySeen = time.Now()
			}
			if curGoto == 0 || curGoto == progressTarget {
				// QUEST-AWARE ROUTE (user: "be aware of what's clear"): the Den has a
				// readable completion flag — never revisit a cleared quest cave. Other
				// stops rely on the runs-dry timer; their "done" isn't a game fact.
				for routeIdx < len(progressRoute)-1 && progressRoute[routeIdx] == 8 &&
					d.Quests[quest.Act1DenOfEvil].Completed() {
					routeIdx++
					_ = os.WriteFile(routeStateFile, []byte(fmt.Sprintf("%d", routeIdx)), 0644)
					logger.Info("autoprogress: Den quest complete — skipping the Den", "next", progressRoute[routeIdx])
				}
				if int(d.PlayerUnit.Area) == progressTarget && progressTarget != 0 &&
					time.Since(lastEnemySeen) > 150*time.Second && routeIdx < len(progressRoute)-1 {
					routeIdx++
					_ = os.WriteFile(routeStateFile, []byte(fmt.Sprintf("%d", routeIdx)), 0644)
					logger.Info("autoprogress: area exhausted — advancing", "to", progressRoute[routeIdx])
					lastEnemySeen = time.Now()
				}
				want := progressRoute[routeIdx]
				if want != progressTarget {
					logger.Info("autoprogress: farming target", "area", want, "routeIdx", routeIdx)
				}
				curGoto, progressTarget = want, want
			}
		}

		// CRITICAL PICKUP (the user's priority rule): an EMPTY belt turns a ground heal into
		// the highest journey there is — above engaging (a fight with no heals is how deaths
		// happen), below true contact (never walk through a swing for a bottle) and below
		// survival/corpse recovery, which run earlier and outside the ledger. Without this,
		// the loot block's "no enemy within radius" safety made a dry-belt pickup IMPOSSIBLE
		// while anything hostile lived nearby — greed-safety inverted into a death rule.
		if *loot && chickenStreak == 0 {
			if _, ok := beltHealPos(d); !ok {
				contact := false
				for _, m := range d.Monsters.Enemies() {
					if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= 5 {
						contact = true
						break
					}
				}
				if !contact {
					bestDist := 1 << 30
					var pot data.Item
					havePot := false
					for _, it := range d.Inventory.ByLocation(item.LocationGround) {
						if bl, ok := lootBlacklist[it.UnitID]; ok && time.Now().Before(bl) {
							continue
						}
						if !groundHeal(it) {
							continue
						}
						if dd := chebyshev(me, it.Position); dd < bestDist && dd <= *lootradius {
							bestDist, pot, havePot = dd, it, true
						}
					}
					if havePot && claim("critloot", 70, 1200*time.Millisecond) {
						if bestDist <= 2 {
							sx, sy := screenPointToward(me, me.X-pot.Position.X, me.Y-pot.Position.Y)
							walkToHold(sx, sy, 150)
							continue
						}
						if bestDist > 5 {
							logger.Info("critloot: belt dry — going for ground heal", "name", string(pot.Name),
								"dist", bestDist)
							navWalk(me, pot.Position)
							continue
						}
						hoverPickClick(pot.Position, pot.UnitID)
						still := false
						for _, it := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
							if it.UnitID == pot.UnitID {
								still = true
								break
							}
						}
						if still {
							lootAttempts[pot.UnitID]++
							if lootAttempts[pot.UnitID] >= 5 {
								lootBlacklist[pot.UnitID] = time.Now().Add(15 * time.Second)
								delete(lootAttempts, pot.UnitID)
							}
						} else {
							logger.Info("critloot: picked up heal", "name", string(pot.Name))
							delete(lootAttempts, pot.UnitID)
						}
						continue
					}
				}
			}
		}

		// CUBE FIRST (the user's extra-space insight): before any town trip, keepers go
		// into the 96-cell modded cube right here in the field — backpack pressure only
		// means town when the cube can't absorb it (or junk needs selling).
		if *cubeKey != "" && tpPhase == 0 && !d.PlayerUnit.Area.IsTown() &&
			time.Since(lastCubeStash) > 5*time.Minute &&
			len(d.Inventory.ByLocation(item.LocationInventory)) >= *tripItems-6 {
			enemyNear := false
			for _, m := range d.Monsters.Enemies() {
				if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= 20 {
					enemyNear = true
					break
				}
			}
			if !enemyNear {
				lastCubeStash = time.Now()
				identifyErrand() // keeper() needs Identified; the tome works anywhere
				if n := cubeStash(); n > 0 {
					continue
				}
			}
		}

		// AUTO TOWN TRIP (the user's "on full inventory"): backpack pressure sends him
		// through his own portal for the full errand run — identify, sell, restock — then
		// back through the same portal. On-demand stays `echo tp > logs/control.txt`.
		if *tripItems > 0 && *tp != "" && tpPhase == 0 && !d.PlayerUnit.Area.IsTown() &&
			time.Since(lastAutoTrip) > 15*time.Minute &&
			len(d.Inventory.ByLocation(item.LocationInventory)) >= *tripItems {
			enemyNear := false
			for _, m := range d.Monsters.Enemies() {
				if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= 20 {
					enemyNear = true
					break
				}
			}
			if !enemyNear {
				logger.Info("towntrip: AUTO — inventory full",
					"items", len(d.Inventory.ByLocation(item.LocationInventory)))
				lastAutoTrip = time.Now()
				tpVendorWanted = true
				tpPhase, tpTripStart, tpParkInTown, tpErrandsDone = 1, time.Now(), false, false
				continue
			}
		}

		// SUMMON: the necro flywheel — attacks make corpses, corpses make skeletons, skeletons
		// tank. Select the summon by its bound hotkey, cast at the nearest monster corpse; the
		// next bite's -rabies press flips the right skill back to the attack. CLOSED LOOP: a
		// cast that never produces a pet means the summon skill isn't actually available (the
		// wand granting RaiseSkeleton is on a corpse somewhere) — the hotkey then selects
		// nothing and the right-click fires the ATTACK skill at the corpse pile every 2s
		// (observed: looked like "retrieving the corpse by right-clicking"). Five fruitless
		// casts disable summoning for 10 minutes.
		if *summonKey != "" && time.Since(summonAt) > 2*time.Second &&
			d.PlayerUnit.MPPercent() >= *meleeBelow && time.Now().After(summonDisabledUntil) {
			pets := 0
			for i := range d.Monsters {
				if d.Monsters[i].IsPet() {
					pets++
				}
			}
			if summonCastPets >= 0 { // a cast is pending verification
				if pets > summonCastPets {
					summonFails = 0
				} else {
					summonFails++
					if summonFails >= 5 {
						logger.Warn("summon: 5 casts produced no pet — skill unavailable (granting item missing?); disabled 10min")
						summonDisabledUntil = time.Now().Add(10 * time.Minute)
						summonFails = 0
					}
				}
				summonCastPets = -1
			}
			// SELF-AWARE ARMY CAP: skeleton count is a live fact — Raise Skeleton allows
			// one skeleton per skill level (item bonuses included in the read), plus the
			// golem in the same pet count. -maxpets froze the army at 4 while the skill
			// grew to 5; the flag is now a FLOOR-only fallback for when the read is empty.
			petCap := *maxPets
			if pts, ok := d.PlayerUnit.Skills[skill.RaiseSkeleton]; ok && pts.Level > 0 {
				petCap = int(pts.Level)
				if *golemKey != "" {
					petCap++ // the golem occupies a pet slot but not a skeleton slot
				}
			}
			if pets < petCap && time.Now().After(summonDisabledUntil) {
				best := 1 << 30
				var corpsePos data.Position
				for i := range d.Corpses {
					if dd := chebyshev(me, d.Corpses[i].Position); dd < best && dd <= 20 {
						best, corpsePos = dd, d.Corpses[i].Position
					}
				}
				if best < 1<<30 {
					sx, sy := gameToScreen(gr, me.X, me.Y, corpsePos.X, corpsePos.Y)
					moveStop()
					hid.PressKey(hid.GetASCIICode(*summonKey))
					time.Sleep(80 * time.Millisecond)
					hid.Click(game.RightButton, sx, sy)
					time.Sleep(150 * time.Millisecond)
					logger.Info("summon: raise", "pets", pets, "corpseDist", best)
					summonCastPets = pets
					summonAt = time.Now()
					continue
				}
			}
			summonAt = time.Now()
		}

		// GOLEM: no corpse needed, so it's a timer, not a scavenger hunt. Recasting replaces a
		// wounded golem — the refresh IS the maintenance. Cast at his own feet, then the next
		// bite's -rabies press (if any) restores the attack skill.
		if *golemKey != "" && time.Since(golemAt) > time.Duration(*golemEvery)*time.Second &&
			d.PlayerUnit.MPPercent() >= *meleeBelow {
			moveStop()
			hid.PressKey(hid.GetASCIICode(*golemKey))
			time.Sleep(80 * time.Millisecond)
			hid.Click(game.RightButton, cx, cy+40)
			time.Sleep(200 * time.Millisecond)
			logger.Info("golem: refresh cast")
			golemAt = time.Now()
			continue
		}

		// AUTO-STAT: same calm-gate as auto-skill. Spends ONE banked point per visit (the panel
		// stays open only briefly — army tanks, but don't loiter), buffering str/dex over gear
		// requirements then vitality. Verified per click via StatPoints; a click that doesn't
		// spend aborts the visit.
		if *autoStat != "" && !anyEnemyWithin(d, me, *dangerRange) && time.Since(autoStatAt) > 20*time.Second {
			sp2, ok := d.PlayerUnit.Stats.FindStat(stat.StatPoints, 0)
			if !ok {
				sp2, _ = d.PlayerUnit.BaseStats.FindStat(stat.StatPoints, 0)
			}
			if sp2.Value > 0 {
				var sbx, sby, dbx, dby, vbx, vby int
				if _, err := fmt.Sscanf(*autoStat, "%d,%d,%d,%d,%d,%d", &sbx, &sby, &dbx, &dby, &vbx, &vby); err == nil {
					cur := func(id stat.ID) int {
						if v, ok := gr.GetData().PlayerUnit.Stats.FindStat(id, 0); ok {
							return v.Value
						}
						v, _ := gr.GetData().PlayerUnit.BaseStats.FindStat(id, 0)
						return v.Value
					}
					reqStr, reqDex := 0, 0
					for _, loc := range []item.LocationType{item.LocationEquipped, item.LocationInventory} {
						for _, it := range d.Inventory.ByLocation(loc) {
							if rs := it.Desc().RequiredStrength; rs > reqStr {
								reqStr = rs
							}
							if rd := it.Desc().RequiredDexterity; rd > reqDex {
								reqDex = rd
							}
						}
					}
					moveStop()
					hid.PressKey(hid.GetASCIICode("c"))
					time.Sleep(700 * time.Millisecond)
					spent := 0
					// Cap the session: a big banked backlog used to hold the loop (and the
					// character) hostage for 20s+ of panel clicking; 5 points per visit keeps
					// each pause human-sized and the 20s cadence drains any backlog quickly.
					for i := 0; i < sp2.Value && i < 5; i++ {
						var bx, by int
						switch {
						case cur(stat.Strength) < reqStr+10:
							bx, by = sbx, sby
						case cur(stat.Dexterity) < reqDex+10:
							bx, by = dbx, dby
						default:
							bx, by = vbx, vby
						}
						before := cur(stat.StatPoints)
						uiClick(bx, by)
						time.Sleep(220 * time.Millisecond)
						if cur(stat.StatPoints) < before {
							spent++
						} else {
							break
						}
					}
					hid.PressKey(hid.GetASCIICode("c"))
					logger.Info("autostat: spent", "points", spent, "str", cur(stat.Strength),
						"dex", cur(stat.Dexterity), "vita", cur(stat.Vitality))
				}
			}
			autoStatAt = time.Now()
			continue
		}

		// AUTO-SKILL: spend banked points into the configured skill when it's calm. Tab click
		// first (idempotent — the tree remembers its last tab, which may not be ours), then the
		// icon; verified by the SkillPoints stat, so a missed click just retries next level.
		if *autoSkill != "" && !anyEnemyWithin(d, me, *dangerRange) && time.Since(autoSkillAt) > 30*time.Second {
			sp, ok := d.PlayerUnit.Stats.FindStat(stat.SkillPoints, 0)
			if !ok {
				sp, _ = d.PlayerUnit.BaseStats.FindStat(stat.SkillPoints, 0)
			}
			if sp.Value > 0 {
				var tx, ty, kx, ky int
				if _, err := fmt.Sscanf(*autoSkill, "%d,%d,%d,%d", &tx, &ty, &kx, &ky); err == nil {
					logger.Info("autoskill: spending point", "banked", sp.Value)
					moveStop()
					hid.PressKey(hid.GetASCIICode("t"))
					time.Sleep(800 * time.Millisecond)
					uiClick(tx, ty)
					time.Sleep(400 * time.Millisecond)
					uiClick(kx, ky)
					time.Sleep(500 * time.Millisecond)
					hid.PressKey(hid.GetASCIICode("t")) // toggle the tree closed (esc would open the pause menu)
					time.Sleep(300 * time.Millisecond)
					d2 := gr.GetData()
					sp2, ok2 := d2.PlayerUnit.Stats.FindStat(stat.SkillPoints, 0)
					if !ok2 {
						sp2, _ = d2.PlayerUnit.BaseStats.FindStat(stat.SkillPoints, 0)
					}
					logger.Info("autoskill: result", "before", sp.Value, "after", sp2.Value,
						"spent", sp2.Value < sp.Value)
				}
				autoSkillAt = time.Now()
				continue
			}
			autoSkillAt = time.Now()
		}

		// OPPORTUNITY GATE: a favorable nearby pack claims the tick for the combat flow even
		// during corpse recovery, death-spot seek, or route travel — kills make corpses,
		// corpses make skeletons, and a corpse-run behind a growing army succeeds where a
		// naked sprint dies (measured: 3 deaths at the same shaman camp, golem-only escort,
		// zero raises the whole way because recovery never let combat run).
		oppFight := false
		if !d.PlayerUnit.Area.IsTown() {
			thr, str, pn, cen := packAssess(d, me)
			// CONTACT OVERRIDE: an enemy inside melee reach cannot be outwalked — feeding it to
			// the planner as a moving obstacle just dithers (measured: sh=9, committedS pinned
			// at 0, position jitter at the Cold Plains crossing). Favorable or not, contact
			// hands the tick to the combat flow — its POSTURES (kite/regroup) are the right
			// tool for bad odds, not a walk-around.
			contact := false
			for i := range d.Monsters {
				m := &d.Monsters[i]
				if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead || m.IsGoodNPC() || m.IsPet() || m.IsMerc() {
					continue
				}
				if chebyshev(me, m.Position) <= 8 {
					contact = true
					break
				}
			}
			if pn > 0 && (contact || (thr <= str && chebyshev(me, cen) <= 25)) {
				oppFight = true
				if time.Since(travelFightLog) > 5*time.Second {
					logger.Info("opportunity: combat claims the tick",
						"threat", thr, "strength", str, "packN", pn, "contact", contact)
					travelFightLog = time.Now()
				}
			}
		}

		// CORPSE RECOVERY: preempts combat/loot/explore. Corpses don't move, so once seen the
		// POSITION is remembered and navigated to unconditionally — the live Corpse.Found flag
		// flickers as rooms stream in/out at range, so it is only trusted as a "gone" signal when
		// standing close enough (<=10) that the corpse's rooms must be loaded.
		if d.Corpse.Found && !d.Corpse.StateNotInteractable() {
			corpseTargetPos = d.Corpse.Position
		}
		if corpseTargetPos.X != 0 && !oppFight && time.Since(corpseGiveUp) > 10*time.Minute {
			if corpseRecoverStart.IsZero() {
				corpseRecoverStart = time.Now()
				logger.Info("corpse: recovery started", "pos", fmt.Sprintf("(%d,%d)", corpseTargetPos.X, corpseTargetPos.Y))
			}
			if time.Since(corpseRecoverStart) > 3*time.Minute {
				logger.Warn("corpse: giving up for now (10min cooldown)")
				corpseGiveUp, corpseRecoverStart = time.Now(), time.Time{}
				corpseTargetPos = data.Position{}
				_ = os.Remove(deathStateFile)
				curGoto = *gotoArea
				continue
			}
			cd := chebyshev(me, corpseTargetPos)
			if time.Since(corpseLogAt) > 3*time.Second {
				logger.Info("corpse: recovering", "dist", cd, "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
					"corpse", fmt.Sprintf("(%d,%d)", corpseTargetPos.X, corpseTargetPos.Y),
					"found", d.Corpse.Found, "sweepFails", corpseSweepFails)
				corpseLogAt = time.Now()
			}
			switch {
			case cd > 10:
				// DON'T FEED THE CAMP: a hopeless pack (regroup-grade) sitting on the approach
				// is not walked into — back off and let the give-up timer route us elsewhere to
				// snowball; the 10-min cooldown retry comes back with an army.
				if thr, str, pn, cen := packAssess(d, me); pn > 0 && thr > str*3 &&
					cd <= 60 && chebyshev(me, cen) <= 30 {
					if time.Since(corpseLogAt) > 3*time.Second {
						logger.Warn("corpse: killer pack too strong for the approach — holding off",
							"threat", thr, "strength", str)
					}
					retreat := data.Position{X: me.X + (me.X-cen.X), Y: me.Y + (me.Y-cen.Y)}
					navWalk(me, retreat)
				} else {
					navWalk(me, corpseTargetPos)
				}
			case !d.Corpse.Found:
				// Close enough that its rooms are loaded — a consistent absence here is REAL.
				corpseGoneStreak++
				if corpseGoneStreak >= 8 {
					logger.Info("corpse: no corpse at remembered spot — clearing", "dist", cd)
					corpseTargetPos = data.Position{}
					corpseRecoverStart = time.Time{}
					corpseGoneStreak = 0
					_ = os.Remove(deathStateFile)
					curGoto = *gotoArea
				}
				time.Sleep(150 * time.Millisecond)
			case cd <= 1:
				// Standing on it hides the label under the character — step off a couple tiles.
				sx, sy := screenPointToward(me, me.X-corpseTargetPos.X, me.Y-corpseTargetPos.Y)
				walkToHold(sx, sy, 150)
			default:
				corpseGoneStreak = 0
				moveStop() // the probes below aim the cursor — held move would walk into the sweep
				bx, by := gameToScreen(gr, me.X, me.Y, d.Corpse.Position.X, d.Corpse.Position.Y)
				got := false
				hoverSeen := ""
				// RESUMABLE SWEEP: the full 13x7 label hunt is ~5s of blocking sleeps — one whole
				// tick with no survival checks at the exact spot we just died (measured). Probe at
				// most 14 points per tick and carry the cursor index across ticks; survival, the
				// control channel, and posture all run between slices.
				probes := 0
				sweepDone := false
				for ; corpseSweepIdx < 13*7 && probes < 14; corpseSweepIdx++ {
					dy := -60 + (corpseSweepIdx/7)*6
					dx := []int{0, -8, 8, -16, 16, -26, 26}[corpseSweepIdx%7]
					px, py := bx+dx, by+dy
					hid.AimPhysical(px, py)
					time.Sleep(55 * time.Millisecond)
					probes++
					d3 := gr.GetData()
					if d3.HoverData.IsHovered && hoverSeen == "" {
						hoverSeen = fmt.Sprintf("type=%d id=%d", d3.HoverData.UnitType, int(d3.HoverData.UnitID))
					}
					if !d3.Corpse.IsHovered {
						continue
					}
					hid.AimPhysical(px, py)
					time.Sleep(70 * time.Millisecond)
					if !gr.GetData().Corpse.IsHovered {
						continue
					}
					logger.Info("corpse: hovered — clicking", "at", fmt.Sprintf("(%d,%d)", px, py))
					interactClick(px, py)
					for w := 0; w < 8; w++ { // 1.2s pickup confirm; the block re-enters next tick anyway
						time.Sleep(150 * time.Millisecond)
						if !gr.GetData().Corpse.Found {
							got = true
							break
						}
					}
					corpseSweepIdx = 0
					break
				}
				if corpseSweepIdx >= 13*7 {
					corpseSweepIdx = 0
					sweepDone = true
				}
				if !got && sweepDone {
					corpseSweepFails++
					logger.Info("corpse: sweep found no Corpse.IsHovered", "fails", corpseSweepFails,
						"anyHover", hoverSeen)
					if corpseSweepFails >= 3 {
						sx2, sy2 := gameToScreen(gr, me.X, me.Y, d.Corpse.Position.X, d.Corpse.Position.Y)
						logger.Info("corpse: blind interactClick fallback", "screen", fmt.Sprintf("(%d,%d)", sx2, sy2))
						interactClick(sx2, sy2)
						time.Sleep(800 * time.Millisecond)
						if !gr.GetData().Corpse.Found {
							got = true
						}
					}
				}
				if got {
					logger.Info("corpse: RECOVERED — gear back, resuming",
						"after", time.Since(corpseRecoverStart).Round(time.Second).String())
					corpseRecoverStart = time.Time{}
					corpseSweepFails = 0
					deathsSinceRecovery = 0
					corpseTargetPos = data.Position{}
					_ = os.Remove(deathStateFile)
					curGoto = *gotoArea
				}
			}
			continue
		}

		// DEATH-SPOT SEEK: we're in the death area but the corpse isn't in a loaded room yet.
		// Navigate to the RECORDED death position — rooms load on approach and Corpse.Found
		// flips, handing off to the recovery block above. TIME-BOXED: an unreachable spot (or a
		// stale state file from a run that got timer-killed mid-recovery) burned a whole 15-min
		// run seeking dist~150 inside a 75-tile cave (measured). 2 minutes, then let it go.
		if !d.Corpse.Found && deathHuntPos.X != 0 && int(d.PlayerUnit.Area) == deathHuntArea && !oppFight {
			if deathSeekStart.IsZero() {
				deathSeekStart = time.Now()
			}
			if chebyshev(me, deathHuntPos) <= 8 || time.Since(deathSeekStart) > 2*time.Minute {
				if chebyshev(me, deathHuntPos) <= 8 {
					logger.Warn("death spot reached but no corpse — clearing death state")
				} else {
					logger.Warn("death-spot seek timed out — clearing death state")
				}
				deathHuntPos, deathHuntArea = data.Position{}, 0
				deathSeekStart = time.Time{}
				_ = os.Remove(deathStateFile)
				curGoto = *gotoArea
			} else {
				if time.Since(corpseLogAt) > 3*time.Second {
					logger.Info("death-spot seek", "dist", chebyshev(me, deathHuntPos),
						"pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
					corpseLogAt = time.Now()
				}
				navWalk(me, deathHuntPos)
				continue
			}
		}
		if d.Corpse.Found && deathHuntPos.X != 0 {
			deathHuntPos, deathHuntArea = data.Position{}, 0 // recovery block owns it now
		}

		// GOTO: travel to a target area via a live room-graph border, using the clearance-aware
		// Navigator (owns its own recovery). Runs BEFORE the generic break-out/unstick so those
		// path-agnostic behaviors don't drag us off a legitimate route.
		// FIGHT ON THE WAY: travel used to own the tick unconditionally, so the bot walked
		// straight past packs it could profitably farm ("a commute, not a sweep"). When the
		// combat oracle calls a nearby pack favorable, yield this tick to the normal combat
		// flow below; travel resumes the tick the pack stops qualifying (dead, fled, or odds
		// turned). Close packs only — a favorable pack 60 tiles off is not a reason to detour.
		if curGoto != 0 && !oppFight && navi != nil && navGrid != nil && int(d.PlayerUnit.Area) != curGoto {
			// RESUME RESET: the stall watchdog's clock keeps aging while combat/loot/recovery
			// own the ticks — travel resuming after a 10s in-place fight looked like a 10s
			// wedge and stamped refusals on innocent ground (measured: hatch cleared 32 cells
			// he then walked straight through). A gap in goto ticks = not a movement stall.
			if time.Since(lastGotoTickAt) > 1200*time.Millisecond {
				gotoLastPos, gotoProgressAt = me, time.Now()
				exitSeekBestAt = time.Now() // same aging bug: a fight is not exit-seek stagnation
			}
			lastGotoTickAt = time.Now()
			// NET-PROGRESS WATCHDOG: the goto path skips the generic unstick (by design), so a
			// physical wedge the grid can't see (fence pockets the live town collision marks
			// walkable) would freeze travel forever — measured twice, minutes of bit-identical
			// position while "walking". No net movement for 8s -> unstick burst + rotate + replan.
			// NET displacement over a FIXED 5s window — the old anchor reset on any 2-tile
			// frame-to-frame move, so a physical wedge with ±3-tile jitter never tripped it
			// (measured: minutes at the Cold Plains gate, committedS pinned at 0, sh flat).
			if time.Since(gotoProgressAt) > 5*time.Second {
				if chebyshev(me, gotoLastPos) > 4 {
					// Healthy window — reset the anchor and FALL THROUGH to travel this tick
					// (the old `continue` here silently discarded one travel tick every 5s).
					gotoLastPos, gotoProgressAt = me, time.Now()
				} else { // jitter, not travel
					if exitSeekExit.X != 0 && chebyshev(me, exitSeekExit) <= 20 {
						// ENTRANCE RITUAL ZONE: jitter here is INTENTIONAL — the contact
						// phase pushes a wall on purpose and the hover-spiral needs a still
						// character. The burst/wedge reflexes were sabotaging both all
						// morning (bounces reset the ritual's phase timer every cycle —
						// "old code overriding us", as the user diagnosed). Stand down.
						gotoLastPos, gotoProgressAt = me, time.Now()
					} else if fightThrough(d, me) {
						logger.Info("goto: blocked — fighting through")
						gotoLastPos, gotoProgressAt = me, time.Now()
						continue
					} else {
					// PET JAM: with a 6-pet army in a cave corridor the wall is usually OUR OWN
					// skeletons (screenshot-verified: boxed into a rock pocket in the Den, 40
					// false wedges stamped at one spot). Minions yield to their owner when he
					// keeps force-moving into them — so SHOVE toward the aim instead of bursting
					// away, and record NOTHING: a pet is not a wall, and stamping it poisons the
					// atlas with refusals the next plan routes around forever.
					petsAdjacent := 0
					for i := range d.Monsters {
						if d.Monsters[i].IsPet() && chebyshev(d.Monsters[i].Position, me) <= 3 {
							petsAdjacent++
						}
					}
					if petsAdjacent >= 2 && gotoAim.X != 0 {
						logger.Info("goto: pet jam — shoving through the army", "pets", petsAdjacent)
						sx, sy := screenPointToward(me, gotoAim.X-me.X, gotoAim.Y-me.Y)
						walkToHold(sx, sy, 1600)
						time.Sleep(300 * time.Millisecond)
						gotoLastPos = gr.GetData().PlayerUnit.Position
						gotoProgressAt = time.Now()
						continue
					}
					// Record the refusal: the cells just AHEAD (toward the aim) are where the
					// game said no — poison them so every replan routes around, not through.
					if gotoAim.X != 0 {
						dx, dy := gotoAim.X-me.X, gotoAim.Y-me.Y
						n := max(abs(dx), abs(dy))
						if n > 0 {
							w := data.Position{X: me.X + dx*3/n, Y: me.Y + dy*3/n}
							petNearW := false
							for i := range d.Monsters {
								if d.Monsters[i].IsPet() && chebyshev(d.Monsters[i].Position, w) <= 2 {
									petNearW = true
									break
								}
							}
							if petNearW {
								logger.Info("goto: refusal NOT stamped — a pet stands there, not a wall")
							} else {
								wedges = append(wedges, w)
								if len(wedges) > 40 {
									wedges = wedges[len(wedges)-40:]
								}
								// PERSIST the lesson: runtime wedges die with the run (measured: every
								// run re-learned the same river bank, 5s of flop per wedge). The atlas
								// keeps refusals forever, sticky against the lying live merge.
								atlas.MarkRefused(gr.MapSeed(), int(d.PlayerUnit.Area), w, 2, me)
								logger.Warn("goto: passage refused — wedge recorded",
									"at", fmt.Sprintf("(%d,%d)", w.X, w.Y), "wedges", len(wedges))
							}
						}
					}
					logger.Warn("goto: no net movement — open burst", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
					openBurst(me)
					if len(gotoCross) > 0 {
						gotoFailedAt[gotoIdx] = time.Now()
						gotoIdx, gotoTryAt = (gotoIdx+1)%len(gotoCross), time.Now()
					}
					navi.havePlan = false
					gotoLastPos = gr.GetData().PlayerUnit.Position
					gotoProgressAt = time.Now()
					continue
					}
				}
			}
			// MULTI-HOP: the room graph only has DIRECT borders, so a non-adjacent target (e.g.
			// town -> Cold Plains) yields 0 candidates forever. BFS the area graph from map data
			// for the next hop; each crossing re-aligns and re-plans, so the chain emerges hop
			// by hop. Falls back to the direct target when map data is absent.
			hopTarget := area.ID(curGoto)
			if nh := nextHopArea(d, d.PlayerUnit.Area, area.ID(curGoto)); nh != 0 {
				hopTarget = nh
			}
			if len(gotoCross) == 0 { // discover crossing candidates from the live room graph
				if rgph, err := gr.ReadCurrentRoomGraph(); err == nil {
					for _, b := range rgph.BordersTo(hopTarget) {
						span := b.SpanEnd - b.SpanStart
						for _, frac := range []float64{0.5, 0.35, 0.65} {
							ap, cr := b.CrossingAt(b.SpanStart + int(float64(span)*frac))
							gotoCross = append(gotoCross, [2]data.Position{ap, cr})
						}
					}
					if len(gotoCross) > 0 || time.Since(gotoBorderSeekLog) > 5*time.Second {
						logger.Info("goto: live borders", "targetArea", curGoto, "hop", int(hopTarget), "candidates", len(gotoCross))
					}
					gotoIdx, gotoTryAt = 0, time.Now()
					gotoFailedAt = map[int]time.Time{} // fresh candidate set, fresh slate
				}
			}
			if len(gotoCross) == 0 {
				// Border rooms not loaded yet — the live room graph only spans LOADED rooms, so a
				// far border yields nothing. Travel toward the map-data exit: plan to the WALKABLE
				// cell nearest the exit (the exit itself is outside the partial grid — planning
				// straight at it degenerates into an instant-arrive/replan loop with ZERO movement),
				// and refresh the live grid as rooms stream in so the frontier advances.
				exit, exitOK := mapExitTo(d, hopTarget)
				// PHANTOM-EXIT SANITY: an exit far outside the current live grid is another
				// area's coordinates (measured: wandering into the cave mid-route left goto
				// beelining at the Cold Plains->Stony exit (5600,5840) from INSIDE area 17 —
				// dist frozen at 464, wedges stamped along the cave wall forever).
				// Bounds test against the AREA's FULL map grid translated to live coords (the
				// live grid spans only loaded rooms, so a legit far exit sits outside it — the
				// first version of this check flagged the real CP->Stony exit as phantom). The
				// cave's map grid is small, so a foreign-frame coordinate still gets caught.
				if exitOK && navGrid != nil && d.AreaData.Grid != nil {
					aw, ah := d.AreaData.Grid.Width, d.AreaData.Grid.Height
					inX := exit.X >= navGrid.OffsetX-50 && exit.X <= navGrid.OffsetX+aw+50
					inY := exit.Y >= navGrid.OffsetY-50 && exit.Y <= navGrid.OffsetY+ah+50
					if !inX || !inY {
						logger.Warn("goto: exit is outside our area frame — phantom, ignoring",
							"exit", fmt.Sprintf("(%d,%d)", exit.X, exit.Y), "hop", int(hopTarget),
							"areaDims", fmt.Sprintf("%dx%d", aw, ah))
						exitOK = false
					}
				}
				// SIDE-AREA ESCAPE: no usable exit toward the hop from here (we wandered into a
				// cave/den off the route) — leave via the nearest REAL adjacency first.
				if !exitOK && len(d.AdjacentLevels) > 0 {
					best := 1 << 30
					aw, ah := 1<<30, 1<<30
					if d.AreaData.Grid != nil {
						aw, ah = d.AreaData.Grid.Width, d.AreaData.Grid.Height
					}
					for _, al := range d.AdjacentLevels {
						if p, ok2 := mapExitTo(d, al.Area); ok2 {
							if navGrid != nil &&
								(p.X < navGrid.OffsetX-50 || p.X > navGrid.OffsetX+aw+50 ||
									p.Y < navGrid.OffsetY-50 || p.Y > navGrid.OffsetY+ah+50) {
								continue // phantom too
							}
							if dd := chebyshev(me, p); dd < best {
								best, exit, exitOK = dd, p, true
								hopTarget = al.Area
							}
						}
					}
					if exitOK && time.Since(gotoBorderSeekLog) > 5*time.Second {
						logger.Info("goto: side-area — exiting via nearest adjacency",
							"via", int(hopTarget), "exit", fmt.Sprintf("(%d,%d)", exit.X, exit.Y))
					}
				}
				if exitOK {
					gotoAim = exit
					if time.Since(gotoBorderSeekLog) > 5*time.Second {
						logger.Info("goto: seeking exit (border rooms not loaded)",
							"hop", int(hopTarget), "exit", fmt.Sprintf("(%d,%d)", exit.X, exit.Y),
							"dist", chebyshev(me, exit))
						gotoBorderSeekLog = time.Now()
					}
					// GOAL-progress watchdog (see declaration). Improvement threshold 5 so wall-pocket
					// jiggle doesn't count as progress.
					if exit != exitSeekExit {
						exitSeekExit, exitSeekBestDist, exitSeekBestAt = exit, 1<<30, time.Now()
						exitDetourDest, exitDetourUntil = data.Position{}, time.Time{}
					}
					if dd := chebyshev(me, exit); dd < exitSeekBestDist-5 || exitSeekBestDist == 1<<30 {
						exitSeekBestDist, exitSeekBestAt = dd, time.Now()
					}
					if !exitDetourUntil.IsZero() && time.Now().Before(exitDetourUntil) &&
						exitDetourDest.X != 0 && chebyshev(me, exitDetourDest) > 6 {
						// Mid-detour: walk to the frontier so rooms load somewhere new, then let the
						// normal seek retry with the grown grid.
						navWalk(me, exitDetourDest)
						continue
					}
					if time.Since(exitSeekBestAt) > 40*time.Second {
						if exitDetourUntil.IsZero() {
							// Stage 1: strategy change, not a harder retry — explore a frontier for up
							// to 60s to load rooms in a different direction.
							if fp, fok := atlas.FrontierNear(gr.MapSeed(), int(d.PlayerUnit.Area), me); fok {
								logger.Warn("goto: no exit progress in 40s — frontier detour",
									"bestDist", exitSeekBestDist, "detour", fmt.Sprintf("(%d,%d)", fp.X, fp.Y))
								exitDetourDest, exitDetourUntil = fp, time.Now().Add(60*time.Second)
								exitSeekBestAt = time.Now() // fresh window for the post-detour retry
								navWalk(me, fp)
								continue
							}
						}
						// Stage 2 (or no frontier at all): the area won't yield this exit. Advance
						// the route ONLY if we genuinely toured the exit's neighborhood — a stall
						// 60+ tiles away is a LOCOMOTION failure, and skipping the stop for it is
						// how one pet-jam in the Den marched routeIdx from Den to Black Marsh in
						// an hour while the character never left the cave.
						gotNear := exitSeekBestDist <= 40
						logger.Warn("goto: no exit progress after detour — abandoning this seek",
							"bestDist", exitSeekBestDist, "gotNear", gotNear)
						exitSeekExit, exitSeekBestDist = data.Position{}, 1<<30
						exitDetourDest, exitDetourUntil = data.Position{}, time.Time{}
						if *autoProgress && curGoto == progressTarget && routeIdx < len(progressRoute)-1 && gotNear {
							routeIdx++
							_ = os.WriteFile(routeStateFile, []byte(fmt.Sprintf("%d", routeIdx)), 0644)
							logger.Info("autoprogress: skipping to next stop", "area", progressRoute[routeIdx])
							curGoto, progressTarget = progressRoute[routeIdx], progressRoute[routeIdx]
						} else if *autoProgress && curGoto == progressTarget {
							logger.Warn("goto: keeping this stop — the exit was never reached, not proven absent")
						} else {
							curGoto = *gotoArea
						}
						continue
					}
					// ENTRANCE WARPS (Den, Burial Grounds, caves): these connect via a clickable
					// warp, not a walkable border, so the room graph NEVER yields candidates.
					// The live entrance UNIT can sit ~30 subtiles from the map-data exit point
					// (measured: mapped (5734,4741) vs clickable mouth (5706,4739)), so match
					// generously and steer at the ENTRANCE, not the mapped point. Radius 140,
					// not 60: he parked at dist 61-71 in the Den while the entrance code slept
					// one conditional away.
					if chebyshev(me, exit) <= 140 {
						var went *data.Entrance
						for i := range d.Entrances {
							if chebyshev(d.Entrances[i].Position, exit) <= 45 {
								went = &d.Entrances[i]
								break
							}
						}
						// NO ENTRANCE UNIT (the Den's stairs listed zero live entrances while
						// he churned 14-19 tiles away for whole runs): steer STRAIGHT at the
						// mapped point with force-move — the nav plan can't be trusted here,
						// the atlas around stairs was refusal-poisoned, and walk-through
						// transitions fire on contact with the tile, not on a plan.
						if went == nil && chebyshev(me, exit) <= 30 {
							if time.Since(gotoBorderSeekLog) > 3*time.Second {
								logger.Info("goto: no entrance unit near mapped exit — steering at the MAP point",
									"exit", fmt.Sprintf("(%d,%d)", exit.X, exit.Y),
									"entrances", len(d.Entrances), "dist", chebyshev(me, exit))
								gotoBorderSeekLog = time.Now()
							}
							// THE TRUE STAIRS TILE: object 37 sits at the Den stairs with
							// Selectable=false — the flag lies (or means something else on
							// 3.2) — and the MAP point lies by ~7 tiles (map (7642,5150) vs
							// object (7649,5152); the "-30 subtiles" lore, Den edition).
							// Steer and click at the OBJECT's tile whenever one is near.
							steerTgt := exit
							bestObjD := 21
							for _, o := range d.Objects {
								if dd := chebyshev(o.Position, exit); dd < bestObjD {
									bestObjD, steerTgt = dd, o.Position
								}
							}
							// KOOLO'S ENTRANCE RECIPE (upstream production code,
							// action/step/interact_entrance.go): aim at (exit-1,-1), SPIRAL
							// the POINTER a few px per attempt, and click only once
							// HoverData confirms an entrance-class unit under the cursor
							// (UnitType 5, or 2+IsHovered). Every blind ladder of the night
							// lacked this hover contract — and monster bites prove HoverData
							// reads fine on 3.2. Phases: 5s of contact push (walk-throughs
							// transition free), then a hover-spiral burst, repeat.
							if chebyshev(me, steerTgt) > 20 {
								// generous: a bounce must not zero the ritual timer
								mapPointContactAt = time.Time{}
								exitClickTry = 0
							} else if mapPointContactAt.IsZero() {
								mapPointContactAt = time.Now()
							}
							phase := 0
							if !mapPointContactAt.IsZero() {
								phase = int(time.Since(mapPointContactAt).Seconds()) % 9
							}
							if phase >= 5 {
								moveStop()
								me2 := gr.GetData().PlayerUnit.Position
								lx, ly := gameToScreen(gr, me2.X, me2.Y, steerTgt.X-1, steerTgt.Y-1)
								// koolo's utils.Spiral, inlined (archimedean, ~3px/turn), /3 like upstream
								trad := float64(exitClickTry*40) * math.Pi / 180.0
								hx := lx + int((4.0-2.0*trad)*math.Cos(trad))/3
								hy := ly + int((4.0-2.0*trad)*math.Sin(trad))/3
								exitClickTry++
								hid.MovePointer(hx, hy)
								time.Sleep(120 * time.Millisecond)
								hd := gr.GetData().HoverData
								if hd.IsHovered {
									logger.Info("goto: spiral hover hit", "unitType", hd.UnitType,
										"unitID", int(hd.UnitID), "at", fmt.Sprintf("(%d,%d)", hx, hy))
								}
								if hd.IsHovered && (hd.UnitType == 5 || hd.UnitType == 2) {
									logger.Info("goto: ENTRANCE HOVER confirmed — clicking",
										"unitType", hd.UnitType, "at", fmt.Sprintf("(%d,%d)", hx, hy))
									hid.Click(game.LeftButton, hx, hy)
									time.Sleep(1000 * time.Millisecond)
								}
								continue
							}
							// Approach RIDES THE MOVER (click gait pathfinds around the rock
							// lip — the straight push rubbed the wall, per the user's eyes);
							// only the last tiles get the straight contact shove.
							if chebyshev(me, steerTgt) > 4 {
								navWalk(me, steerTgt)
								time.Sleep(80 * time.Millisecond)
								continue
							}
							sx, sy := screenPointToward(me, steerTgt.X-me.X, steerTgt.Y-me.Y)
							walkToHold(sx, sy, 260)
							time.Sleep(80 * time.Millisecond)
							continue
						}
						if went != nil {
							ed := chebyshev(me, went.Position)
							// WALK-THROUGH entrances (Den, caves, area exits) transition on CONTACT,
							// not on a click from range — the old code stopped navigating at dist>12
							// and clicked forever between 2 and 12 (measured: 310 clicks, never
							// closing), and each failed hover-click fell through to interactClick =
							// a held-LMB Attack punching air on a weaponless char (the "shift+attack"
							// the user saw). So: force-move STRAIGHT ONTO the tile until the area
							// changes; only after ~6s of fruitless contact try a click (for the rare
							// click-to-open stairs).
							if entranceContactStart.IsZero() {
								entranceContactStart = time.Now()
							}
							if ed > 3 {
								if time.Since(gotoBorderSeekLog) > 3*time.Second {
									logger.Info("goto: walking onto entrance", "name", int(went.Name), "dist", ed)
									gotoBorderSeekLog = time.Now()
								}
								// Aim the force-move directly at the entrance tile (not navWalk,
								// which arrives ~4 tiles short) so we actually step onto it.
								sx, sy := screenPointToward(me, went.Position.X-me.X, went.Position.Y-me.Y)
								walkToHold(sx, sy, 200)
								continue
							}
							if time.Since(entranceContactStart) > 6*time.Second {
								logger.Info("goto: entrance contact stalled — trying a click", "name", int(went.Name))
								// Straight to interactClick: entrances mostly never register hover, so the
								// full hoverPickClick sweep blocked ~20s per attempt (measured) with zero
								// survival checks — and fruitless sweeps landed on interactClick anyway.
								sx, sy := gameToScreen(gr, me.X, me.Y, went.Position.X, went.Position.Y)
								interactClick(sx, sy)
								entranceContactStart = time.Now()
							} else {
								// On the tile: nudge straight through it and let the transition fire.
								sx, sy := screenPointToward(me, went.Position.X-me.X, went.Position.Y-me.Y)
								walkToHold(sx, sy, 160)
							}
							time.Sleep(60 * time.Millisecond)
							continue
						}
					}
					if time.Since(gotoGridRefresh) > 4*time.Second {
						if g, ok2 := acquireGrid(); ok2 {
							navGrid = g
							navi = NewNavigator(g)
							walkables, walkCentroid = computeWalk(g)
						}
						gotoGridRefresh = time.Now()
					}
					// PARKED AT A LYING MAP POINT: the mapped exit produced no entrance and no
					// border candidates. Measured off by ~30 subtiles at the Den; a full run burned
					// at Burial Grounds. The park timer counts time-in-NEIGHBORHOOD (<=40) and
					// survives bounce excursions (resetting on momentary dist>6 made the ring
					// unreachable — the bouncing WAS the fish). Ring-search at 32; after two full
					// fruitless rings, give up on this route stop and advance — a lying exit costs
					// 90 seconds, not a night.
					if chebyshev(me, exit) <= 40 {
						if exitParkStart.IsZero() {
							exitParkStart = time.Now()
							exitRingIdx = 0
						}
						if time.Since(exitParkStart) > 8*time.Second {
							if exitRingIdx >= 16 {
								logger.Warn("goto: exit unreachable after 2 rings — abandoning this stop")
								exitParkStart = time.Time{}
								if *autoProgress && curGoto == progressTarget && routeIdx < len(progressRoute)-1 {
									routeIdx++
									_ = os.WriteFile(routeStateFile, []byte(fmt.Sprintf("%d", routeIdx)), 0644)
									logger.Info("autoprogress: skipping to next stop", "area", progressRoute[routeIdx])
									curGoto, progressTarget = progressRoute[routeIdx], progressRoute[routeIdx]
								} else {
									curGoto = *gotoArea
								}
								continue
							}
							angle := float64(exitRingIdx%8) / 8.0 * 2 * math.Pi
							ring := data.Position{
								X: exit.X + int(32*math.Cos(angle)),
								Y: exit.Y + int(32*math.Sin(angle)),
							}
							if time.Since(gotoBorderSeekLog) > 5*time.Second {
								logger.Info("goto: mapped exit is a dud — ring-searching",
									"sector", exitRingIdx%8, "lap", exitRingIdx/8,
									"to", fmt.Sprintf("(%d,%d)", ring.X, ring.Y))
								gotoBorderSeekLog = time.Now()
							}
							if chebyshev(me, ring) <= 6 {
								exitRingIdx++
							} else {
								navWalk(me, ring)
							}
							continue
						}
					}
					// Travel to the exit through the MOVER — live planner first, FULL MAP-PRIOR
					// planner when the live grid can't reach. The old greedy walk-to-the-cell-
					// nearest-the-exit beelined into terrain the map knows how to go around
					// (measured: Blood Moor->Cold Plains exit, parked at dist=701 on the river
					// bank for 9 minutes — the bridge is elsewhere and greedy can't see it).
					navWalk(me, exit)
					time.Sleep(40 * time.Millisecond)
					continue
				}
			}
			if len(gotoCross) > 0 {
				if gotoIdx >= len(gotoCross) {
					gotoIdx = 0
				}
				// NEAREST-FIRST, not round-robin: rotation used to march us 38 tiles west while
				// we stood 2 tiles from a different candidate's crossing (measured at Flavie's
				// gate). Failed candidates sit out 60s; standing beside ANY crossing takes it now.
				pickCand := func() int {
					best, bd := -1, 1<<30
					for i := range gotoCross {
						if t, okf := gotoFailedAt[i]; okf && time.Since(t) < 60*time.Second {
							continue
						}
						if dd := chebyshev(me, gotoCross[i][0]); dd < bd {
							bd, best = dd, i
						}
					}
					if best < 0 { // everything recently failed — take the nearest anyway
						for i := range gotoCross {
							if dd := chebyshev(me, gotoCross[i][0]); dd < bd {
								bd, best = dd, i
							}
						}
					}
					return best
				}
				for i := range gotoCross {
					if chebyshev(me, gotoCross[i][0]) <= 5 {
						gotoIdx = i // beside a crossing — use it regardless of whose turn it was
						break
					}
				}
				if time.Since(gotoTryAt) > 25*time.Second { // hard safety: candidate stuck too long
					gotoFailedAt[gotoIdx] = time.Now()
					gotoIdx, gotoTryAt = pickCand(), time.Now()
					navi.havePlan = false
				}
				ap, cr := gotoCross[gotoIdx][0], gotoCross[gotoIdx][1]
				gotoAim = ap
				// ONE AUTHORITY: the candidate approach rides the Mover like every other walk —
				// live planner, full map-prior fallback, wedges, thin-ray carrot, stall ladder.
				// (The old inline live-grid planner predated the Mover and had NONE of those;
				// it was the last non-Mover locomotion path and the one that flopped all night.)
				if chebyshev(me, ap) > 5 {
					if shiftCheck%4 == 0 {
						logger.Info("goto", "cand", fmt.Sprintf("%d/%d", gotoIdx, len(gotoCross)),
							"approach", fmt.Sprintf("(%d,%d)", ap.X, ap.Y),
							"dist", chebyshev(me, ap), "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
					}
					navWalk(me, ap)
				} else {
					// At the near side of the boundary — push across to transition.
					dx, dy := cr.X-me.X, cr.Y-me.Y
					if dm := chebyshev(me, cr); dm > 12 {
						dx, dy = dx*12/dm, dy*12/dm
					}
					sx, sy := screenPointToward(me, dx, dy)
					walkToHold(sx, sy, 140)
				}
				time.Sleep(60 * time.Millisecond)
				continue
			}
		}

		// CONFINEMENT BREAK-OUT: sliding along a wall in Y looks like "progress" but goes nowhere.
		// If our recent positions span a thin X-strip (boxed against a wall), push HARD toward the
		// open-ground centroid, jittering N/S to find the gap the wall must have somewhere.
		posHist = append(posHist, me)
		if len(posHist) > 55 {
			posHist = posHist[1:]
		}
		if navi == nil && navGrid != nil && len(posHist) >= 50 {
			mnX, mxX, mnY, mxY := 1<<30, -(1 << 30), 1<<30, -(1 << 30)
			for _, h := range posHist {
				mnX, mxX = min(mnX, h.X), max(mxX, h.X)
				mnY, mxY = min(mnY, h.Y), max(mxY, h.Y)
			}
			// Confined = tiny span on one axis while we've clearly been trying (span on the other).
			if (mxX-mnX < 35 && mxY-mnY > 45) || (mxY-mnY < 35 && mxX-mnX > 45) {
				breakoutTick++
				dx, dy := walkCentroid.X-me.X, walkCentroid.Y-me.Y
				// Jitter perpendicular to the centroid heading every few ticks to slide along the
				// wall and find its opening, while biasing toward open ground.
				jitter := ((breakoutTick / 4) % 3) - 1 // -1, 0, +1
				dx += -dy * jitter / 2
				dy += dx * jitter / 2
				msx, msy := screenPointToward(me, dx, dy)
				logger.Info("breakout", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
					"xspan", mxX-mnX, "yspan", mxY-mnY, "toward", fmt.Sprintf("(%d,%d)", walkCentroid.X, walkCentroid.Y))
				walkTo(msx, msy)
				if breakoutTick > 40 { // clear history so we re-evaluate after a real relocation
					posHist = nil
					breakoutTick = 0
				}
				continue
			}
		}

		// GLOBAL UNSTICK / self-repair: track NET progress, not per-tick movement (the character can
		// oscillate in place against a wall and look like it's moving). If we haven't advanced
		// 15+ units for a while, we're walled or the grid cell is slightly misaligned — break
		// free with a rotating heuristic heading (the wall-escape that already works).
		if chebyshev(me, progressPos) >= 15 {
			progressPos, progressAt = me, time.Now() // real net progress made — reset the stuck timer
		}
		stuckDur := time.Since(progressAt)
		// In nav mode the Navigator's own localEscape handles normal recovery deliberately, so the
		// rotating-heading unstick stays OFF (it fought the Navigator and caused the N/S "running
		// around"). Keep it ONLY as a long-threshold freeze-breaker: if she's made no net progress
		// for 6s+ (truly wedged, e.g. Navigator can't plan a route out), fire it once to break free.
		freezeThresh := 2500 * time.Millisecond
		if navi != nil {
			freezeThresh = 6000 * time.Millisecond
		}
		if stuckDur > freezeThresh {
			// PACING: this block was written when walkTo BLOCKED for the hold — one heading
			// per ~320ms. Continuous locomotion made walkTo non-blocking, so the compass spun
			// at CPU speed (13,807 unstick lines in one Den pocket, ~3ms apart — the user
			// watched him "run in circles by the stairs"). Fire at the design cadence; the
			// held force-move keeps walking between firings.
			if time.Since(lastUnstickAt) < 350*time.Millisecond {
				time.Sleep(40 * time.Millisecond)
				continue
			}
			lastUnstickAt = time.Now()
			// Drop (once) and remember the destination we failed to reach, so exploration steers
			// away from this walled-off region next time.
			if exploreDest.X != 0 {
				badDests = append(badDests, exploreDest)
				if len(badDests) > 24 {
					badDests = badDests[len(badDests)-24:]
				}
				exploreDest = data.Position{}
			}
			// Escalate: brief snags rotate headings fast; a long trap commits to each heading
			// far longer so the character actually travels the length of the wall and rounds its end.
			breakDir++
			div, amp := 4, 300.0
			if stuckDur > 9*time.Second {
				div, amp = 16, 360.0
			}
			angle := float64((breakDir/div)%8) / 8.0 * 2 * math.Pi
			logger.Info("unstick", "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y),
				"dir", (breakDir/div)%8, "deep", stuckDur > 9*time.Second)
			walkTo(cx+int(amp*math.Cos(angle)), cy+int(amp*0.47*math.Sin(angle)))
			continue
		}

		// Keep the current target until it's dead/gone; else pick nearest non-blacklisted.
		// COMBAT ORACLE v1 — posture before targets. Founding incident: a bow necromancer
		// charged a 12-density pack with two minions and died. Threat = weighted enemy mass in
		// judgment range; strength = the army + our pools. Postures:
		//   engage  — fight normally.
		//   kite    — shoot from range, NEVER advance into the pack; step back when crowded.
		//   regroup — too weak for this pack: pull back, raise from any corpse, refresh golem;
		//             with nothing to raise, walk away from the pack entirely (live to farm).
		threat, strength, packN, packCentroid := packAssess(d, me)
		petsAlive := 0
		for i := range d.Monsters {
			if d.Monsters[i].IsPet() {
				petsAlive++
			}
		}
		posture := "engage"
		if threat > strength*3 {
			posture = "regroup"
		} else if threat > strength {
			posture = "kite"
		}
		// POSTURE HYSTERESIS: at the boundary the raw comparison flips every tick (run 24:
		// kite<->engage 12x/s at threat 17-20 vs strength 19 — retreat/advance jitter).
		// A change must be a BIG swing (margin >= 4) or survive a 1.2s dwell. Regroup is
		// the flee-for-your-life read and always applies immediately.
		if posture != lastPosture && posture != "regroup" && lastPosture != "" {
			if time.Since(postureHeldAt) < 1200*time.Millisecond && abs(threat-strength) < 4 {
				posture = lastPosture // signal too small to flip on
			}
		}
		if posture != lastPosture {
			logger.Info("posture", "now", posture, "threat", threat, "strength", strength, "pets", petsAlive)
			lastPosture = posture
			postureHeldAt = time.Now()
		}
		if posture == "regroup" && packN > 0 {
			// Away from the pack, not through it. Corpse-raising happens via the summon block
			// next tick once we're clear; with nothing to raise this simply leaves the pack.
			retreat := data.Position{X: me.X + (me.X-packCentroid.X), Y: me.Y + (me.Y-packCentroid.Y)}
			if chebyshev(me, packCentroid) > 25 {
				// Far enough — hold here so summoning can happen; do not re-approach.
				time.Sleep(200 * time.Millisecond)
			} else {
				navWalk(me, retreat)
			}
			continue
		}

		var target data.Monster
		haveTarget := false
		if targetID != 0 {
			if m, ok := d.Monsters.FindByID(targetID); ok && m.Stats[stat.Life] > 0 {
				target, haveTarget = m, true
			}
		}
		if !haveTarget {
			// TARGET SCORING. Nearest-first is anti-Rabies: Rabies is a SPREADING disease whose
			// value is infecting a FRESH host standing among uninfected neighbours. Re-biting an
			// already-poisoned monster neither stacks nor re-spreads — it is a wasted bite. So we
			// score rather than sort by distance, and let distance be one term among several.
			bestScore := -(1 << 30)
			for _, m := range d.Monsters.Enemies() {
				if m.Stats[stat.Life] <= 0 {
					continue
				}
				if bl, ok := blacklist[m.UnitID]; ok && time.Since(bl) < 12*time.Second {
					continue // recently unreachable / undamageable — don't re-hump the same wall
				}
				dd := chebyshev(me, m.Position)
				if dd > *radius {
					continue
				}
				s := -dd * 4 // nearer is better, all else equal
				// Shamans rebuild the pack behind her while she chews trash. Kill them first.
				if m.IsMonsterRaiser() {
					s += 600
				}
				if m.IsElite() {
					s += 150
				}
				// Already infected: the poison is doing its work — go start another fire.
				if m.States.HasState(state.Poison) {
					s -= 300
				}
				// Reward hosts with uninfected neighbours: that is where the spread pays.
				for _, n := range d.Monsters.Enemies() {
					if n.UnitID != m.UnitID && n.Stats[stat.Life] > 0 &&
						chebyshev(m.Position, n.Position) <= 10 && !n.States.HasState(state.Poison) {
						s += 25
					}
				}
				// Poison-immune is deprioritised, NOT skipped: the werewolf bite still lands
				// physical damage, so it is killable — just slow. Hard-filtering would let an
				// immune monster body-block a corridor forever. The damage watchdog below is the
				// real backstop, and it catches immunities we never thought to check for.
				if m.IsImmune(stat.PoisonImmune) {
					s -= 500
				}
				if s > bestScore {
					bestScore, target, haveTarget = s, m, true
				}
			}
			if haveTarget {
				targetID = target.UnitID
				lastDist, stuckCount = 1<<30, 0
			}
		}
		if !haveTarget {
			targetID = 0

			// LOOT (gated behind -loot): combat already has priority (we're only here because
			// there's no live target), and survival already ran this tick. Never loot with an
			// enemy still in engage range or while chickening — safety over greed. The claim
			// adds hysteresis: no diving at loot the instant a target blinks out of the list.
			if *loot && chickenStreak == 0 && claim("loot", 30, 900*time.Millisecond) {
				enemyNearby := false
				for _, m := range d.Monsters.Enemies() {
					if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= 25 { // loot-safety: 25, NOT -radius(90) — the 90 gate meant "never loot"
						enemyNearby = true
						break
					}
				}
				if !enemyNearby {
					bestDist := 1 << 30
					var lootTarget data.Item
					haveLoot := false
					for _, it := range d.Inventory.ByLocation(item.LocationGround) {
						if bl, ok := lootBlacklist[it.UnitID]; ok && time.Now().Before(bl) {
							continue // recently failed to pick up — skip so we don't hump the same item
						}
						if !*lootAll && !lootWorthy(d, it) {
							continue // filter: uniques/sets/rares/crafted, gold, needed potions
						}
						dd := chebyshev(me, it.Position)
						if dd < bestDist && dd <= *lootradius {
							bestDist, lootTarget, haveLoot = dd, it, true
						}
					}
					if haveLoot {
						// Stand a couple tiles OFF the item — its clickable label hides under the
						// character when you're on top of it.
						if bestDist <= 2 {
							sx, sy := screenPointToward(me, me.X-lootTarget.Position.X, me.Y-lootTarget.Position.Y)
							walkToHold(sx, sy, 150)
							continue
						}
						if bestDist > 5 {
							// Ride the NAV GRID, not a straight line — the Den maze proved a
							// wall-adjacent item pulls the straight aim into rock forever. And
							// track progress: no closing for 6s = unreachable, 45s blacklist.
							if lootTarget.UnitID != lootApproachID {
								lootApproachID, lootApproachBest, lootApproachAt = lootTarget.UnitID, bestDist, time.Now()
							} else if bestDist < lootApproachBest {
								lootApproachBest, lootApproachAt = bestDist, time.Now()
							} else if time.Since(lootApproachAt) > 6*time.Second {
								lootBlacklist[lootTarget.UnitID] = time.Now().Add(45 * time.Second)
								lootApproachID = 0
								logger.Info("loot: unreachable — blacklisting", "name", string(lootTarget.Name), "dist", bestDist)
								continue
							}
							if time.Since(lootApproachLog) > 2*time.Second {
								logger.Info("loot: approach", "name", string(lootTarget.Name), "dist", bestDist,
									"pos", fmt.Sprintf("(%d,%d)", me.X, me.Y))
								lootApproachLog = time.Now()
							}
							navWalk(me, lootTarget.Position)
							continue
						}
						// Hover-sweep to the item's label (game-confirmed) and click it.
						// (No extra settle: hoverPickClick already polls the ground for the pickup.)
						hoverPickClick(lootTarget.Position, lootTarget.UnitID)
						stillThere := false
						for _, it := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
							if it.UnitID == lootTarget.UnitID {
								stillThere = true
								break
							}
						}
						if stillThere {
							lootAttempts[lootTarget.UnitID]++
							logger.Info("loot: attempt failed", "name", string(lootTarget.Name),
								"attempts", lootAttempts[lootTarget.UnitID])
							if lootAttempts[lootTarget.UnitID] >= 5 {
								lootBlacklist[lootTarget.UnitID] = time.Now().Add(15 * time.Second)
								delete(lootAttempts, lootTarget.UnitID)
								logger.Info("loot: blacklisting", "name", string(lootTarget.Name))
							}
						} else {
							logger.Info("loot: picked up", "name", string(lootTarget.Name))
							delete(lootAttempts, lootTarget.UnitID)
						}
						continue
					}
				}
			}

			// OBJECTS (gated behind -objects): open chests / smash barrels / use selectable
			// objects when idle and safe. Same hoverPickClick primitive as loot. Skips waypoints
			// (would open the travel panel) and blacklists each object after use.
			if *objects && chickenStreak == 0 && claim("objects", 25, 900*time.Millisecond) {
				enemyNearby := false
				for _, m := range d.Monsters.Enemies() {
					if m.Stats[stat.Life] > 0 && chebyshev(me, m.Position) <= 25 { // loot-safety: 25, NOT -radius(90) — the 90 gate meant "never loot"
						enemyNearby = true
						break
					}
				}
				if !enemyNearby {
					// WAYPOINT: activate on sight — the network is free fast-travel infrastructure
					// and the bot used to walk right past unactivated pads (user-reported at Stony).
					// WPs hover-click fine (unlike NPCs, -wpat proven); the panel that opens on
					// activation is closed with ESC — safe here because a panel IS open (the trap
					// is esc with nothing open), and only if the click was hover-confirmed.
					wpBusy := false
					for _, o := range d.Objects {
						if !o.IsWaypoint() || wpTouched[o.ID] {
							continue
						}
						if dd := chebyshev(me, o.Position); dd <= *objradius {
							wpBusy = true
							if dd > 5 {
								navWalk(me, o.Position)
								time.Sleep(40 * time.Millisecond)
							} else {
								wpTouched[o.ID] = true
								if hoverPickClick(o.Position, o.ID) {
									time.Sleep(900 * time.Millisecond)
									moveStop()
									hid.PressKey(hid.GetASCIICode("esc")) // close the WP panel
									logger.Info("waypoint: ACTIVATED", "area", int(d.PlayerUnit.Area),
										"pos", fmt.Sprintf("(%d,%d)", o.Position.X, o.Position.Y))
								} else {
									logger.Warn("waypoint: hover-click failed — skipping", "id", int(o.ID))
								}
							}
							break
						}
					}
					if wpBusy {
						continue
					}
					var objTarget data.Object
					bestDist, haveObj := 1<<30, false
					for _, o := range d.Objects {
						if !o.Selectable || o.IsWaypoint() {
							continue
						}
						if bl, ok := objBlacklist[o.ID]; ok && time.Since(bl) < 60*time.Second {
							continue
						}
						if dd := chebyshev(me, o.Position); dd < bestDist && dd <= *objradius {
							bestDist, objTarget, haveObj = dd, o, true
						}
					}
					if haveObj {
						if bestDist > 6 {
							sx, sy := screenPointToward(me, objTarget.Position.X-me.X, objTarget.Position.Y-me.Y)
							logger.Info("object: approach", "name", int(objTarget.Name), "dist", bestDist)
							walkToHold(sx, sy, min(240, max(90, bestDist*9))) // scale by distance — no overshoot
							continue
						}
						objBlacklist[objTarget.ID] = time.Now()
						ok := hoverPickClick(objTarget.Position, objTarget.ID)
						logger.Info("object: interacted", "name", int(objTarget.Name), "clicked", ok,
							"pos", fmt.Sprintf("(%d,%d)", objTarget.Position.X, objTarget.Position.Y))
						time.Sleep(250 * time.Millisecond) // let a chest disgorge / barrel drop
						continue
					}
				}
			}

			// EXPLORE: frontier-driven, not random. The atlas knows which walkable cells touch
			// UNKNOWN space; walking to one loads rooms, grows the atlas, and moves the frontier —
			// by construction never aimless. Random-far-cell picking and the blind wander circle
			// generator are DELETED. Execution goes through the Mover (LOS-lookahead executor).
			if navGrid != nil {
				// RESUME RESET: same watchdog-aging bug as goto — a combat/loot preemption is
				// not an explore stall; don't let it drop the destination into badDests.
				if time.Since(lastExploreTickAt) > 1200*time.Millisecond {
					exploreBestAt = time.Now()
				}
				lastExploreTickAt = time.Now()
				// COMMITMENT: hold the current destination until arrival or a genuine progress
				// stall — the old flat 25s re-pick let FrontierNear retarget mid-walk to a frontier
				// BEHIND us (rooms loading shift the "nearest"), producing the back-and-forth walk
				// the user watched. Progress = best-dist-so-far improving; stall = 12s without.
				if exploreDest.X != 0 {
					if dd := chebyshev(me, exploreDest); dd < exploreBestDist-3 || exploreBestDist == 1<<30 {
						exploreBestDist, exploreBestAt = dd, time.Now()
					}
				}
				arrived := exploreDest.X != 0 && chebyshev(me, exploreDest) < 8
				stalled := exploreDest.X != 0 && time.Since(exploreBestAt) > 12*time.Second &&
					time.Since(exploreSince) > 12*time.Second
				if stalled {
					// Unreachable as planned — remember it so we don't re-pick it immediately.
					badDests = append(badDests, exploreDest)
					if len(badDests) > 24 {
						badDests = badDests[len(badDests)-24:]
					}
					logger.Info("explore: stalled — dropping destination",
						"dest", fmt.Sprintf("(%d,%d)", exploreDest.X, exploreDest.Y), "bestDist", exploreBestDist)
				}
				need := exploreDest.X == 0 || arrived || stalled
				if need {
					exploreDest, exploreBestDist = data.Position{}, 1<<30
					exploreBestAt = time.Now()
					if fp, ok := atlas.FrontierNear(gr.MapSeed(), int(d.PlayerUnit.Area), me); ok && !nearAny(fp, badDests, 30) {
						exploreDest, exploreSince = fp, time.Now()
						logger.Info("explore: frontier", "to", fmt.Sprintf("(%d,%d)", fp.X, fp.Y),
							"dist", chebyshev(me, fp))
					} else if len(walkables) > 0 {
						// Fully-mapped area (or frontier unreachable): sweep known ground far from here.
						for tries := 0; tries < 40; tries++ {
							c := walkables[rand.Intn(len(walkables))]
							w := data.Position{X: c.X + navGrid.OffsetX, Y: c.Y + navGrid.OffsetY}
							if chebyshev(me, w) > 70 && !nearAny(w, badDests, 50) {
								exploreDest, exploreSince = w, time.Now()
								break
							}
						}
					}
				}
				if exploreDest.X != 0 {
					navWalk(me, exploreDest)
					time.Sleep(40 * time.Millisecond)
					continue
				}
			}
			// No grid at all: hold position briefly rather than walk circles.
			time.Sleep(300 * time.Millisecond)
			continue
		}

		dist := chebyshev(me, target.Position)
		sx, sy := gameToScreen(gr, me.X, me.Y, target.Position.X, target.Position.Y)
		// The fight holds the intent ledger like any other journey: contact outranks a
		// critical pickup (never walk through a swing), plain engaging yields to it.
		combatPrio := 50
		if dist <= 8 {
			combatPrio = 60
		}
		if !claim("combat", combatPrio, 900*time.Millisecond) {
			time.Sleep(60 * time.Millisecond)
			continue
		}
		// Attack mode for THIS tick, derived from live equipment/skills/bindings — see
		// deriveCombat. Ranged (bow in hand): the legacy flag path, -attackrange engage.
		// Not ranged: a bound damage spell casts at -castrange when mana allows; otherwise
		// close to melee reach and swing (left skill is Attack on a fresh char).
		prof := deriveCombat(d)
		manaOK := d.PlayerUnit.MPPercent() >= *meleeBelow
		useCast := *rabies != "" && manaOK
		spellCast := !prof.ranged && prof.hasCast && manaOK
		engageRange := *attackRange
		switch {
		case spellCast:
			engageRange = *castRange
		case useCast:
			engageRange = *castRange
		case !prof.ranged:
			engageRange = 4 // stick in hand: walk up and swing, don't snipe air
		}
		// CURSE PASS: one Amp Damage on the pack multiplies the whole army's physical
		// damage — the summoner's actual job. Its own tick, then the next tick attacks.
		if prof.hasCurse && manaOK && dist <= *castRange &&
			time.Since(lastCurseAt) > 8*time.Second {
			moveStop()
			hid.PressKeyBinding(prof.curseKB)
			time.Sleep(60 * time.Millisecond)
			hid.Click(game.RightButton, sx, sy)
			lastCurseAt = time.Now()
			logger.Info("curse cast", "skill", int(prof.curseID), "dist", dist)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if dist <= engageRange {
			// Damage watchdog — see engagedID above. Must run BEFORE the bite, because the bite
			// resets stuckCount and would otherwise trap us here forever.
			life := target.Stats[stat.Life]
			switch {
			case target.UnitID != engagedID:
				engagedID, engagedLife, engagedAt = target.UnitID, life, time.Now()
			case life < engagedLife:
				engagedLife, engagedAt = life, time.Now() // it's dying: reset the clock
			case time.Since(engagedAt) > 8*time.Second:
				// NOTE: on 3.2 the Life stat is frozen (32768) so "life never dropped" fires for
				// every kill too — dead targets are now excluded in Enemies() by Mode instead, and
				// this watchdog only catches genuinely unkillable/walled targets. 8s because a
				// level-1 char legitimately needs >4s on tougher monsters.
				logger.Warn("target takes no damage in melee — blacklisting",
					"unitID", target.UnitID, "name", int(target.Name),
					"mode", int(target.Mode),
					"poisonImmune", target.IsImmune(stat.PoisonImmune),
					"life", life, "engagedSecs", int(time.Since(engagedAt).Seconds()))
				blacklist[target.UnitID] = time.Now()
				targetID, engagedID = 0, 0
				continue
			}
			// AIM ORACLE: the cursor is still parked where the PREVIOUS bite aimed, so this tick's
			// HoverData is the game's own verdict on whether that aim actually lands on a unit.
			// A run full of hover=false means gameToScreen is off for WORLD clicks — the exact
			// question the desktop's panel-proven mapping left open.
			logger.Info("bite", "dist", dist, "hp", d.PlayerUnit.HPPercent(), "mp", d.PlayerUnit.MPPercent(),
				"pos", fmt.Sprintf("(%d,%d)", me.X, me.Y), "targetUnit", int(target.UnitID),
				"hover", d.HoverData.IsHovered, "hoverUnit", int(d.HoverData.UnitID), "cast", useCast)
			switch {
			case spellCast:
				// Bound damage spell: select it via the game's own binding, right-click cast.
				moveStop()
				hid.PressKeyBinding(prof.castKB)
				time.Sleep(50 * time.Millisecond)
				hid.Click(game.RightButton, sx, sy)
			case useCast:
				// Right-click cast: select the skill by hotkey, cast at the target. (Message-only
				// right-clicks are reliable; left needs the VK_LBUTTON treatment below.)
				hid.PressKey(hid.GetASCIICode(*rabies))
				time.Sleep(50 * time.Millisecond)
				hid.Click(game.RightButton, sx, sy)
			case *melee != "":
				// Bound melee hotkey (e.g. Attack on right-click) — same right-click path.
				hid.PressKey(hid.GetASCIICode(*melee))
				time.Sleep(50 * time.Millisecond)
				hid.Click(game.RightButton, sx, sy)
			default:
				// No mana, no melee key: LEFT-click normal attack (fresh chars have Attack as the
				// left skill), hover-confirmed so it registers as an ATTACK, not a walk.
				meleeSwing(target.UnitID, target.Position)
			}
			time.Sleep(200 * time.Millisecond)
			lastDist, stuckCount = dist, 0
		} else {
			// Drop (and blacklist) the target if we're not getting closer — it's fleeing or
			// walled off. Blacklisting stops us re-picking the same unreachable monster.
			if dist < lastDist-1 {
				stuckCount = 0
			} else {
				stuckCount++
			}
			lastDist = dist
			// SUMMONER PATIENCE: with engage=4 every target starts far, closing takes real
			// time, and monsters dance with the skeletons — the old threshold blacklisted
			// whole packs in seconds (observed: 9 blacklists in 6s, Den of Evil). If the
			// ARMY is already on the target, Benji doesn't need to reach it at all: hold
			// position and let them work.
			stuckLimit := 8
			if !prof.ranged {
				stuckLimit = 20
				if stuckCount >= 8 {
					armyOnIt := false
					for i := range d.Monsters {
						if d.Monsters[i].IsPet() && chebyshev(d.Monsters[i].Position, target.Position) <= 8 {
							armyOnIt = true
							break
						}
					}
					// Army engaged: never blacklist (the fight is happening, we're just late).
					// KITE posture holds back — the wall works while Benji stays whole. In a
					// FAVORABLE fight, keep pushing in to join the kill: hanging back while the
					// skeletons win alone was the "maintains distance until monsters are dead"
					// passivity the user called out.
					if armyOnIt {
						stuckCount = 0
						if posture == "kite" {
							time.Sleep(250 * time.Millisecond)
							continue
						}
					}
				}
			}
			if stuckCount >= stuckLimit {
				logger.Info("not closing on target, blacklisting", "dist", dist)
				blacklist[targetID] = time.Now()
				targetID = 0
				continue
			}
			// CLOSE-RANGE FINAL APPROACH: within short reach walk STRAIGHT with distance-scaled
			// pulses (a long hold overshoots past the monster — the original back-and-forth).
			if dist <= 30 {
				msx, msy := screenPointToward(me, target.Position.X-me.X, target.Position.Y-me.Y)
				hold := dist * 9
				if hold < 80 {
					hold = 80
				}
				if hold > 220 {
					hold = 220
				}
				walkToHold(msx, msy, hold)
				time.Sleep(40 * time.Millisecond)
				continue
			}
			if posture == "kite" {
				// Kiting: do NOT close on a pack that outweighs the army. Step back when crowded,
				// otherwise hold and let the bite fire at whatever drifts into range.
				if packN > 0 && chebyshev(me, packCentroid) < *attackRange-5 {
					retreat := data.Position{X: me.X + (me.X-packCentroid.X)/2, Y: me.Y + (me.Y-packCentroid.Y)/2}
					navWalk(me, retreat)
				} else {
					time.Sleep(150 * time.Millisecond)
				}
				continue
			}
			// Long chase rides the Mover — the movelab executor with dest tolerance, so a drifting
			// monster keeps the plan instead of resetting it every tick.
			if shiftCheck%4 == 0 {
				logger.Info("chase", "dist", dist, "me", fmt.Sprintf("(%d,%d)", me.X, me.Y),
					"mon", fmt.Sprintf("(%d,%d)", target.Position.X, target.Position.Y))
			}
			navWalk(me, target.Position)
			time.Sleep(40 * time.Millisecond)
		}
	}
	if err := atlas.Save(); err != nil {
		logger.Warn("atlas: save failed", "err", err)
	} else {
		logger.Info("atlas: saved")
	}
	logger.Info("done")
}
