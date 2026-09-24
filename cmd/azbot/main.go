// azbot — the intelligent successor to farmbot. Design: docs/AZBOT_DESIGN.md.
// This entrypoint implements M0 (attach & epistemics gate) + M1 (perceive & survive)
// + the owner's kill-switch. Activities, verbs, and the arbiter arrive in M2+.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/sentinel"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
	"github.com/hectorgimenez/koolo/internal/azbot/unstick"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/azbot/watchdog"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

func chebyshev(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

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

// runReplay drives the DEMAND layer + arbiter over a recorded snapshot stream.
// Step never runs (no world to act on), so grants churn on rule-zero eviction —
// exactly the layer where tonight's decision bugs lived (starvation, dead bands,
// class wars). Output: one line per grant change, with the self-model beside it.
func runReplay(logger *slog.Logger, path string, legs []activity.Leg) {
	f, err := os.Open(path)
	if err != nil {
		logger.Error("replay: open failed", "err", err)
		return
	}
	defer f.Close()
	// The live executive's own roster: replay bids exactly what the loop bids.
	roster := activity.Registry(legs, nil)
	arb := &arbiter.Arbiter{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	frame, last, live := 0, "", "-"
	for sc.Scan() {
		// Decision frames ride beside the snapshots (newer files): they name the
		// live holder for comparison and are not frames to decide on.
		if df, ok := trace.IsFrame(sc.Bytes()); ok {
			live = df.Holder
			if live == "" {
				live = "-"
			}
			continue
		}
		frame++
		var s percept.Snapshot
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			continue
		}
		demands := roster.Demands(&s)
		grant, ch := arb.Decide(demands)
		if ch.Changed() || (grant == nil && last != "-") {
			holder, urg := "-", 0.0
			if grant != nil {
				holder, urg = grant.Demand.Who, grant.Demand.Urgency
			}
			if holder != last {
				near := 0
				for _, e := range s.Enemies {
					if abs(s.Me.Pos.X-e.Pos.X) <= 25 || abs(s.Me.Pos.Y-e.Pos.Y) <= 25 {
						near++
					}
				}
				logger.Info("replay", "frame", frame, "t", s.At.Format("15:04:05"),
					"grant", holder, "urgency", fmt.Sprintf("%.2f", urg),
					"hp", s.Me.HPPct, "area", int(s.Me.Area), "near", near,
					"weapon", s.Me.WeaponKind, "bids", len(demands), "why", ch.Why(), "live", live)
				last = holder
			}
		}
	}
	logger.Info("replay: done", "frames", frame)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func vkOf(name string) int {
	switch strings.ToLower(name) {
	case "pause", "break":
		return 0x13
	case "f9":
		return 0x78
	case "f10":
		return 0x79
	case "f11":
		return 0x7A
	case "f12":
		return 0x7B
	case "scrolllock", "scroll":
		return 0x91
	}
	if len(name) == 1 {
		return int(strings.ToUpper(name)[0])
	}
	return 0x13
}

func main() {
	seconds := flag.Int("seconds", 3600, "run budget in seconds — then the SAFE END: the session winds down (TP to town or a quiet field; never exits with a monster within 40), then -pausefailsafe, then disengage and hold (see -winddown). logs/stop.now and a first Ctrl-C do the same")
	windDownF := flag.Duration("winddown", exec.WindCap, "the SAFE END's Recall budget: this long to TP to town (or find a field quiet 3s) before the next rung — the pause (-pausefailsafe) or disengage-and-hold")
	pauseFailsafeF := flag.Bool("pausefailsafe", true, "the SAFE END's pause rung: when the Recall budget is spent, Recall gives up (no tome / an empty one) or HP falls under the flee floor (33%), open the pause menu (one ESC on a clear screen, seen within 2s, one retry) and exit with it LEFT UP; not confirmed → disengage and hold. Live test R8 verifies the pause freezes the offline world — if it does not, this default flips to false and the ladder goes straight from Recall to disengage-and-hold (tome and HP then do not end the Recall rung)")
	disengagedF := flag.Bool("disengaged", false, "start DISENGAGED (teaching mode): full perception, screen shadow, trace/state lines and flight recorder, ZERO input — no injector stubs, no attach amnesty, no calibration or startup hygiene — until F10 engages")
	dpiScale := flag.Float64("dpiscale", 1.25, "display scale (this laptop: 1.25)")
	fakeFocus := flag.Bool("fakefocus", true, "background play: post WM_ACTIVATE-family messages so D2R keeps its hover oracle alive while another window has the foreground (never takes focus, never clips the cursor)")
	worldScaleF := flag.Float64("worldscale", 0, "world-aim scale override (logical client px -> world cursor px). 0 = the display scale; the per-client projection correction comes from -aimcal instead")
	aimCalPath := flag.String("aimcal", "logs/aimcal.json", "measured world-projection calibration from build/aimcal.exe (applied when its client size matches)")
	moveKey := flag.String("move", "e", "Force Move key (D2R Options>Controls binding)")
	swapKey := flag.String("swap", "w", "weapon-swap key (the bowzon dance: bow at range, javelin at contact)")
	killKey := flag.String("killswitch", "f10", "hotkey to toggle bot control (f9|f10|f11|pause|scrolllock — NOT f12, Windows reserves it for debuggers). Disengage heals all input patches — the human owns Diablo instantly.")
	belt := flag.String("belt", "1,2,3,4", "belt column keys")
	drinkAt := flag.Int("drinkat", 60, "sentinel drinks at or below this HP%")
	memDir := flag.String("memdir", "logs/azmem", "memory store directory (WAL)")
	goal := flag.String("goal", "farm", "the Director's current goal: farm (routes+explore+loot) | campaign/rampage (Act 1 march) | gamble (Gheed errand when bankrolled). Goals shape WHICH activities bid; the arbiter still owns every moment.")
	meleeKeyF := flag.String("meleekey", "", "OWNER-DECLARED melee skill key (e.g. f1 for Jab) — config beats inference on a scrambled mod; overrides calibration")
	rangedKeyF := flag.String("rangedkey", "", "OWNER-DECLARED bow skill key — overrides calibration (the flinch audit still verifies)")
	leftSkillF := flag.String("leftskill", "auto", "LEFT-button primary strike (the owner's Carnage, 2026-09-24): auto = primary when calibration reads a non-basic, owned PlayerUnit.LeftSkill | on = force it primary whatever calibration saw | off = never (right-skill strikes only, the pre-Carnage order)")
	tpKeyF := flag.String("tpkey", "", "OWNER-DECLARED Town Portal (tome) key, e.g. f1 — overrides calibration; none/off = no town portal (Recall, Unstick, Withdraw and Breakout skip their portal paths). Empty = calibration decides")
	calKeysF := flag.String("calkeys", combat.DefaultExtraCalibrationKeys, "EXTRA skill hotkeys calibration probes after F1-F8 (comma list). Only F1-F11 are ever pressed — never the killswitch, never F12, never a letter/digit/Tab/Enter/Esc (panels, belt, chat)")
	invKeyF := flag.String("invkey", "i", "inventory-panel toggle key (the Equip service's door; KeyBindings memory is dead, so declare it if rebound)")
	roadTest := flag.Bool("roadtest", false, "M2 soak: walk the measured town road out and back on Stride verbs, print the outcome histogram, exit")
	jTest := flag.String("jtest", "", "M3 soak: journey to world x,y on the live grid via the Journey authority, print the verdict, exit")
	fightTest := flag.Bool("fighttest", false, "M4 soak: calibrate capability, cross to Blood Moor, hover-strike nearest enemies with evidence, exit")
	portalTest := flag.Bool("portaltest", false, "manual harness: find the nearest portal in the snapshot, approach if far, click through with the EnterPortal verb, report every state change, exit")
	charsiTest := flag.Bool("charsitest", false, "manual harness: walk to Charsi, open TRADE (menu byte + Down/Enter), screenshot the shop for repair-button calibration; with -repairxy also click it and report the gold delta, exit")
	repairXY := flag.String("repairxy", "", "client x,y of the repair button (measured from logs/charsi_shop.png)")
	selfTest := flag.Bool("selftest", false, "PREFLIGHT (in town, D2R focused, hands off): prove belt reads, NPC aim, and the full trade chain by buying ONE healing potion; prints PASS/FAIL per primitive and exits")
	akaraTest := flag.Bool("akaratest", false, "manual harness: find Akara, open TRADE, dump belt self-model + her readable stock (LocationVendor), screenshot for slot calibration; with -buy also execute the restock plan (1 row mana, rest HP) and report gold/belt deltas, exit")
	buyPots := flag.Bool("buy", false, "akaratest: execute the potion purchases (needs gold)")
	gambleBuy := flag.String("gamblebuy", "", "gamble-buy the item at client pixel 'x,y' and read the rolled result the same frame (gold delta + full affix dump)")
	gambleRefresh := flag.Bool("gamblerefresh", false, "read the parked cursor as the refresh button, click it, verify the stock re-rolls")
	gambleDump := flag.Bool("gambledump", false, "PURE READ: dump the open gamble/vendor screen's stock with quality+affixes+stats — tests the pre-roll hypothesis, touches nothing")
	invDump := flag.Bool("invdump", false, "dump inventory/belt/equipped with NUMERIC item IDs (the mod scrambles names; IDs cannot lie), exit")
	shopPick := flag.String("shoppick", "", "akaratest: uiClick a client pixel 'x,y', read the item that lands on the CURSOR (true identity from memory), then click again to put it back — the click-based slot oracle")
	shopMap := flag.String("shopmap", "", "akaratest: hover-sweep the shop panel 'x0,y0,x1,y1,step' and log which stock item the GAME says is hovered at each point — builds the true pixel map empirically")
	exitProbe := flag.Bool("exitprobe", false, "PURE READ: dump the current area's AdjacentLevels (raw + live-translated), live entrance units, and the BFS hop toward the next Act 1 leg — validates the crossing knowledge before the Advance activity trusts it")
	missileProbe := flag.Int("missileprobe", 0, "PURE READ: sample the missile table for N seconds and print every projectile with measured velocity — validates the dodge oracle (stand near something that shoots)")
	relogTest := flag.Bool("relogtest", false, "RELOG DRILL (in game, hands off): one session relog end to end on the executive's Session FSM — pause menu by sight, Save and Exit, Play, new world — photographing every phase (logs/relog_<phase>.png), then report the seed and the corpse, exit. Live, create logs/relog.now to request one")
	wpCalTest := flag.Bool("wpcaltest", false, "P-10 phase 1 harness: walk onto the nearest waypoint, click it open, photograph the panel (logs/wp_panel.png), and dump the blueness map — re-derives the compass column and destination rows for THIS client, exit")
	replayF := flag.String("replay", "", "OFFLINE DECISION REPLAY: path to a flight .jsonl — every frame runs Demand + arbiter and prints the grant timeline. No game needed; live failures become desk-checkable evidence (the STE mentality: verify against recorded reality, not her blood)")
	exitXY := flag.String("exitxy", "", "relogtest: override the pause menu's Save and Exit button, screenshot x,y (default 958,822 on the 1920x1050 client)")
	playXY := flag.String("playxy", "", "relogtest: override the character screen's Play button, screenshot x,y (default 922,822)")
	janitorF := flag.Bool("janitor", false, "v2 step 6: the executive GATES every Step on the screen oracle and a janitor closes foreign panels by sight (replaces the pause sentry, startup hygiene, cursor-drop ESC, watchdog ESC probe and the services' blind ESCs). Also AZBOT_JANITOR=1")
	combatLearnF := flag.String("combatlearn", "on", "DYNAMIC STRIKE (the owner, 2026-09-24: Leap Attack's AoE vs Carnage's single target): on = choose by the learned kills/sec − HP − mana utility per (skill, cluster, distance) bucket, persisted per character+skill set | off = deterministic priors, no exploration (telemetry still measured and stored)")
	exploreF := flag.Float64("explore", 0.1, "DYNAMIC STRIKE exploration ε at zero samples (decays with the bucket's sample count; never below 50% life); 0 = never explore")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	flag.Parse()
	switch strings.ToLower(*combatLearnF) {
	case "off", "false", "0", "none":
		activity.SetCombatLearn(false, 0)
	default:
		activity.SetCombatLearn(true, *exploreF)
	}

	// ---- OFFLINE REPLAY: no attach, no injector, no game — pure decision review ----
	if *replayF != "" {
		legs := activity.FarmItinerary()
		if *goal == "campaign" || *goal == "rampage" {
			// Replay has no live area snapshot; keep the historical Act 1 route.
			legs = activity.CampaignItinerary(0)
		}
		runReplay(logger, *replayF, legs)
		return
	}

	// ---- attach (recipe proven by farmbot) ----
	if err := config.Load(); err != nil {
		logger.Error("config load failed", "err", err)
		return
	}
	cfg := config.Characters["Main"]
	game.SetPhysicalScale(*dpiScale)

	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("D2R.exe not running — load a game first")
		return
	}
	hwnd := findHWND(pid)
	logger.Info("azbot attaching", "pid", pid)
	gr, err := game.NewGameReader(cfg, "Main", pid, hwnd, logger)
	if err != nil {
		logger.Error("game reader failed", "err", err)
		return
	}
	motor.FakeFocus = *fakeFocus
	ws := *worldScaleF
	if ws <= 0 {
		ws = *dpiScale
	}
	game.SetWorldScale(ws)
	client := fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY)
	if buf, err := os.ReadFile(*aimCalPath); err == nil {
		var cal game.AimCal
		if json.Unmarshal(buf, &cal) == nil && cal.Client == client {
			game.SetAimCalibration(cal)
			logger.Info("aim calibration loaded", "kx", cal.KX, "ky", cal.KY, "ox", cal.OX, "oy", cal.OY)
		} else {
			logger.Warn("aim calibration IGNORED — measured on a different client size; run build/aimcal.exe", "file", *aimCalPath, "client", client, "cal", cal.Client)
		}
	} else {
		logger.Warn("no aim calibration — world aims use raw koolo constants; run build/aimcal.exe", "file", *aimCalPath)
	}
	logger.Info("world scale", "scale", ws, "client", client, "dpi", *dpiScale)
	logger.Info("navigation mode", "deliberate", os.Getenv("AZBOT_DELIBERATE") == "1")
	janitorOn := *janitorF || os.Getenv("AZBOT_JANITOR") == "1"
	logger.Info("ui mode", "janitor", janitorOn)
	gi, err := game.InjectorInit(logger, pid)
	if err != nil {
		logger.Error("injector init failed", "err", err)
		return
	}
	if *disengagedF {
		// DISENGAGED START: no stubs are loaded (F10's Reengage loads them); the
		// unload only heals whatever a prior run may have left patched — the
		// kill-switch's own heal, no input.
		_ = gi.Unload()
	} else if err := gi.Load(); err != nil {
		logger.Error("injector load failed", "err", err)
		return
	}
	defer func() { gi.Unload(); gi.Close() }()
	// THE SAFE END (relay R4): the executive's graceful stop requests — the
	// -seconds budget, logs/stop.now and a first Ctrl-C — all wind the session
	// down (exec.Session WindDown). A second Ctrl-C, a SIGTERM (console close
	// gives no time to wind down) or any signal before the executive runs
	// heals and exits at once, as before.
	var executiveLive atomic.Bool
	stopReq := make(chan string, 1)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range sigCh {
			if sig == os.Interrupt && executiveLive.Load() {
				select {
				case stopReq <- "signal (Ctrl-C)":
					logger.Warn("signal — winding down safely (town or a quiet field, then exit); Ctrl-C again exits NOW")
					continue
				default:
				}
			}
			logger.Info("signal — healing D2R input and exiting")
			gi.Unload()
			gi.Close()
			os.Exit(0)
		}
	}()
	hid := game.NewHID(gr, gi)
	// Modifier amnesty at attach AND exit (LIFO: this defer runs before gi.Unload's
	// heal): clear latched shift/ctrl/alt so nobody inherits a stuck modifier.
	// A -disengaged run sends neither until F10 first engages it.
	neverEngaged := *disengagedF
	if !neverEngaged {
		hid.ModifierAmnesty()
		game.SendModifierUpReal()
	}
	defer func() {
		if !neverEngaged {
			hid.ModifierAmnesty()
			game.SendModifierUpReal()
		}
	}()

	// ---- M0: epistemics gate ----
	p := percept.New(gr)
	// Let the game state settle before judging (load screens read garbage).
	var report percept.AttachReport
	for i := 0; i < 24; i++ {
		report = p.Gate()
		if report.OK() {
			break
		}
		// THE PRE-ATTACH MEDIC (01:19: a swap kill landed between a watchdog
		// probe's menu-open and its restore; the world froze behind the quit
		// menu and the NEXT process could never attach to read it — the
		// sentry lives inside the bot, the wedge lives outside it). Halfway
		// through the patience, one focused ESC: if a standing menu is the
		// blocker, this clears it; if not, the pause it raises is cleared by
		// the second press two cycles later.
		// RETIRED 2026-09-23: on a DEATH SCREEN the gate also fails, and ESC there
		// means "continue" — it respawned Fableboi in town and left his corpse in
		// the field; the second press then opened the pause menu. A blind ESC is
		// not a medic. The gate now fails loudly and the operator looks (shot.exe).
		// A PAUSED GAME READS AS GARBAGE (2026-09-23: position/stats invalid behind
		// the ESC menu — two attach failures that day were a stray pause). The
		// screen identifies it; the cure is a click on Return to Game, never ESC.
		if (i == 4 || i == 10) && !*disengagedF { // -disengaged: no input before F10
			if _, x, y, ok := game.UIBlocker(gr.Screenshot()); ok {
				hid.FocusGame()
				time.Sleep(200 * time.Millisecond)
				game.SendClickRealScreen(int(float64(x)/(*dpiScale))+gr.WindowLeftX, int(float64(y)/(*dpiScale))+gr.WindowTopY)
				logger.Warn("attach: gate failing behind a PAUSE/sub-panel screen — clicked it away", "try", i)
			}
		}
		if i == 12 {
			logger.Warn("attach: gate failing — NOT pressing anything; check the screen (death screen? menu?)", "try", i)
		}
		time.Sleep(500 * time.Millisecond)
	}
	logger.Info("attach report", "verdict", report.String())
	if !report.OK() {
		logger.Error("EPISTEMICS GATE FAILED — refusing to run on garbage reads. Is a character in-game?")
		return
	}

	// ---- memory + scribe ----
	mem, err := memory.Open(*memDir)
	if err != nil {
		logger.Error("memory open failed", "err", err)
		return
	}
	stop := make(chan struct{})
	go mem.Scribe(stop)

	// ---- motor + sentinel ----
	m := motor.New(logger, hid, gi, hid.GetASCIICode(*moveKey))
	m.SetPanelScale(*dpiScale)
	if *disengagedF {
		m.StartDisengaged() // before the sentinel starts: not one drink, not one key
	}
	var beltKeys []byte
	for _, k := range strings.Split(*belt, ",") {
		if k = strings.TrimSpace(k); k != "" {
			beltKeys = append(beltKeys, hid.GetASCIICode(k))
		}
	}
	sen := sentinel.New(logger, p, m, mem, sentinel.Config{
		KillVK: vkOf(*killKey), BeltKeys: beltKeys, DrinkAtHP: *drinkAt,
	})
	go sen.Run(stop)
	logger.Info("sentinel live", "killswitch", *killKey, "drinkAt", *drinkAt)
	mem.PutJSON("goal", memory.ScopeGame, memory.Provenance{Source: "owner"}, map[string]string{"goal": *goal})
	logger.Info("director", "goal", *goal)

	// ---- map knowledge: the area graph + exit positions (koolo-map subprocess; cached
	// by seed, so this costs ~3-5s ONCE per game). Without it Advance simply never bids —
	// honest absence, not a crash.
	if err := gr.FetchMapData(); err != nil {
		logger.Warn("map data unavailable — cross-area Advance disabled", "err", err)
	} else {
		logger.Info("map data fetched", "seed", gr.MapSeed(), "areas", len(gr.GetData().Areas))
	}
	// MAP FUSION: every navigation grid is live > atlas > trusted prior > priced unknown.
	fz := newFusion(gr, logger)
	defer fz.flush()

	if *exitProbe {
		// The training drill for the leg-walker: read everything Advance would act on,
		// touch nothing.
		d := gr.GetData()
		var lg *game.Grid
		if g, _, err := gr.BuildLiveGridRooms(); err == nil {
			lg = g
			logger.Info("exitprobe: live grid", "origin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY),
				"size", fmt.Sprintf("%dx%d", g.Width, g.Height))
		} else {
			logger.Warn("exitprobe: live grid failed", "err", err)
		}
		if d.AreaData.Grid != nil {
			logger.Info("exitprobe: map grid", "area", int(d.PlayerUnit.Area),
				"origin", fmt.Sprintf("(%d,%d)", d.AreaData.Grid.OffsetX, d.AreaData.Grid.OffsetY),
				"size", fmt.Sprintf("%dx%d", d.AreaData.Grid.Width, d.AreaData.Grid.Height))
		} else {
			logger.Warn("exitprobe: NO map grid for current area — is koolo-map serving this seed?")
		}
		logger.Info("exitprobe: me", "pos", fmt.Sprintf("(%d,%d)", d.PlayerUnit.Position.X, d.PlayerUnit.Position.Y),
			"area", int(d.PlayerUnit.Area), "adjacents", len(d.AdjacentLevels), "entrances", len(d.Entrances))
		for _, al := range d.AdjacentLevels {
			tx, ty := al.Position.X, al.Position.Y
			if lg != nil && d.AreaData.Grid != nil {
				tx += lg.OffsetX - d.AreaData.Grid.OffsetX
				ty += lg.OffsetY - d.AreaData.Grid.OffsetY
			}
			logger.Info("exitprobe: adjacent", "area", int(al.Area), "isEntrance", al.IsEntrance,
				"raw", fmt.Sprintf("(%d,%d)", al.Position.X, al.Position.Y),
				"live", fmt.Sprintf("(%d,%d)", tx, ty),
				"dist", chebyshev(d.PlayerUnit.Position, data.Position{X: tx, Y: ty}))
		}
		for _, en := range d.Entrances {
			logger.Info("exitprobe: live entrance", "name", int(en.Name), "id", int(en.ID),
				"pos", fmt.Sprintf("(%d,%d)", en.Position.X, en.Position.Y),
				"dist", chebyshev(d.PlayerUnit.Position, en.Position))
		}
		// THE LIVE BORDER ORACLE: cross-level rooms straight from D2R's room graph —
		// the geometry that cannot lie (map-data placement DOES lie on this mod).
		if ext, err := gr.AdjacentLevelRooms(); err == nil {
			for ar, rects := range ext {
				for _, r := range rects {
					logger.Info("exitprobe: LIVE border room", "area", int(ar),
						"rect", fmt.Sprintf("(%d,%d %dx%d)", r.X, r.Y, r.W, r.H),
						"dist", chebyshev(d.PlayerUnit.Position, data.Position{X: r.X + r.W/2, Y: r.Y + r.H/2}))
				}
			}
		} else {
			logger.Warn("exitprobe: room graph read failed", "err", err)
		}
		close(stop)
		return
	}

	if *missileProbe > 0 {
		// The dodge oracle's training drill: watch the missile table live, measure
		// velocities from consecutive reads, judge closing-vs-fleeing — touch nothing.
		type mtrack struct {
			pos data.Position
			at  time.Time
		}
		tracks := map[data.UnitID]mtrack{}
		deadline := time.Now().Add(time.Duration(*missileProbe) * time.Second)
		for time.Now().Before(deadline) {
			d := gr.GetData()
			me := d.PlayerUnit.Position
			now := time.Now()
			for _, ms := range gr.Missiles() {
				if tr, ok := tracks[ms.UnitID]; ok {
					dt := now.Sub(tr.at).Seconds()
					if dt > 0.02 && (ms.Position != tr.pos) {
						vx := float64(ms.Position.X-tr.pos.X) / dt
						vy := float64(ms.Position.Y-tr.pos.Y) / dt
						rx, ry := float64(me.X-ms.Position.X), float64(me.Y-ms.Position.Y)
						closing := rx*vx+ry*vy > 0
						logger.Info("missile", "id", int(ms.UnitID), "txt", ms.TxtFileNo,
							"pos", fmt.Sprintf("(%d,%d)", ms.Position.X, ms.Position.Y),
							"v", fmt.Sprintf("(%.0f,%.0f)", vx, vy),
							"dist", chebyshev(me, ms.Position), "closing", closing)
					}
				}
				tracks[ms.UnitID] = mtrack{pos: ms.Position, at: now}
			}
			time.Sleep(70 * time.Millisecond)
		}
		close(stop)
		return
	}

	if *wpCalTest {
		// WAYPOINT CALIBRATION (P-10 phase 1): find the WP, walk on, click it
		// open, photograph the panel, and MAP THE BLUE. The 853x480-era layout
		// constants do not survive this client; the blueness metric
		// (blue - (red+green)/2, farmbot's compass oracle) is resolution-
		// independent and re-derives the compass column and row bands here.
		led := verbs.NewLedger(256)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
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
		if !have || best > 200 {
			logger.Error("wpcal: no waypoint within 200", "nearest", best)
			close(stop)
			return
		}
		logger.Info("wpcal: waypoint found", "pos", fmt.Sprintf("(%d,%d)", wp.Position.X, wp.Position.Y), "dist", best)
		for i := 0; i < 60; i++ {
			s := p.Capture()
			if !s.Valid || !m.Engage.Engaged() {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if chebyshev(s.Me.Pos, wp.Position) <= 2 {
				break
			}
			verbs.Stride{To: wp.Position, Hold: 700 * time.Millisecond, MinGain: 1}.Do(m, gr, p, led, "wpcal")
		}
		// A WAYPOINT IS A FLAT GROUND PAD, not a tall portal — its clickable
		// hover sits AT the base, so the sweep centers on the projection
		// (dy -34..+34), not 80px above it. Hover-confirm against wp.ID, then
		// click; the blueness read below is the panel-open proof.
		m.MoveStop()
		m.ModifierAmnesty()
		func() {
			for tryOpen := 0; tryOpen < 6; tryOpen++ {
				dd := gr.GetData()
				meNow := dd.PlayerUnit.Position
				bx := int(float32((wp.Position.X-meNow.X)-(wp.Position.Y-meNow.Y))*19.8) + gr.GameAreaSizeX/2
				by := int(float32((wp.Position.X-meNow.X)+(wp.Position.Y-meNow.Y))*9.9) + gr.GameAreaSizeY/2
				for dy := -34; dy <= 34; dy += 6 {
					for _, dx := range []int{0, -10, 10, -20, 20, -32, 32} {
						cx, cy := bx+dx, by+dy
						if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
							continue
						}
						hid.AimPhysical(cx, cy)
						time.Sleep(45 * time.Millisecond)
						hd := gr.GetData().HoverData
						if !hd.IsHovered || hd.UnitID != wp.ID {
							continue
						}
						hid.AimPhysical(cx, cy)
						time.Sleep(60 * time.Millisecond)
						hd = gr.GetData().HoverData
						if hd.IsHovered && hd.UnitID == wp.ID {
							logger.Info("wpcal: WP hover confirmed", "screen", fmt.Sprintf("(%d,%d)", cx, cy))
							gi.OverrideGetKeyState(0x01)
							gi.OverrideGetAsyncKeyState(0x01)
							hid.LeftClickNoMove(cx, cy)
							time.Sleep(900 * time.Millisecond)
							return
						}
					}
				}
				logger.Info("wpcal: no WP hover this pass, re-approaching", "attempt", tryOpen+1)
				verbs.Stride{To: wp.Position, Hold: 500 * time.Millisecond, MinGain: 1}.Do(m, gr, p, led, "wpcal")
			}
		}()
		time.Sleep(400 * time.Millisecond)
		img := gr.Screenshot()
		if f, err := os.Create("logs/wp_panel.png"); err == nil {
			_ = png.Encode(f, img)
			f.Close()
			logger.Info("wpcal: panel photographed", "path", "logs/wp_panel.png")
		}
		// Blueness map: every 6px cell in the left 2/3, keep the strong blues.
		type blueCell struct {
			x, y int
			bl   float64
		}
		var cells []blueCell
		bnd := img.Bounds()
		for y := 60; y < bnd.Dy()-140; y += 6 {
			for x := 40; x < bnd.Dx()*2/3; x += 6 {
				var sum, n float64
				for yy := y; yy < y+6 && yy < bnd.Dy(); yy += 2 {
					for xx := x; xx < x+6 && xx < bnd.Dx(); xx += 2 {
						rr, gg, bb, _ := img.At(xx, yy).RGBA()
						sum += float64(int(bb>>8) - (int(rr>>8)+int(gg>>8))/2)
						n++
					}
				}
				if n > 0 {
					if v := sum / n; v > 20 {
						cells = append(cells, blueCell{x, y, v})
					}
				}
			}
		}
		sort.Slice(cells, func(i, j int) bool { return cells[i].bl > cells[j].bl })
		if len(cells) > 60 {
			cells = cells[:60]
		}
		for _, c := range cells {
			logger.Info("wpcal: blue", "x", c.x, "y", c.y, "bl", int(c.bl))
		}
		logger.Info("wpcal: done", "blueCells", len(cells), "panelOpen", len(cells) > 0)
		if len(cells) > 0 {
			m.KeyLane().Press(0x1B) // the blue proves a panel — ESC is lawful (WARNING 4)
		}
		close(stop)
		return
	}

	if *relogTest {
		// THE RELOG DRILL (docs/AZBOT_V2.md step 8): one relog end to end on the
		// exact Session FSM and driver the executive runs — every phase judged by
		// sight or memory validity with a bounded wait, every phase photographed
		// (logs/relog_<phase>.png). No ESC of its own: the session sends its one
		// OpenPause ESC only on a clear world screen. -exitxy / -playxy override
		// the Save and Exit / Play buttons (screenshot px, 1920x1050 physical).
		shot := func(path string) {
			if f, err := os.Create(path); err == nil {
				_ = png.Encode(f, gr.Screenshot())
				f.Close()
				logger.Info("relogtest: screenshot", "path", path)
			}
		}
		// A manual drill runs from a terminal that holds the focus; the session
		// never steals it, so hand it to the game once, verified.
		for i := 0; i < 12; i++ {
			if win.GetForegroundWindow() == hwnd {
				break
			}
			game.ForceForegroundHWND(hwnd)
			time.Sleep(300 * time.Millisecond)
		}
		logger.Info("relogtest: foreground check", "isForeground", win.GetForegroundWindow() == hwnd)
		dsh := newShadow(logger, gr)
		dsd := newSessionDriver(logger, m, gr, dsh, activity.NewRelog())
		if *exitXY != "" {
			fmt.Sscanf(*exitXY, "%d,%d", &dsd.ses.SaveExit.X, &dsd.ses.SaveExit.Y)
		}
		if *playXY != "" {
			fmt.Sscanf(*playXY, "%d,%d", &dsd.ses.Play.X, &dsd.ses.Play.Y)
		}
		dsd.onChange = func(ses string) {
			shot("logs/relog_" + strings.ReplaceAll(ses, "/", "_") + ".png")
		}
		var ended *exec.RelogEnd
		dsd.onEnd = func(e exec.RelogEnd) { ended = &e }
		requested, invalid := false, false
		var tick uint64
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) && (!requested || dsd.ses.Relogging()) {
			tick++
			s := p.Capture()
			dsh.observe(tick, s, "relogtest")
			invalid = invalid || !s.Valid
			if s.Valid && invalid && dsd.ses.Relogging() {
				invalid = false
				if err := gr.FetchMapData(); err != nil {
					logger.Warn("relogtest: map data fetch failed", "err", err)
				}
			}
			if !requested && dsd.ses.State() == exec.InGame {
				requested = dsd.request("drill (-relogtest)")
			}
			dsd.step(tick, s)
			time.Sleep(40 * time.Millisecond)
		}
		switch {
		case ended == nil:
			shot("logs/relog_stuck.png")
			logger.Error("relogtest: the relog never ended inside 2 minutes", "ses", dsd.ses.String())
		case !ended.OK():
			logger.Error("relogtest: relog FAILED", "why", ended.Why.String(), "phase", ended.Phase.String(),
				"churned", ended.Churned, "detail", ended.Detail)
			if !ended.Churned {
				// Live, the janitor clicks the now-foreign pause menu away; the
				// drill has no janitor, so the same click by sight.
				if n := activity.EnsureWorld(gr, m); n > 0 {
					logger.Info("relogtest: pause menu clicked away by sight (Return to Game)", "layers", n)
				}
			}
		default:
			time.Sleep(2 * time.Second)
			d := gr.GetData()
			logger.Info("relogtest: BACK IN GAME", "took", ended.Took.Round(100*time.Millisecond), "detail", ended.Detail,
				"pos", fmt.Sprintf("(%d,%d)", d.PlayerUnit.Position.X, d.PlayerUnit.Position.Y),
				"area", int(d.PlayerUnit.Area), "seed", gr.MapSeed())
			logger.Info("relogtest: corpse", "found", d.Corpse.Found,
				"pos", fmt.Sprintf("(%d,%d)", d.Corpse.Position.X, d.Corpse.Position.Y))
		}
		close(stop)
		return
	}

	// ---- M4 fight test: calibrate capability, cross to Blood Moor, strike with evidence ----
	if *gambleRefresh {
		// The owner parked the REAL cursor on the mod's refresh button — read it,
		// convert to client pixels, record, click it, and verify the stock changes.
		var pt struct{ X, Y int32 }
		windows.NewLazySystemDLL("user32.dll").NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pt)))
		cx := int(pt.X) - int(float64(gr.WindowLeftX)*(*dpiScale))
		cy := int(pt.Y) - int(float64(gr.WindowTopY)*(*dpiScale))
		logger.Info("gamblerefresh: parked cursor", "screen", fmt.Sprintf("(%d,%d)", pt.X, pt.Y),
			"client", fmt.Sprintf("(%d,%d)", cx, cy))
		stockSig := func() string {
			sig := ""
			for _, it := range gr.GetData().Inventory.ByLocation(item.LocationVendor) {
				sig += fmt.Sprintf("%d@%d,%d;", it.ID, it.Position.X, it.Position.Y)
			}
			return sig
		}
		before := stockSig()
		m.UIClick(cx, cy)
		time.Sleep(700 * time.Millisecond)
		after := stockSig()
		if before == after {
			// Panel click deaf on this button — the mod UI may honor only REAL input.
			logger.Info("gamblerefresh: uiClick deaf — trying OS-level real click (foregrounding)")
			win.SetForegroundWindow(hwnd)
			time.Sleep(400 * time.Millisecond)
			game.SendClickRealScreen(int(pt.X), int(pt.Y))
			time.Sleep(900 * time.Millisecond)
			after = stockSig()
		}
		logger.Info("gamblerefresh: result", "changed", before != after, "itemsBefore", strings.Count(before, ";"), "itemsAfter", strings.Count(after, ";"))
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationVendor) {
			logger.Info("gamblerefresh: stock now", "id", it.ID, "name", string(it.Name), "gx", it.Position.X, "gy", it.Position.Y)
		}
		close(stop)
		return
	}

	if *gambleBuy != "" {
		var bx, by int
		if _, err := fmt.Sscanf(*gambleBuy, "%d,%d", &bx, &by); err != nil {
			logger.Error("bad -gamblebuy")
			close(stop)
			return
		}
		goldOf := func() int {
			if v, ok := gr.GetData().PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
				return v.Value
			}
			return 0
		}
		invSig := func() map[string]bool {
			sig := map[string]bool{}
			for _, it := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				sig[fmt.Sprintf("%d@%d,%d", it.ID, it.Position.X, it.Position.Y)] = true
			}
			return sig
		}
		g0, inv0 := goldOf(), invSig()
		m.UIClick(bx, by)
		time.Sleep(700 * time.Millisecond)
		if goldOf() == g0 {
			logger.Info("gamblebuy: uiClick deaf — real click fallback")
			win.SetForegroundWindow(hwnd)
			time.Sleep(400 * time.Millisecond)
			game.SendClickRealScreen(int(float64(gr.WindowLeftX)*(*dpiScale))+bx, int(float64(gr.WindowTopY)*(*dpiScale))+by)
			time.Sleep(900 * time.Millisecond)
		}
		g1 := goldOf()
		logger.Info("gamblebuy: transaction", "goldDelta", g1-g0, "gold", g1)
		// THE ORACLE MOMENT: read the rolled result the same frame it exists.
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
			if !inv0[fmt.Sprintf("%d@%d,%d", it.ID, it.Position.X, it.Position.Y)] {
				logger.Info("gamblebuy: RESULT", "id", it.ID, "name", string(it.Name),
					"quality", int(it.Quality), "identified", it.Identified, "idName", it.IdentifiedName,
					"prefixes", fmt.Sprintf("%v", it.Affixes.Magic.Prefixes),
					"suffixes", fmt.Sprintf("%v", it.Affixes.Magic.Suffixes), "stats", len(it.Stats))
				for _, st := range it.Stats {
					logger.Info("gamblebuy: result stat", "id", int(st.ID), "value", st.Value)
				}
			}
		}
		close(stop)
		return
	}

	if *gambleDump {
		d := gr.GetData()
		for _, it := range d.Inventory.ByLocation(item.LocationVendor) {
			pre, suf := it.Affixes.Magic.Prefixes, it.Affixes.Magic.Suffixes
			nStats := len(it.Stats)
			logger.Info("gambledump", "id", it.ID, "name", string(it.Name),
				"pos", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y),
				"quality", int(it.Quality), "identified", it.Identified,
				"idName", it.IdentifiedName,
				"prefixes", fmt.Sprintf("%v", pre), "suffixes", fmt.Sprintf("%v", suf),
				"stats", nStats, "store", it.InTradeOrStoreScreen)
		}
		g := 0
		if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
			g = v.Value
		}
		logger.Info("gambledump: gold", "gold", g)
		close(stop)
		return
	}

	if *invDump {
		d := gr.GetData()
		for _, loc := range []item.LocationType{item.LocationInventory, item.LocationBelt, item.LocationEquipped, item.LocationCursor} {
			for _, it := range d.Inventory.ByLocation(loc) {
				logger.Info("invdump", "loc", string(loc), "id", it.ID, "name", string(it.Name),
					"pos", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y), "quality", int(it.Quality), "identified", it.Identified)
			}
		}
		for _, bi := range d.Inventory.Belt.Items {
			logger.Info("invdump: belt", "id", bi.ID, "name", string(bi.Name), "pos", fmt.Sprintf("(%d,%d)", bi.Position.X, bi.Position.Y))
		}
		g := 0
		if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
			g = v.Value
		}
		logger.Info("invdump: gold", "gold", g, "beltName", string(d.Inventory.Belt.Name), "beltItems", len(d.Inventory.Belt.Items))
		close(stop)
		return
	}

	if *selfTest {
		led := verbs.NewLedger(64)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "holder", o.Holder, "result", o.Result.String(), "ev", o.Evidence)
		}
		g, _, _ := gr.BuildLiveGridRooms()
		ctx := &activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: g, Mem: mem,
			InvKey: hid.GetASCIICode(*invKeyF), SwapKey: hid.GetASCIICode(*swapKey)}
		res := activity.SelfTest(ctx, p.Capture)
		fails := 0
		fmt.Println("==== azbot preflight self-test ====")
		for _, r := range res {
			fmt.Println(r.String())
			logger.Info("selftest", "result", r.String())
			if !r.Pass {
				fails++
			}
		}
		fmt.Printf("==== %d/%d passed ====\n", len(res)-fails, len(res))
		close(stop)
		return
	}

	if *akaraTest {
		led := verbs.NewLedger(64)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
		s := p.Capture()
		if !s.Valid || !s.Me.InTown {
			logger.Error("akaratest: not in town")
			close(stop)
			return
		}
		// Gold truth hunt: the stat we read says 0 while the UI shows 900 — dump every
		// stat and let the value name itself.
		dstats := gr.GetData()
		for _, st := range dstats.PlayerUnit.Stats {
			if st.Value != 0 {
				logger.Info("akaratest: stat", "id", int(st.ID), "layer", st.Layer, "value", st.Value)
			}
		}
		for _, st := range dstats.PlayerUnit.BaseStats {
			if st.Value != 0 {
				logger.Info("akaratest: basestat", "id", int(st.ID), "layer", st.Layer, "value", st.Value)
			}
		}

		// ---- BELT SELF-MODEL: the bot KNOWS its belt, it doesn't assume one. ----
		d0 := gr.GetData()
		belt := d0.Inventory.Belt
		rows := belt.Rows()
		haveHP, haveMana := 0, 0
		for _, bi := range belt.Items {
			n := string(bi.Name)
			logger.Info("akaratest: belt slot", "item", n, "col", bi.Position.X, "row", bi.Position.Y)
			if strings.Contains(n, "Healing") {
				haveHP++
			} else if strings.Contains(n, "Mana") {
				haveMana++
			}
		}
		// Doctrine: 1 row of mana, the rest HP — scaled to the belt she actually wears.
		wantMana := 4
		if rows == 1 {
			wantMana = 1
		}
		wantHP := rows*4 - wantMana
		buyHP, buyMana := wantHP-haveHP, wantMana-haveMana
		if buyHP < 0 {
			buyHP = 0
		}
		if buyMana < 0 {
			buyMana = 0
		}
		gold := 0
		if v, ok := d0.PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
			gold = v.Value
		}
		logger.Info("akaratest: belt self-model", "belt", string(belt.Name), "rows", rows,
			"slots", rows*4, "haveHP", haveHP, "haveMana", haveMana)
		logger.Info("akaratest: restock plan", "wantHP", wantHP, "wantMana", wantMana,
			"buyHP", buyHP, "buyMana", buyMana, "gold", gold)

		// ---- FIND AKARA: scan, else walk the town ring until she loads. ----
		var ak data.Monster
		found := false
		scan := func() {
			for _, mo := range gr.GetData().Monsters {
				if mo.Name == npc.Akara {
					ak, found = mo, true
				}
			}
		}
		scan()
		if !found {
			ring := []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}
			for _, wp := range ring {
				for i := 0; i < 12 && !found; i++ {
					scan()
					if found || chebyshev(gr.GetData().PlayerUnit.Position, wp) <= 5 {
						break
					}
					verbs.Stride{To: wp, MinGain: 1}.Do(m, gr, p, led, "akaratest/search")
				}
				if found {
					break
				}
			}
		}
		if !found {
			logger.Error("akaratest: Akara never loaded — townsfolk seen:")
			for _, mo := range gr.GetData().Monsters {
				logger.Info("akaratest: townsfolk(final)", "npc", int(mo.Name), "pos", fmt.Sprintf("(%d,%d)", mo.Position.X, mo.Position.Y))
			}
			close(stop)
			return
		}
		logger.Info("akaratest: Akara found", "unit", int(ak.UnitID), "pos", fmt.Sprintf("(%d,%d)", ak.Position.X, ak.Position.Y))

		// ---- APPROACH (band 4..7) + BARE CLICK + HOME/DOWN/ENTER — the Charsi laws. ----
		approach := func() {
			for i := 0; i < 30; i++ {
				d := gr.GetData()
				scan()
				dist := chebyshev(d.PlayerUnit.Position, ak.Position)
				if dist >= 4 && dist <= 7 {
					break
				}
				if dist < 4 {
					me := d.PlayerUnit.Position
					back := data.Position{X: me.X + (me.X-ak.Position.X)*3, Y: me.Y + (me.Y-ak.Position.Y)*3}
					verbs.Stride{To: back, Hold: 400 * time.Millisecond}.Do(m, gr, p, led, "akaratest/backoff")
					continue
				}
				hold := 1600 * time.Millisecond
				if dist < 14 {
					hold = 500 * time.Millisecond
				}
				verbs.Stride{To: ak.Position, Hold: hold, MinGain: 1}.Do(m, gr, p, led, "akaratest/approach")
			}
			m.MoveStop()
			time.Sleep(600 * time.Millisecond)
		}
		approach()

		menuByte := func() bool { ub := gr.UIBytes(); return len(ub) > 0xF4 && ub[0xF4] == 1 }
		menuKey := func(vk byte) {
			_ = gi.OverrideGetKeyState(vk)
			_ = gi.OverrideGetAsyncKeyState(vk)
			hid.RawKeyDown(vk)
			time.Sleep(220 * time.Millisecond)
			hid.RawKeyUp(vk)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
			time.Sleep(200 * time.Millisecond)
		}
		shopOpen := false
		for attempt := 0; attempt < 6 && !shopOpen; attempt++ {
			if attempt > 0 {
				d0 := gr.GetData()
				verbs.Stride{To: data.Position{X: d0.PlayerUnit.Position.X + 8, Y: d0.PlayerUnit.Position.Y + 8}, Hold: 700 * time.Millisecond}.Do(m, gr, p, led, "akaratest/reset")
				approach()
			}
			d := gr.GetData()
			me := d.PlayerUnit.Position
			scan()
			bx := int(float32((ak.Position.X-me.X)-(ak.Position.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
			by := int(float32((ak.Position.X-me.X)+(ak.Position.Y-me.Y))*9.9) + gr.GameAreaSizeY/2
			px, py, confirmed := bx, by, false
		akSweep:
			for dy := -8; dy >= -64; dy -= 8 {
				for _, dx := range []int{0, -8, 8, -16, 16, -24, 24} {
					cx, cy := bx+dx, by+dy
					if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
						continue
					}
					m.AimPhysical(cx, cy)
					time.Sleep(40 * time.Millisecond)
					hd := gr.GetData().HoverData
					if !hd.IsHovered || hd.UnitID != ak.UnitID {
						continue
					}
					m.AimPhysical(cx, cy)
					time.Sleep(60 * time.Millisecond)
					hd = gr.GetData().HoverData
					if hd.IsHovered && hd.UnitID == ak.UnitID {
						px, py, confirmed = cx, cy, true
						break akSweep
					}
				}
			}
			if menuByte() {
				logger.Info("akaratest: menu already open — proceeding", "n", attempt)
			} else {
				if !confirmed {
					logger.Info("akaratest: no hover confirmation — re-approaching", "n", attempt)
					continue
				}
				hid.Click(game.LeftButton, px, py)
				logger.Info("akaratest: body click (bare)", "n", attempt)
				ok := false
				for i := 0; i < 30; i++ {
					if menuByte() {
						ok = true
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				if !ok {
					logger.Info("akaratest: menu never opened — retrying")
					continue
				}
			}
			m.MoveStop()
			menuKey(0x24) // HOME
			menuKey(0x28) // DOWN
			hid.PressKey(hid.GetASCIICode("enter"))
			time.Sleep(1200 * time.Millisecond)
			// Shop oracle: vendor stock becomes READABLE when the trade panel is up.
			if len(gr.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
				shopOpen = true
			} else {
				hid.PressKey(hid.GetASCIICode("esc"))
				time.Sleep(300 * time.Millisecond)
			}
		}
		if !shopOpen {
			logger.Error("akaratest: shop never opened")
			close(stop)
			return
		}

		// ---- STOCK READ: the vendor's inventory from memory — no pixel guessing. ----
		type slotRef struct{ x, y int }
		var hpSlots, manaSlots []slotRef
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationVendor) {
			n := string(it.Name)
			logger.Info("akaratest: stock", "item", n, "gx", it.Position.X, "gy", it.Position.Y)
			if strings.Contains(n, "HealingPotion") || strings.Contains(n, "MalahsPotion") {
				hpSlots = append(hpSlots, slotRef{it.Position.X, it.Position.Y})
			} else if strings.Contains(n, "ManaPotion") {
				manaSlots = append(manaSlots, slotRef{it.Position.X, it.Position.Y})
			}
		}
		logger.Info("akaratest: potion slots", "hp", len(hpSlots), "mana", len(manaSlots))
		if f, err := os.Create("logs/akara_shop.png"); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("akaratest: shop screenshot saved", "path", "logs/akara_shop.png")
		}

		// ---- SHOPPICK: the click-based slot oracle. Pick → read LocationCursor →
		// put back. The cursor item's memory identity cannot lie, and no gold moves
		// until a drop into OUR inventory. ----
		if *shopPick != "" {
			var sx, sy int
			if _, err := fmt.Sscanf(*shopPick, "%d,%d", &sx, &sy); err != nil {
				logger.Error("akaratest: bad -shoppick")
				close(stop)
				return
			}
			uiClick := func(cx, cy int) {
				m.MoveStop()
				ppx := int(float64(gr.WindowLeftX)*(*dpiScale)) + cx
				ppy := int(float64(gr.WindowTopY)*(*dpiScale)) + cy
				_ = gi.OverridePhysicalCursorPos(ppx, ppy)
				hid.MouseMoveClient(cx, cy)
				time.Sleep(150 * time.Millisecond)
				_ = gi.OverrideGetKeyState(0x01)
				_ = gi.OverrideGetAsyncKeyState(0x01)
				hid.LeftClickNoMoveClient(cx, cy)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
				time.Sleep(120 * time.Millisecond)
				_ = gi.RestorePhysicalCursorPos()
				_ = gi.RestoreGetCursorInfo()
				_ = gi.RestoreGetCursorPosAddr()
			}
			cursorItems := func() []string {
				var out []string
				for _, it := range gr.GetData().Inventory.ByLocation(item.LocationCursor) {
					out = append(out, string(it.Name))
				}
				return out
			}
			goldOf := func() int {
				if v, ok := gr.GetData().PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
					return v.Value
				}
				return 0
			}
			invCount := func() int { return len(gr.GetData().Inventory.ByLocation(item.LocationInventory)) }
			g0, n0 := goldOf(), invCount()
			logger.Info("akaratest: shoppick", "px", sx, "py", sy, "gold", g0, "invItems", n0, "cursorBefore", fmt.Sprintf("%q", cursorItems()))
			uiClick(sx, sy)
			time.Sleep(400 * time.Millisecond)
			held := cursorItems()
			logger.Info("akaratest: shoppick result", "cursorAfter", fmt.Sprintf("%q", held),
				"goldDelta", goldOf()-g0, "invDelta", invCount()-n0)
			for _, it := range gr.GetData().Inventory.ByLocation(item.LocationInventory) {
				logger.Info("akaratest: inv now", "id", it.ID, "name", string(it.Name), "pos", fmt.Sprintf("(%d,%d)", it.Position.X, it.Position.Y))
			}
			if len(held) > 0 {
				uiClick(sx, sy) // put it back — no transaction
				time.Sleep(300 * time.Millisecond)
				logger.Info("akaratest: shoppick returned", "cursorNow", fmt.Sprintf("%v", cursorItems()))
			}
			hid.PressKey(hid.GetASCIICode("esc"))
			time.Sleep(300 * time.Millisecond)
			close(stop)
			return
		}

		// ---- SHOPMAP: hover-confirm the PANEL, the same honest law as the world.
		// The mod scrambles the name table and my derived grid formula pointed at a
		// rune; the game itself knows what is under the cursor — ask IT per pixel. ----
		if *shopMap != "" {
			var x0, y0, x1, y1, step int
			if _, err := fmt.Sscanf(*shopMap, "%d,%d,%d,%d,%d", &x0, &y0, &x1, &y1, &step); err != nil {
				logger.Error("akaratest: bad -shopmap", "err", err)
				close(stop)
				return
			}
			// REAL cursor, the HoverStrike way — the patched-export dance is for click
			// registration; hover tracking follows the actual pointer.
			aimPanel := func(sx, sy int) {
				m.AimPhysical(sx, sy)
			}
			// Panel hover appears to gate on window FOCUS (world hover does not) —
			// foreground the game for the sweep.
			win.SetForegroundWindow(hwnd)
			time.Sleep(400 * time.Millisecond)
			seen := map[string]bool{}
			for sy := y0; sy <= y1; sy += step {
				for sx := x0; sx <= x1; sx += step {
					aimPanel(sx, sy)
					time.Sleep(60 * time.Millisecond)
					for _, it := range gr.GetData().Inventory.ByLocation(item.LocationVendor) {
						if it.IsHovered {
							key := fmt.Sprintf("%s@%d,%d", string(it.Name), it.Position.X, it.Position.Y)
							if !seen[key] {
								seen[key] = true
								logger.Info("akaratest: shopmap hit", "px", sx, "py", sy,
									"item", string(it.Name), "gx", it.Position.X, "gy", it.Position.Y)
							}
						}
					}
				}
			}
			_ = gi.RestorePhysicalCursorPos()
			_ = gi.RestoreGetCursorInfo()
			_ = gi.RestoreGetCursorPosAddr()
			logger.Info("akaratest: shopmap done", "distinct", len(seen))
			hid.PressKey(hid.GetASCIICode("esc"))
			time.Sleep(300 * time.Millisecond)
			close(stop)
			return
		}

		// ---- BUY: one uiClick on a vendor cell IS an instant purchase (discovered by
		// the accidental 2x tome buy: gold -300 each, auto-placed). Potions auto-place
		// into the BELT. Cells measured optically from akara_shop.png and verified by
		// the 30g Herb (id 602 = the mod's HP potion) landing in belt (0,0). ----
		if *buyPots {
			const hpID = 602             // "Herb" — the mod's HP potion, proven by purchase
			hpCell := [2]int{612, 478}   // red bottle, Misc tab
			manaCell := [2]int{612, 525} // blue bottle, Misc tab
			uiClick := func(cx, cy int) {
				m.MoveStop()
				ppx := int(float64(gr.WindowLeftX)*(*dpiScale)) + cx
				ppy := int(float64(gr.WindowTopY)*(*dpiScale)) + cy
				_ = gi.OverridePhysicalCursorPos(ppx, ppy)
				hid.MouseMoveClient(cx, cy)
				time.Sleep(150 * time.Millisecond)
				_ = gi.OverrideGetKeyState(0x01)
				_ = gi.OverrideGetAsyncKeyState(0x01)
				hid.LeftClickNoMoveClient(cx, cy)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
				time.Sleep(120 * time.Millisecond)
				_ = gi.RestorePhysicalCursorPos()
				_ = gi.RestoreGetCursorInfo()
				_ = gi.RestoreGetCursorPosAddr()
			}
			goldOf := func() int {
				if v, ok := gr.GetData().PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
					return v.Value
				}
				return 0
			}
			beltIDs := func() []int {
				var out []int
				for _, bi := range gr.GetData().Inventory.Belt.Items {
					out = append(out, bi.ID)
				}
				return out
			}
			countID := func(ids []int, want int) int {
				n := 0
				for _, id := range ids {
					if id == want {
						n++
					}
				}
				return n
			}
			buyCell := func(cell [2]int, label string) (newID int, ok bool) {
				g0 := goldOf()
				b0 := beltIDs()
				uiClick(cell[0], cell[1])
				time.Sleep(500 * time.Millisecond)
				g1 := goldOf()
				b1 := beltIDs()
				// The new belt id, if any (first id in b1 exceeding b0's count of it).
				for _, id := range b1 {
					if countID(b1, id) > countID(b0, id) {
						newID = id
						break
					}
				}
				logger.Info("akaratest: buy", "what", label, "cell", fmt.Sprintf("(%d,%d)", cell[0], cell[1]),
					"goldDelta", g1-g0, "beltBefore", len(b0), "beltAfter", len(b1), "newID", newID)
				return newID, g1 < g0
			}

			ids := beltIDs()
			hpHave := countID(ids, hpID)
			logger.Info("akaratest: belt by id", "total", len(ids), "hp(602)", hpHave)
			// Learn the mana potion's true id with ONE blue purchase.
			manaID, ok := buyCell(manaCell, "mana-probe")
			if !ok {
				logger.Error("akaratest: mana probe did not purchase — stopping")
			} else {
				if manaID != 0 && manaID != hpID {
					logger.Info("akaratest: MANA POTION ID LEARNED", "id", manaID)
					mem.PutJSON("mod_item_ids", memory.ScopeForever,
						memory.Provenance{Source: "measured", Evidence: "vendor purchase deltas 2026-07-19"},
						map[string]int{"hp_potion": hpID, "mana_potion": manaID, "tp_tome": 533, "id_tome": 534})
				}
				// Fill the rest of the doctrine: 1 row mana (have 1 now), rest HP.
				for i := hpHave; i < wantHP; i++ {
					if _, ok := buyCell(hpCell, "hp"); !ok {
						logger.Warn("akaratest: hp buy failed — stopping")
						break
					}
				}
			}
			final := beltIDs()
			logger.Info("akaratest: restock RESULT", "beltTotal", len(final),
				"hp", countID(final, hpID), "mana", countID(final, manaID), "gold", goldOf())
		}
		hid.PressKey(hid.GetASCIICode("esc"))
		time.Sleep(300 * time.Millisecond)
		logger.Info("akaratest: done — esc out")
		close(stop)
		return
	}

	if *charsiTest {
		led := verbs.NewLedger(64)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
		s := p.Capture()
		if !s.Valid || !s.Me.InTown {
			logger.Error("charsitest: not in town — this drill starts in the Encampment", "valid", s.Valid)
			close(stop)
			return
		}
		// Scout the townsfolk (the unit list calls them monsters) and find Charsi.
		var ch data.Monster
		found := false
		for _, mo := range gr.GetData().Monsters {
			logger.Info("charsitest: townsfolk", "npc", int(mo.Name), "unit", int(mo.UnitID),
				"pos", fmt.Sprintf("(%d,%d)", mo.Position.X, mo.Position.Y))
			if mo.Name == npc.Charsi {
				ch, found = mo, true
			}
		}
		if !found {
			// Out of load range: walk the proven town road toward the west-gate forge,
			// scanning every leg until she loads in.
			logger.Info("charsitest: Charsi not loaded — walking the road toward the forge")
			road := []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
			for _, wp := range road {
				for i := 0; i < 12 && !found; i++ {
					d := gr.GetData()
					for _, mo := range d.Monsters {
						if mo.Name == npc.Charsi {
							ch, found = mo, true
						}
					}
					if found || chebyshev(d.PlayerUnit.Position, wp) <= 5 {
						break
					}
					verbs.Stride{To: wp, MinGain: 1}.Do(m, gr, p, led, "charsitest/search")
				}
				if found {
					break
				}
			}
			if !found {
				logger.Error("charsitest: walked the whole road and Charsi never loaded — dump townsfolk and rethink the anchor")
				for _, mo := range gr.GetData().Monsters {
					logger.Info("charsitest: townsfolk(final)", "npc", int(mo.Name), "pos", fmt.Sprintf("(%d,%d)", mo.Position.X, mo.Position.Y))
				}
				close(stop)
				return
			}
			logger.Info("charsitest: Charsi loaded in", "unit", int(ch.UnitID), "pos", fmt.Sprintf("(%d,%d)", ch.Position.X, ch.Position.Y))
		}
		// Approach with honest strides; she wanders, so re-read her every leg. Arrival
		// is a BAND (4..7): a full-hold stride at close range overshoots and oscillates
		// (the rep-2 thrash: 20 strides bouncing across her), so near her the hold
		// shrinks, and standing ON her is also wrong — sprites overlap and hover lies.
		approach := func() {
			for i := 0; i < 30; i++ {
				d := gr.GetData()
				for _, mo := range d.Monsters {
					if mo.Name == npc.Charsi {
						ch = mo
					}
				}
				dist := chebyshev(d.PlayerUnit.Position, ch.Position)
				if dist >= 4 && dist <= 7 {
					break
				}
				if dist < 4 { // too close — back off a step so her sprite is clickable
					me := d.PlayerUnit.Position
					back := data.Position{X: me.X + (me.X-ch.Position.X)*3, Y: me.Y + (me.Y-ch.Position.Y)*3}
					verbs.Stride{To: back, Hold: 400 * time.Millisecond}.Do(m, gr, p, led, "charsitest/backoff")
					continue
				}
				hold := 1600 * time.Millisecond
				if dist < 14 {
					hold = 500 * time.Millisecond // short legs near her: land in the band, don't fly past it
				}
				verbs.Stride{To: ch.Position, Hold: hold, MinGain: 1}.Do(m, gr, p, led, "charsitest/approach")
			}
			m.MoveStop()
			time.Sleep(600 * time.Millisecond) // let both of us settle out of walk animations
		}
		approach()

		// uiClick: farmbot's PROVEN panel-click recipe (panels read the cursor via the
		// patched exports at raw physical pixels; clicks register only while GetKeyState
		// polls LBUTTON down). Local here until the M7 services port gives it a home.
		uiClick := func(sx, sy int) {
			m.MoveStop()
			px := int(float64(gr.WindowLeftX)*(*dpiScale)) + sx
			py := int(float64(gr.WindowTopY)*(*dpiScale)) + sy
			_ = gi.OverridePhysicalCursorPos(px, py)
			hid.MouseMoveClient(sx, sy)
			time.Sleep(150 * time.Millisecond)
			_ = gi.OverrideGetKeyState(0x01)
			_ = gi.OverrideGetAsyncKeyState(0x01)
			hid.LeftClickNoMoveClient(sx, sy)
			_ = gi.RestoreGetKeyState()
			_ = gi.RestoreGetAsyncKeyState()
			time.Sleep(120 * time.Millisecond)
			_ = gi.RestorePhysicalCursorPos()
			_ = gi.RestoreGetCursorInfo()
			_ = gi.RestoreGetCursorPosAddr()
		}
		// leftChange: the shop-open oracle — count changed pixels in the UPPER-left
		// (the chat panel bottom-left must not count). >12000 = a big panel appeared.
		leftChange := func(before, after image.Image) int {
			b := after.Bounds()
			n := 0
			for y := 40; y < b.Dy()*55/100; y += 3 {
				for x := 20; x < b.Dx()*45/100; x += 3 {
					r1, g1, b1, _ := before.At(x, y).RGBA()
					r2, g2, b2, _ := after.At(x, y).RGBA()
					dr, dg, db := int(r1>>8)-int(r2>>8), int(g1>>8)-int(g2>>8), int(b1>>8)-int(b2>>8)
					if dr < 0 {
						dr = -dr
					}
					if dg < 0 {
						dg = -dg
					}
					if db < 0 {
						db = -db
					}
					if dr+dg+db > 60 {
						n++
					}
				}
			}
			return n
		}

		shopOpen := false
		for attempt := 0; attempt < 6 && !shopOpen; attempt++ {
			if attempt > 0 {
				// The Akara lesson: stale-vantage clicks keep missing — step off and re-approach fresh.
				d0 := gr.GetData()
				verbs.Stride{To: data.Position{X: d0.PlayerUnit.Position.X + 8, Y: d0.PlayerUnit.Position.Y + 8}, Hold: 700 * time.Millisecond}.Do(m, gr, p, led, "charsitest/reset")
				approach()
			}
			d := gr.GetData()
			me := d.PlayerUnit.Position
			for _, mo := range d.Monsters {
				if mo.Name == npc.Charsi {
					ch = mo
				}
			}
			bx := int(float32((ch.Position.X-me.X)-(ch.Position.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
			by := int(float32((ch.Position.X-me.X)+(ch.Position.Y-me.Y))*9.9) + gr.GameAreaSizeY/2
			// Hover-confirm her unit — NO confirmation, NO click (the HoverStrike law
			// applies to townsfolk too; rep-2's blind fallback clicks all missed).
			px, py, confirmed := bx, by, false
		npcSweep:
			for dy := -8; dy >= -64; dy -= 8 {
				for _, dx := range []int{0, -8, 8, -16, 16, -24, 24} {
					cx, cy := bx+dx, by+dy
					if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
						continue
					}
					m.AimPhysical(cx, cy)
					time.Sleep(40 * time.Millisecond)
					hd := gr.GetData().HoverData
					if !hd.IsHovered || hd.UnitID != ch.UnitID {
						continue
					}
					// Double-confirm (the EnterPortal lesson: one read can reflect the
					// previous probe's cursor).
					m.AimPhysical(cx, cy)
					time.Sleep(60 * time.Millisecond)
					hd = gr.GetData().HoverData
					if hd.IsHovered && hd.UnitID == ch.UnitID {
						px, py, confirmed = cx, cy, true
						break npcSweep
					}
				}
			}
			menuByte := func() bool { ub := gr.UIBytes(); return len(ub) > 0xF4 && ub[0xF4] == 1 }
			base := gr.Screenshot()
			if menuByte() {
				// A previous click's menu arrived after its poll window (the click starts
				// a WALK-to-talk — from the 4..7 band that walk outlives a short poll).
				// The menu is open right now: use it, don't fight it.
				logger.Info("charsitest: menu already open from a late click — proceeding", "n", attempt)
			} else {
				if !confirmed {
					logger.Info("charsitest: no hover confirmation on Charsi — re-approaching, not clicking blind", "n", attempt)
					continue
				}
				// BARE message click — koolo's proven NPC talk. The LBUTTON override that
				// objects need reads as a HELD button if the game polls mid-window, and a
				// held click on an NPC is ATTACK semantics (the shift-click the owner saw),
				// not talk. NPCs accept the bare click; objects are the ones that don't.
				hid.Click(game.LeftButton, px, py)
				logger.Info("charsitest: body click (bare)", "n", attempt, "hoverConfirmed", confirmed)
				menuOpen := false
				for i := 0; i < 30; i++ { // 3s: the walk-to-talk from 7 tiles takes ~1.5s alone
					if menuByte() {
						menuOpen = true
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				if !menuOpen {
					logger.Info("charsitest: menu byte never flipped — retrying with a fresh approach")
					continue
				}
			}
			m.MoveStop()
			// Menus poll GetKeyState: hold the override around each raw key. HOME first
			// (koolo's recipe) — it normalizes the highlight to the top row, so Down+Enter
			// lands TRADE regardless of where the highlight started.
			menuKey := func(vk byte) {
				_ = gi.OverrideGetKeyState(vk)
				_ = gi.OverrideGetAsyncKeyState(vk)
				hid.RawKeyDown(vk)
				time.Sleep(220 * time.Millisecond)
				hid.RawKeyUp(vk)
				_ = gi.RestoreGetKeyState()
				_ = gi.RestoreGetAsyncKeyState()
				time.Sleep(200 * time.Millisecond)
			}
			menuKey(0x24) // HOME
			menuKey(0x28) // DOWN
			hid.PressKey(hid.GetASCIICode("enter"))
			time.Sleep(1000 * time.Millisecond)
			chg := leftChange(base, gr.Screenshot())
			logger.Info("charsitest: after Down+Enter", "leftChange", chg)
			if chg > 12000 {
				shopOpen = true
				break
			}
			hid.PressKey(hid.GetASCIICode("esc"))
			time.Sleep(300 * time.Millisecond)
		}
		if !shopOpen {
			logger.Error("charsitest: shop never opened")
			close(stop)
			return
		}
		if f, err := os.Create("logs/charsi_shop.png"); err == nil {
			_ = png.Encode(f, gr.Screenshot())
			f.Close()
			logger.Info("charsitest: shop OPEN — calibration screenshot saved", "path", "logs/charsi_shop.png")
		}
		// Durability dump: the honest repair oracle (prior finding: durability reads
		// work on this repack; the bow degrades). Max-durability gear = nothing to prove.
		durDump := func(tag string) {
			for _, it := range gr.GetData().Inventory.ByLocation(item.LocationEquipped) {
				dur, hasDur := it.FindStat(stat.Durability, 0)
				mx, hasMax := it.FindStat(stat.MaxDurability, 0)
				if hasDur || hasMax {
					logger.Info("charsitest: durability "+tag, "item", string(it.Name),
						"dur", dur.Value, "max", mx.Value)
				}
			}
		}
		if *repairXY != "" {
			var rx, ry int
			if _, err := fmt.Sscanf(*repairXY, "%d,%d", &rx, &ry); err == nil {
				durDump("before")
				g0 := 0
				if v, ok := gr.GetData().PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
					g0 = v.Value
				}
				uiClick(rx, ry)
				time.Sleep(600 * time.Millisecond)
				g1 := g0
				if v, ok := gr.GetData().PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
					g1 = v.Value
				}
				logger.Info("charsitest: repair click", "xy", *repairXY,
					"goldBefore", g0, "goldAfter", g1, "delta", g0-g1)
				durDump("after")
				if f, err := os.Create("logs/charsi_repair.png"); err == nil {
					_ = png.Encode(f, gr.Screenshot())
					f.Close()
				}
			}
		}
		hid.PressKey(hid.GetASCIICode("esc"))
		time.Sleep(300 * time.Millisecond)
		logger.Info("charsitest: done — esc out")
		close(stop)
		return
	}

	if *portalTest {
		led := verbs.NewLedger(64)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
		deadline := time.Now().Add(60 * time.Second)
		attempts := 0
		for time.Now().Before(deadline) {
			s := p.Capture()
			if !s.Valid || !m.Engage.Engaged() {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			if len(s.Portals) == 0 {
				logger.Info("portaltest: no portal in the snapshot yet — waiting",
					"area", int(s.Me.Area), "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y))
				time.Sleep(700 * time.Millisecond)
				continue
			}
			best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
			for _, pt := range s.Portals[1:] {
				if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
					best, bd = pt, d
				}
			}
			logger.Info("portaltest: portal in snapshot",
				"unit", int(best.ID), "pos", fmt.Sprintf("(%d,%d)", best.Pos.X, best.Pos.Y),
				"dist", bd, "area", int(s.Me.Area))
			if bd > 20 {
				verbs.Stride{To: best.Pos, MinGain: 1}.Do(m, gr, p, led, "portaltest/approach")
				continue
			}
			attempts++
			o := verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(m, gr, p, led, "portaltest")
			if o.Result == verbs.ResDone {
				s2 := p.Capture()
				logger.Info("portaltest: THROUGH the portal",
					"attempts", attempts, "area", int(s2.Me.Area),
					"pos", fmt.Sprintf("(%d,%d)", s2.Me.Pos.X, s2.Me.Pos.Y), "town", s2.Me.InTown)
				close(stop)
				return
			}
			logger.Warn("portaltest: attempt failed", "n", attempts,
				"result", o.Result.String(), "ev", o.Evidence)
			time.Sleep(400 * time.Millisecond)
		}
		logger.Error("portaltest: gave up (60s)", "attempts", attempts)
		close(stop)
		return
	}

	if *fightTest {
		led := verbs.NewLedger(512)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
		cap := combat.Calibrate(logger, gr, hid, mem, []string{"f1", "f2", "f3", "f4"})
		if cap.Contact == nil {
			logger.Warn("fighttest: no proven melee binding — plain attack only")
		}
		// Walk the road out and cross (the corridor + gate strides proven by hand).
		road := []data.Position{{X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
		for _, wp := range road {
			for tries := 0; tries < 15; tries++ {
				s := p.Capture()
				if !s.Valid || !m.Engage.Engaged() {
					time.Sleep(300 * time.Millisecond)
					continue
				}
				if int(s.Me.Area) == 2 {
					break // crossed already
				}
				if chebyshev(s.Me.Pos, wp) <= 6 {
					break
				}
				verbs.Stride{To: wp}.Do(m, gr, p, led, "fighttest/road")
			}
			if s := p.Capture(); s.Valid && int(s.Me.Area) == 2 {
				break
			}
		}
		s := p.Capture()
		if !s.Valid || int(s.Me.Area) != 2 {
			logger.Error("fighttest: did not reach Blood Moor", "area", int(s.Me.Area))
			close(stop)
			return
		}
		logger.Info("fighttest: in Blood Moor — hunting", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y))
		grid, err := fz.build()
		if err != nil {
			logger.Error("fighttest: grid failed", "err", err)
			close(stop)
			return
		}
		strikes, kills := 0, 0
		sweep := []data.Position{{X: 5920, Y: 4980}, {X: 5890, Y: 4950}, {X: 5860, Y: 5000}, {X: 5920, Y: 5040}}
		sweepIdx := 0
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) && strikes < 10 {
			if !m.Engage.Engaged() {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			s = p.Capture()
			if !s.Valid {
				continue
			}
			if s.Me.HPPct > 0 && s.Me.HPPct < 35 {
				logger.Warn("fighttest: HP low — ending the test alive", "hp", s.Me.HPPct)
				break
			}
			// nearest live enemy within 40
			best, bd := percept.EnemyRef{}, 41
			for _, e := range s.Enemies {
				if d := chebyshev(s.Me.Pos, e.Pos); d < bd {
					best, bd = e, d
				}
			}
			if bd > 40 {
				if sweepIdx >= len(sweep) {
					logger.Info("fighttest: sweep exhausted — done hunting")
					break
				}
				wp := sweep[sweepIdx]
				if chebyshev(s.Me.Pos, wp) <= 8 {
					sweepIdx++
					continue
				}
				verbs.Stride{To: wp}.Do(m, gr, p, led, "fighttest/sweep")
				continue
			}
			if bd > 4 {
				j := journey.New(gr, grid, best.Pos, "fighttest/approach")
				st := j.Step(m, p, led)
				if st.State == journey.Stalled || st.State == journey.NoPath {
					logger.Info("fighttest: approach verdict", "state", st.State.String())
				}
				continue
			}
			var key byte
			if cap.Combat != nil {
				key = cap.Combat.Key
			} else if cap.Contact != nil {
				key = cap.Contact.Key
			}
			o := verbs.HoverStrike{Target: best.ID, TargetPos: best.Pos, SelectKey: key}.Do(m, gr, p, led, "fighttest")
			strikes++
			if o.Result == verbs.ResDone {
				kills++ // evidence of damage (mode transition), not necessarily a kill
			}
		}
		logger.Info("fighttest: summary", "strikes", strikes, "evidenced", kills)
		close(stop)
		return
	}

	// ---- M3 journey test: one goal, one authority, honest verdicts ----
	if *jTest != "" {
		var tx, ty int
		if n, _ := fmt.Sscanf(*jTest, "%d,%d", &tx, &ty); n != 2 {
			logger.Error("jtest: want 'x,y'")
			return
		}
		led := verbs.NewLedger(512)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "holder", o.Holder, "result", o.Result.String(), "ev", o.Evidence)
		}
		grid, err := fz.build()
		if err != nil {
			logger.Error("jtest: live grid failed", "err", err)
			return
		}
		j := journey.New(gr, grid, data.Position{X: tx, Y: ty}, "jtest")
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if !m.Engage.Engaged() {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			st := j.Step(m, p, led)
			if st.Note != "" {
				logger.Info("journey", "state", st.State.String(), "note", st.Note)
			}
			if st.State == journey.Arrived || st.State == journey.NoPath || st.State == journey.Stalled {
				logger.Info("jtest: verdict", "state", st.State.String(), "note", st.Note)
				break
			}
		}
		close(stop)
		return
	}

	// ---- M2 road test: walk the measured town road on Stride verbs, ledger everything ----
	if *roadTest {
		led := verbs.NewLedger(512)
		led.Sink = func(o verbs.Outcome) {
			logger.Info("outcome", "verb", o.Verb, "result", o.Result.String(), "ev", o.Evidence)
		}
		// Seed the hand-piloted road as facts (provenance recorded once, consumed as data).
		road := []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
		mem.PutJSON("road.town.blood_moor_gate", memory.ScopeSeed,
			memory.Provenance{Source: "hand-piloted", Evidence: "2026-07-18 sessions, seed 466817790"}, road)
		course := append(append([]data.Position{}, road...), road[len(road)-2], road[0]) // out and back
		hist := map[string]int{}
		for wi, wp := range course {
			for tries := 0; tries < 12; tries++ {
				s := p.Capture()
				if !s.Valid {
					time.Sleep(300 * time.Millisecond)
					continue
				}
				if chebyshev(s.Me.Pos, wp) <= 6 {
					logger.Info("roadtest: waypoint reached", "i", wi, "wp", fmt.Sprintf("(%d,%d)", wp.X, wp.Y))
					break
				}
				if !m.Engage.Engaged() {
					time.Sleep(500 * time.Millisecond) // human has the controls; wait politely
					continue
				}
				o := verbs.Stride{To: wp}.Do(m, gr, p, led, "roadtest")
				hist[o.Result.String()]++
			}
		}
		logger.Info("roadtest: histogram", "results", fmt.Sprintf("%v", hist))
		close(stop)
		return
	}

	// ---- THE EXECUTIVE: one arbiter, one activity per cycle, honest grants ----
	led := verbs.NewLedger(2048)
	// THE ONE COVERAGE MODEL (activity/coverage.go): the tiles she has SEEN,
	// per seed and area, persisted at seed scope — a town trip or a restart
	// comes back to them. Explore and Advance's search share its picker.
	activity.Cov = activity.NewCovTracker(gr, mem, func(msg string, kv ...any) { logger.Info(msg, kv...) })
	sh := newShadow(logger, gr)
	sh.Start()
	led.Sink = func(o verbs.Outcome) {
		// done outcomes are the quiet normal; exceptions speak — except the route
		// owner's decisions, which are the story of WHERE she is going and why.
		// Strike hits and fight summaries are the Carnage run's telemetry
		// (2026-09-24): every one speaks.
		if o.Result != verbs.ResDone || o.Verb == "intent" || o.Verb == "escalate" ||
			o.Verb == "strike" || o.Verb == "fight" {
			logger.Info("outcome", "verb", o.Verb, "holder", o.Holder, "tgt", o.Target, "result", o.Result.String(), "ev", o.Evidence)
		}
		// Nav follower transitions are ResDone too — the trace names every one.
		if o.Verb == "nav" {
			if ln, ok := trace.Nav(o.Holder, o.Evidence, curTick.Load()); ok {
				emit(ln)
			}
		}
		if uiVerbs[o.Verb] {
			sh.Kick()
		}
	}
	activity.PhaseSink = func(ln string) { emit(trace.Phase(ln, curTick.Load())) }
	// The loot brain: config/loot.yaml, verb=lootpick decisions, the loot
	// census, and the item catalog (memory + logs/loot_catalog.jsonl).
	activity.WireLoot(mem, func(msg string, args ...any) { logger.Info(msg, args...) })
	// WARNING 6 (the 12:00 and 12:12 lessons): a swap can inherit ANY panel —
	// the vendor window (readable) or the plain bag (byte-blind, photographed
	// eating six straight talks). The successor heals itself blind: in town,
	// one ESC — the three-bearing stride test then tells whether it closed a
	// panel or raised the pause menu, and a RealEsc restores the difference.
	// Trade panels exist only at vendors: the vendor-stock read LINGERS after
	// leaving town (measured 00:33: a field attach right after a shopping trip
	// saw "readable stock" and fired three blind ESCs — an odd count leaves
	// the pause menu STANDING and the world frozen). Town-only, always.
	// STARTUP UI HYGIENE, BY SIGHT (2026-09-23): the blind ESC dance here raised
	// the pause menu whenever no panel was open, and memory cannot see panels.
	// Close an inherited shop by its own X, then click away any pause/sub-panel.
	// JANITOR ON (v2 step 6): the janitor does this by the same oracle the gate
	// uses — every seen panel is foreign with no holder — and it runs once
	// more every tick, so it also replaces the pause sentry below.
	activity.JanitorOn = janitorOn
	// LAYER 0 — THE SESSION (v2 step 8): owns the relog, which is no longer an
	// arbiter activity; Relog is its trigger and its motor half.
	relog := activity.NewRelog()
	sd := newSessionDriver(logger, m, gr, sh, relog)
	sd.ses.WindCap, sd.ses.PauseFailsafe = *windDownF, *pauseFailsafeF
	gk := newGatekeeper(logger, m, gr, sh, sd.ses)
	hygiene := func() {
		if janitorOn {
			if n := gk.settle(p); n > 0 {
				logger.Info("startup: janitor cleared foreign screens by sight", "actions", n)
			}
		} else {
			if x, y, ok := game.ShopOpenX(gr.Screenshot()); ok {
				logger.Info("startup: inherited trade panel — closing by its X")
				m.RealMenuClick(x, y)
				time.Sleep(400 * time.Millisecond)
			}
			if n := activity.EnsureWorld(gr, m); n > 0 {
				logger.Info("startup: cleared blocking screens by sight", "layers", n)
			}
		}
	}
	// calibrate wraps probing + the owner's declared build: the owner KNOWS the char
	// (classic-bot law — kolbot/koolo configs declared skills; nobody inferred them).
	calibrate := func() combat.Capability {
		calKeys, refused := combat.CalibrationKeys(*calKeysF, *killKey)
		for _, r := range refused {
			logger.Warn("capability: -calkeys key refused (never pressed)", "key", r)
		}
		c := combat.Calibrate(logger, gr, hid, mem, calKeys)
		// Owner declaration sets the KEY; the proven SKILL for that key survives if
		// calibration flipped it (P-8.7 skill-spend needs the skill ID for its tree
		// seat — "skill 0 has no tree seat" was the bare-key binding, 12:48).
		provenSkill := func(key byte) skill.ID {
			for _, b := range c.Proven {
				if b.Key == key {
					return b.Skill
				}
			}
			return 0
		}
		if *meleeKeyF != "" {
			k := hid.GetASCIICode(*meleeKeyF)
			declared := &combat.Binding{Key: k, Skill: provenSkill(k)}
			c.Contact = declared
			c.Combat = declared // an explicit owner declaration outranks auto-preference
			c.LeapAttack = nil
			c.DoubleSwing = nil // do not let the automatic fallback override the declaration
			logger.Info("capability: owner-declared melee key", "key", *meleeKeyF, "skill", int(c.Contact.Skill))
		}
		if *rangedKeyF != "" {
			k := hid.GetASCIICode(*rangedKeyF)
			c.Reach = &combat.Binding{Key: k, Skill: provenSkill(k)}
			logger.Info("capability: owner-declared ranged key", "key", *rangedKeyF, "skill", int(c.Reach.Skill))
		}
		// -tpkey: the owner's word on the Town Portal tome key. Without a
		// binding every portal path (Recall, Unstick, Withdraw, Breakout, the
		// watchdog's portal remedy) is skipped — the wind-down goes straight
		// to its pause rung instead of pressing a dead key.
		switch tk := strings.ToLower(strings.TrimSpace(*tpKeyF)); tk {
		case "":
		case "none", "off", "false", "0":
			c.TownTP = nil
			logger.Info("capability: town portal DISABLED (-tpkey=none)")
		default:
			k := hid.GetASCIICode(tk)
			sk := provenSkill(k)
			if sk == 0 {
				sk, _ = combat.OwnedTownPortal(c.Known)
			}
			c.TownTP = &combat.Binding{Key: k, Skill: sk}
			logger.Info("capability: owner-declared town portal key", "key", tk,
				"skill", int(sk), "skill_name", combat.SkillName(sk))
		}
		if c.TownTP == nil {
			logger.Warn(combat.NoTownPortalLine, "why", combat.TownPortalDetail(c.Known))
		} else {
			logger.Info("capability: town portal binding", "key", int(c.TownTP.Key),
				"skill", int(c.TownTP.Skill), "skill_name", combat.SkillName(c.TownTP.Skill))
		}
		activity.SetTownPortal(c.TownTP != nil)
		// -leftskill: the owner's word on the LEFT-button primary when
		// calibration cannot see it (on) or must not use it (off).
		switch strings.ToLower(*leftSkillF) {
		case "on", "force", "true", "1":
			if c.Left == nil {
				c.Left = combat.ReadLeft(gr.GetData().PlayerUnit)
			}
			c.Left.Forced = true
			logger.Info("capability: left skill FORCED primary (-leftskill=on)",
				"skill", int(c.Left.Skill), "skill_name", c.Left.Name, "proven", c.Left.Proven)
		case "off", "false", "0", "none":
			if c.Left != nil {
				c.Left.Disabled = true
			}
			logger.Info("capability: left-click primary DISABLED (-leftskill=off) — right-skill strikes only")
		default: // auto
			if c.Left.Primary() {
				logger.Info("capability: left skill is the PRIMARY strike",
					"skill", int(c.Left.Skill), "skill_name", c.Left.Name, "mouse", "left")
			} else if c.Left != nil {
				logger.Info("capability: left skill not proven — right-skill strike order",
					"skill", int(c.Left.Skill), "skill_name", c.Left.Name)
			}
		}
		// UNARM THE TOME: probing leaves the LAST flipped skill selected — F3 is the
		// Identify tome, so she stood around visibly armed with Identify (the owner
		// kept catching it). End calibration on a combat selection.
		if c.Reach != nil {
			hid.PressKey(c.Reach.Key)
		} else if c.Contact != nil {
			hid.PressKey(c.Contact.Key)
		}
		return c
	}
	// The engaged start: hygiene, then calibration (Calibrate presses F1..F8
	// through the HID — input). A -disengaged start defers both to the first
	// engaged tick; until then the capability is empty and nothing acts.
	var cap combat.Capability
	started := false
	start := func() {
		started = true
		hygiene()
		cap = calibrate()
		activity.SetBrawler(cap.Reach == nil && cap.Throw == nil) // P-2.-2: no ranged game = the Brawler's Creed
	}
	if *disengagedF {
		sd.ses.Disengage(time.Now(), "-disengaged start: the owner drives until F10")
		logger.Warn("DISENGAGED start: press F10 to engage — perceiving only (screen shadow, trace, flight recorder), no input until then")
	} else {
		start()
	}
	arb := &arbiter.Arbiter{}
	road := []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
	// The hand-piloted road belongs to ONE world — its own provenance says
	// "seed 466817790". On Fableboi's fresh seed it marched him into the
	// corners of a town that doesn't exist (04:03, the barbarian's first
	// minutes). Foreign seed: no legacy road — Advance's border oracle and
	// the road recorder are seed-independent and learn his world instead.
	if gr.MapSeed() != 466817790 {
		road = nil
	}
	mem.PutJSON("road.town.blood_moor_gate", memory.ScopeSeed, memory.Provenance{Source: "hand-piloted", Evidence: "2026-07-18, seed 466817790"}, road)
	// Seed the one door already proven — under the seed it was MEASURED in (the world
	// re-rolls per game; other seeds learn their own doors from the live oracle).
	if _, have := mem.Get(activity.BorderKey(466817790, area.RogueEncampment, area.BloodMoor)); !have {
		mem.PutJSON(activity.BorderKey(466817790, area.RogueEncampment, area.BloodMoor), memory.ScopeSeed,
			memory.Provenance{Source: "hand-piloted", Evidence: "road end, seed 466817790"}, road[len(road)-1])
	}
	// The goal shapes the itinerary: farm grinds the proven circuit's summit;
	// campaign/rampage selects the full route for the act the character is
	// actually standing in. This keeps a saved Act 1 frontier from making an
	// Act 2 character walk back toward Blood Moor.
	legs := activity.FarmItinerary()
	if *goal == "campaign" || *goal == "rampage" {
		startArea := area.ID(gr.GetData().PlayerUnit.Area)
		legs = activity.CampaignItinerary(startArea)
		logger.Info("campaign itinerary selected", "act", startArea.Act(), "startArea", int(startArea), "legs", len(legs))
	}
	// One registry for live and -replay; its order breaks exact bid ties.
	roster := activity.Registry(legs, road)
	sd.recall = roster.Recall // Spent: the wind-down's empty-tome rung trigger
	fight := roster.Fight
	// The lifecycle rides the arbiter's Changes: Suspend on preempt/outbid,
	// Begin on every seat, End on every verdict or monitor release.
	core := &exec.Core[*activity.Ctx]{Arb: arb, Find: roster.Lifecycle, Trace: emit}

	var grid *game.Grid
	// Live journeys adopt every regrid: a wall that streams in across a route
	// replans it at once.
	journey.Source = func() *game.Grid { return grid }
	// Stride's look-ahead reads the CURRENT live grid (the closure follows every
	// regrid). Unknown/unloaded ground stays walkable — only real walls refuse.
	verbs.Walkable = func(p data.Position) bool {
		g := grid
		if g == nil {
			return true
		}
		rp := g.RelativePosition(p)
		if rp.X < 0 || rp.Y < 0 || rp.X >= g.Width || rp.Y >= g.Height {
			return true
		}
		return g.IsWalkable(p)
	}
	gridArea := -1
	var gridAt time.Time
	// THE CARTOGRAPHER: every area crossing she ever makes — by march, by wander, by
	// portal — records BOTH sides of the door as seed facts. Advance consumes them as
	// layer-1 border knowledge; the world's doors accumulate from use.
	lastArea := area.ID(0)
	var lastPos data.Position
	// THE ROAD RECORDER (the owner, 01:47: "a more complex set of waypoints
	// rather than a single priority move order"): a rolling breadcrumb trail
	// of her ACTUAL walk — one crumb per 12+ tiles — persisted on every real
	// crossing as road.<seed>.<from>.<to>. The march replays proven roads
	// crumb by crumb instead of re-deriving geometry every night.
	var crumbs []data.Position
	// THE SEAM DEBOUNCE (03:29, the Monastery Gate: a brawl ON the seam
	// flapped the area read 7<->26 every second and the cartographer stamped
	// a fresh "door" per flip — ledger pollution at event-spam rate. The
	// adopt logic has had this law since P-5.3a; the cartographer never did):
	// a crossing is recorded only after the new area HOLDS 1.5s unbroken.
	crossPend := area.ID(0)
	crossPendAt := time.Time{}
	recordCrossing := func(s *percept.Snapshot) {
		if s.Me.Area == lastArea && lastArea != 0 {
			crossPend = 0 // the flicker died; the seam keeps its secrets
			if len(crumbs) == 0 || chebyshev(crumbs[len(crumbs)-1], s.Me.Pos) >= 12 {
				crumbs = append(crumbs, s.Me.Pos)
				if len(crumbs) > 12 {
					crumbs = crumbs[len(crumbs)-12:]
				}
			}
		}
		if s.Me.Area != lastArea && lastArea != 0 && s.Me.Area != 0 {
			if s.Me.Area != crossPend {
				crossPend, crossPendAt = s.Me.Area, time.Now()
				return // a one-frame flip records nothing
			}
			if time.Since(crossPendAt) < 1500*time.Millisecond {
				return // still proving itself; the old side keeps the stamp
			}
			crossPend = 0
			// THE SHARED CROSSING TIMESTAMP (item 2): a confirmed transition
			// arms the seam hysteresis so the legacy road walkers do not shove
			// her straight back across a gate the deliberate marcher just crossed.
			activity.NoteSeamCross()
			// THE TOWN-HOP WITNESS (night 2: three field→town teleports with NO
			// verb logged — portal ambush, stale walk order, or something still
			// unnamed; the log could not say). Every arrival in town from the
			// field is now stamped so the next forensic run has a timestamp to
			// correlate against the ledger's final verbs.
			if s.Me.Area == 1 {
				logger.Warn("cartographer: TOWN HOP — field to town", "from", int(lastArea),
					"at", fmt.Sprintf("(%d,%d)", lastPos.X, lastPos.Y))
			}
			// Portals teleport (town↔field): near sides = a walked door. But cave
			// WARPS teleport coordinates too (03:45: the owner's manual click into
			// the Underground Passage jumped 3000 tiles and the anti-TP rule threw
			// away the human-proven doorstep) — a big jump between topologically
			// ADJACENT non-town areas is a warp DOOR and records like any other.
			adjacent := false
			if ad, ok := gr.GetData().Areas[lastArea]; ok {
				for _, lv := range ad.AdjacentLevels {
					if lv.Area == s.Me.Area {
						adjacent = true
						break
					}
				}
			}
			if chebyshev(lastPos, s.Me.Pos) <= 40 ||
				(adjacent && !lastArea.IsTown() && !s.Me.Area.IsTown()) {
				seed := gr.MapSeed()
				prov := memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("crossed %d->%d seed %d", int(lastArea), int(s.Me.Area), seed)}
				// THE CONTRADICTION SPEAKS (the owner, 21:40: "there's only one
				// door though" — two learned 4->10 doors 83 tiles apart swapped
				// silently, and one was necessarily a lie, likely a sparse-crumb
				// stamp during a fast manual walk). A re-learn far from the old
				// truth is SAID, not swallowed; latest still wins.
				var oldDoor data.Position
				haveOld := mem.GetJSON(activity.BorderKey(seed, lastArea, s.Me.Area), &oldDoor) && oldDoor.X != 0
				if haveOld && chebyshev(oldDoor, lastPos) <= 15 {
					// ALREADY KNOWN (05:19, the Monastery-Gate seam pin: a 4s
					// oscillation re-wrote the same door hundreds of times, WAL
					// bloat and a drifting stamp). A crossing within 15 of the
					// recorded door teaches nothing — skip the write entirely.
					crumbs = crumbs[:0]
					lastArea, lastPos = s.Me.Area, s.Me.Pos
					return
				}
				if haveOld && chebyshev(oldDoor, lastPos) > 30 {
					logger.Warn("cartographer: door CONTRADICTION — one of these is a lie",
						"pair", fmt.Sprintf("%d->%d", int(lastArea), int(s.Me.Area)),
						"old", fmt.Sprintf("(%d,%d)", oldDoor.X, oldDoor.Y),
						"new", fmt.Sprintf("(%d,%d)", lastPos.X, lastPos.Y))
				}
				mem.PutJSON(activity.BorderKey(seed, lastArea, s.Me.Area), memory.ScopeSeed, prov, lastPos)
				mem.PutJSON(activity.BorderKey(seed, s.Me.Area, lastArea), memory.ScopeSeed, prov, s.Me.Pos)
				if len(crumbs) >= 3 {
					mem.PutJSON(activity.RoadKey(seed, lastArea, s.Me.Area), memory.ScopeSeed,
						memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("walked road, %d crumbs", len(crumbs))},
						append(append([]data.Position{}, crumbs...), lastPos))
					logger.Info("cartographer: ROAD learned", "from", int(lastArea), "to", int(s.Me.Area), "crumbs", len(crumbs)+1)
				}
				logger.Info("cartographer: door learned", "from", int(lastArea), "to", int(s.Me.Area),
					"at", fmt.Sprintf("(%d,%d)", lastPos.X, lastPos.Y))
			}
			crumbs = crumbs[:0]
		}
		lastArea, lastPos = s.Me.Area, s.Me.Pos
	}
	// THE PAD WITNESS (the owner, 03:5x: "it didnt take any waypoints... i had
	// to manually take it"): the touch ritual only ran while the MARCH held —
	// grind mode and fights walked him past pads forever, and the owner's own
	// manual rides taught the lit-ledger nothing. Proximity activates on this
	// game: standing within 3 of a pad IS the activation, whoever is driving,
	// engaged or not. The watcher ledgers it forever, once per area.
	padSeen := map[area.ID]bool{}
	padWitness := func(s *percept.Snapshot) {
		if s.Me.Area == 0 || padSeen[s.Me.Area] {
			return
		}
		dd := gr.GetData()
		for _, ob := range dd.Objects {
			if ob.IsWaypoint() && chebyshev(s.Me.Pos, ob.Position) <= 3 {
				padSeen[s.Me.Area] = true
				lit := false
				mem.GetJSON(activity.LitKey(dd.PlayerUnit.Name, s.Me.Area), &lit)
				if !lit {
					mem.PutJSON(activity.LitKey(dd.PlayerUnit.Name, s.Me.Area), memory.ScopeForever,
						memory.Provenance{Source: "measured", Evidence: "stood at the pad — proximity activates"}, true)
					logger.Info("cartographer: PAD lit by proximity", "area", int(s.Me.Area))
				}
				return
			}
		}
	}
	// Regrid: mid-area grid regrowth for the leg-walker — rooms stream in as she walks,
	// and a grid built at the border knows nothing of the far exit.
	regrid := func() *game.Grid {
		if g, err := fz.build(); err == nil {
			grid = g
			logger.Info("executive: grid regrown", "origin", fmt.Sprintf("(%d,%d)", g.OffsetX, g.OffsetY))
		}
		return grid
	}
	// FLIGHT RECORDER: 1 Hz snapshot samples — the replay corpus (-replay reads it) —
	// plus a rolling ring dumped as a BLACK BOX on death. Live failures become
	// desk-checkable; fixes get verified against recorded reality, not her blood.
	flightPath := fmt.Sprintf("logs/flight_%d.jsonl", time.Now().Unix())
	var flightW *bufio.Writer
	if ff, err := os.Create(flightPath); err == nil {
		flightW = bufio.NewWriter(ff)
		defer func() { flightW.Flush(); ff.Close() }()
		logger.Info("flight recorder live", "path", flightPath)
	}
	var flightAt time.Time
	lastBids := 0 // demands in the last Decide, for the decision frame
	// record writes the 1 Hz sample — engaged or not: a disengaged run (the
	// owner driving, -disengaged, a wind-down hold) is evidence too.
	record := func(tick uint64, s *percept.Snapshot) {
		if flightW == nil || time.Since(flightAt) < time.Second {
			return
		}
		if b, err := json.Marshal(s); err == nil {
			flightW.Write(b)
			flightW.WriteByte('\n')
			flightW.Flush()
		}
		// The decision frame follows its snapshot (replay skips it by "k").
		if b, err := json.Marshal(sh.frame(tick, s, arb, core, roster, lastBids)); err == nil {
			flightW.Write(b)
			flightW.WriteByte('\n')
			flightW.Flush()
		}
		flightAt = time.Now()
	}
	var ring []*percept.Snapshot
	wasArmed := false
	wasDead := false
	// The watchdog is pure: it observes and prescribes (stuck, orbit, thrash, the
	// deadman box, the pacer); the executive benches, and Unstick performs.
	wd := watchdog.New()
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	// A stale stop file must not end the new run on its first tick.
	if err := os.Remove(stopNowPath); err == nil {
		logger.Warn("startup: removed a stale stop request", "path", stopNowPath)
	}
	var stopLookAt time.Time // last look for the owner's stop.now
	statusAt := time.Time{}
	stallWarnAt := time.Time{}
	prevEngaged := true
	engagedAt := time.Now()
	var lastEvidence time.Time // the ledger's last append credited as the holder's progress
	lastPhase := ""            // the holder's last reported phase (a change is progress)
	cursorItemAt := time.Time{}
	cursorDropAt := time.Time{}
	sawInvalid := false
	// NEW GAME detection: a validity gap (relog, load screen) may mean a fresh
	// world — the seed re-rolls per game. FetchMapData no-ops when the seed is
	// unchanged; on a real change it re-fetches and the grid realigns below.
	// The session's AwaitWorld reads the seed this refreshes.
	newWorld := func() {
		if !sawInvalid {
			return
		}
		sawInvalid = false
		prevSeed := gr.MapSeed()
		if err := gr.FetchMapData(); err == nil && gr.MapSeed() != prevSeed {
			logger.Info("executive: NEW WORLD", "seed", gr.MapSeed())
			gridArea = -1 // force grid realign
			lastArea = 0  // don't record a phantom crossing over the gap
			// Beliefs retired by one world's bad frames must not silence
			// services in the next (WARNING 8; the reviewer's finding 2).
			activity.NewWorld()
		}
	}
	var drillAt time.Time // last look for the owner's relog.now
	wasRelogging := false
	wasFocused := true
	var trail []string // the last moments, for the owner's death reports
	var trailAt time.Time
	holderOf := func(a *arbiter.Arbiter) string {
		if g := a.Current(); g != nil {
			return g.Demand.Who
		}
		return "-"
	}
	lastDeEsc := time.Time{}
	gateOpenAt := time.Time{} // janitor ON: when the gate last reopened (the stall alarm's grace)
	idleSince := time.Time{}
	idleSaidAt := time.Time{}
	lastTick := time.Time{}
	executiveLive.Store(true)
	for {
		// THE TICK HAS A FLOOR (night-2 audit finding 10): the granted path had
		// no sleep at all — the executive busy-spun Capture+Observe+Step at
		// maximum rate, spraying sub-350ms no-op cycles and periodically
		// stalling on the 6s grid rebuild mid-fight. 40ms floor ≈ 25 ticks/s:
		// faster than any verb needs, calmer than any spin.
		if dt := time.Since(lastTick); dt < 40*time.Millisecond {
			time.Sleep(40*time.Millisecond - dt)
		}
		lastTick = time.Now()
		tick := curTick.Add(1)
		core.Tick = tick
		// WARNING 7 (revised twice; the owner 2026-07-19 night: "we didn't need to
		// focus diablo before"): the bot NEVER steals focus AND never stops playing
		// for lack of it. In-game input is posted window messages + the injector's
		// patched cursor — it reaches D2R focused or not, and lands ONLY in the game
		// window, so there is no conflict with the owner's apps to stand by for.
		// The 22:08 lesson: a dead amazon lay unrespawned behind a Discord window
		// while a stand-by gate idled the whole run. Menus (Relog) remain the one
		// hardware-input exception and grab foreground for their single click.
		// If the world is truly paused, verbs read deaf into a frozen game — free.
		if focused := m.GameFocused(); focused != wasFocused {
			if focused {
				logger.Info("executive: game refocused")
				wd.Reset() // a pause-frozen position history would read as pathology
			} else {
				logger.Info("executive: game unfocused — playing on (posted input, your windows untouched)")
			}
			wasFocused = focused
		}
		s := p.Capture()
		// SHADOW SCREEN (v2 step 5): read, trace and publish what is on screen —
		// engaged or not, so the owner's panels are named too. Nothing gates on it.
		sh.Feed(tick, s, holderWho(arb))
		// LAYER 0 — THE SESSION (v2 step 8). A relog is requested by Relog's
		// trigger (a naked girl in town, her body far) or by the owner's
		// logs/relog.now; once the session takes it, the holder's episode ends
		// and the session owns every tick until it is InGame again — nothing
		// below acts: no arbitration, no gate, no monitors, no sentry.
		if s.Valid && sd.ses.Relogging() {
			newWorld() // AwaitWorld judges the refreshed seed
		}
		if sd.ses.State() == exec.InGame && s.Valid && m.Engage.Engaged() {
			now := time.Now()
			why, want := relog.Wants(s, now)
			drill := false
			if !want && now.Sub(drillAt) >= time.Second {
				drillAt = now
				if _, err := os.Stat(relogNowPath); err == nil {
					why, want, drill = "owner drill ("+relogNowPath+")", true, true
				}
			}
			if want && sd.request(why) {
				if drill {
					_ = os.Remove(relogNowPath)
				}
				if who := holderWho(arb); who != "" {
					m.MoveStop()
					core.End(&activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: grid, Cap: &cap, Snap: s, Mem: mem,
						Seen: sh.Eye.Latest(), Held: arb.Held}, who, phase.Abandoned, phase.Preempted, "session: relog")
				}
			}
		}
		// THE SAFE END (relay R4): the budget, the owner's stop file and a first
		// Ctrl-C ask the session to wind down; the session decides when the run
		// may end (in town, or no living monster within 40 for 3s).
		if !sd.ses.Winding() {
			switch {
			case !time.Now().Before(deadline):
				sd.stop(tick, "time budget spent")
			case time.Since(stopLookAt) >= time.Second:
				stopLookAt = time.Now()
				if _, err := os.Stat(stopNowPath); err == nil {
					_ = os.Remove(stopNowPath)
					sd.stop(tick, "owner stop ("+stopNowPath+")")
				}
			}
			select {
			case why := <-stopReq:
				sd.stop(tick, why)
			default:
			}
		}
		sesOwns := sd.step(tick, s)
		sh.stateLine(tick, s, sd.ses.String(), arb, roster)
		// WindTown: the town road (Recall, Withdraw's TP ride) bids this tick.
		roster.Recall.Want(sd.out.Wind == exec.WindTown)
		if w := sd.out.Wind; w == exec.WindExit || w == exec.WindHold || w == exec.WindPause {
			// Every way nothing more is driven: stop the feet and end the
			// holder's episode (its lease and keys go with it). WindPause: the
			// session's ESC follows on a later tick it owns.
			m.MoveStop()
			if who := holderWho(arb); who != "" {
				core.End(&activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: grid, Cap: &cap, Snap: s, Mem: mem,
					Seen: sh.Eye.Latest(), Held: arb.Held}, who, phase.Abandoned, phase.Preempted, "session: "+sd.ses.String())
			}
			if w == exec.WindExit {
				// A paused exit leaves the pause menu up: the session claims it,
				// and nothing on the way out sends an ESC or clicks Return to Game.
				logger.Warn("SESSION: safe to stop — exiting", "town", s.Valid && s.Me.InTown,
					"paused", sd.ses.Paused(),
					"pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y), "area", int(s.Me.Area))
				break
			}
			if w == exec.WindHold {
				// The last rung: hand the controls back (the kill-switch's own
				// path) and hold — perceiving, reminding every 30s, never
				// exiting while hot. WindPause stays engaged: the sentinel
				// drinks on until the ESC's menu is seen.
				m.Disengage()
			}
		}
		if wasRelogging && !sd.ses.Relogging() {
			// A relog moves her to a new world's spawn (or leaves the world
			// intact after a pause): the position monitors start fresh, the
			// refocus precedent, so the jump is not read as pathology.
			wd.Reset()
		}
		wasRelogging = sd.ses.Relogging()
		if sesOwns {
			sawInvalid = sawInvalid || !s.Valid
			continue
		}
		if !started && s.Valid && m.Engage.Engaged() {
			// -disengaged: F10 engaged for the first time — the deferred start.
			neverEngaged = false
			logger.Warn("ENGAGED: first engagement of a -disengaged run — startup hygiene and calibration now")
			start()
			wasArmed = s.Me.Armed // calibrated just now: not an armed-flip event
			wd.Reset()
			continue
		}
		if !s.Valid || !m.Engage.Engaged() {
			// THE WATCHER NEVER SLEEPS (01:12, the owner: "i even entered dark
			// wood" — and the cartographer was DEAF because this gate skipped
			// perception while they drove; their first crossing taught nothing
			// and the second recorded only by toggle luck). Perception is
			// read-only: record the owner's crossings and crumbs hands-off —
			// shepherding is a TEACHING MODE now, guaranteed, not a coin flip.
			if s.Valid {
				recordCrossing(s)
				padWitness(s) // the owner's pad stands teach the ledger too
				record(tick, s)
			}
			sawInvalid = sawInvalid || !s.Valid
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// WARNING 10 — THE WALL EDITS THE WORLD: stamp Walled once per tick so
		// every demand and step downstream counts only enemies with a clear line.
		// 30 tiles covers every proximity bar in use (crowd 25, hunt 45 excepted —
		// far picks re-check LoS themselves at selection).
		if grid != nil && !s.Me.InTown {
			for i := range s.Enemies {
				e := &s.Enemies[i]
				dx, dy := e.Pos.X-s.Me.Pos.X, e.Pos.Y-s.Me.Pos.Y
				if dx < 0 {
					dx = -dx
				}
				if dy < 0 {
					dy = -dy
				}
				if dx <= 30 && dy <= 30 {
					e.Walled = !activity.LosClear(grid, s.Me.Pos, e.Pos)
				}
			}
		}
		newWorld() // after a validity gap: a new seed means a new world
		recordCrossing(s)
		padWitness(s)            // any brush with a pad lights it, whatever the holder
		activity.ObserveBlood(s) // P-2.0: one blood truth for every Demand this cycle
		// Grid follows the area (the re-align, owned in one place) — and REGROWS on a
		// clock in the field: rooms stream in as she walks, and a grid built at the
		// border brands every unloaded room a wall. Loot/Reclaim/Fight journeys were
		// planning against that stale truth (Advance was the only one regridding);
		// fresh journeys now always start on current rooms.
		if int(s.Me.Area) != gridArea {
			if g, err := fz.build(); err == nil {
				grid, gridArea = g, int(s.Me.Area)
				gridAt = time.Now()
				logger.Info("executive: grid re-aligned", "area", gridArea)
			}
		} else if !s.Me.InTown && time.Since(gridAt) > 6*time.Second {
			if g, err := fz.build(); err == nil {
				grid = g
				gridAt = time.Now()
			}
		}
		// Coverage follows the same grid: mark what she sees (a no-op unless she
		// moved), rebuild its terrain when the grid regrew, flush every 10s.
		activity.Cov.Tick(s, grid, area.ID(gridArea))
		// DEATH REPORT for the owner ("i didnt even know how it died"): the last
		// moments, from the trail ring — hp slope, who held the actuator, how many
		// teeth were on her.
		if time.Since(trailAt) >= 900*time.Millisecond || s.Me.HPPct <= 0 {
			trailAt = time.Now()
			near := 0
			for _, e := range s.Enemies {
				if chebyshev(s.Me.Pos, e.Pos) <= 12 {
					near++
				}
			}
			trail = append(trail, fmt.Sprintf("%s hp=%d holder=%s near=%d pos=(%d,%d)",
				s.At.Format("15:04:05"), s.Me.HPPct, holderOf(arb), near, s.Me.Pos.X, s.Me.Pos.Y))
			if len(trail) > 12 {
				trail = trail[1:]
			}
		}
		// Flight recorder: ring every cycle, sampled line every second.
		ring = append(ring, s)
		if len(ring) > 300 {
			ring = ring[1:]
		}
		record(tick, s)
		// Self-model events: armed flip OR back-from-death → recalibrate capability.
		deadNow := s.Me.HPPct <= 0
		if deadNow && !wasDead {
			logger.Warn("DEATH REPORT — the last moments:")
			for _, ln := range trail {
				logger.Warn("  " + ln)
			}
			// BLACK BOX: the full snapshot ring around the death — replayable evidence.
			bbPath := fmt.Sprintf("logs/blackbox_%d.jsonl", time.Now().Unix())
			if bf, err := os.Create(bbPath); err == nil {
				w := bufio.NewWriter(bf)
				for _, snap := range ring {
					if b, err := json.Marshal(snap); err == nil {
						w.Write(b)
						w.WriteByte('\n')
					}
				}
				w.Flush()
				bf.Close()
				logger.Warn("BLACK BOX dumped", "path", bbPath, "frames", len(ring))
			}
		}
		if (s.Me.Armed != wasArmed || (wasDead && !deadNow)) && !deadNow {
			logger.Info("executive: self-model event — recalibrating", "armed", s.Me.Armed, "revived", wasDead)
			cap = calibrate()
			activity.SetBrawler(cap.Reach == nil && cap.Throw == nil)
			fight.Recalibrated() // the audit follows the hands (P-7.1)
			wasArmed = s.Me.Armed
		}
		wasDead = deadNow

		// THE MENU SENTRY (the owner, 00:52: "an aware state that de-escs
		// unless there's a good reason like relogging"): the quit menu is
		// READABLE (OpenMenus.QuitMenu, UI byte 0x09 — never byte-blind after
		// all). Standing unsanctioned, it is a wedge that freezes the world;
		// the sentry closes it within a tick. A relogging session alone claims it
		// (and while it relogs this code is not reached at all).
		// 2026-09-23: s.QuitMenu reads FALSE with the pause menu on screen, so this
		// sentry never fired and a stray pause ate whole runs. The screen is the
		// oracle now (game.PauseMenuVisible, 12/12 vs 0/12 on real captures), and
		// the cure is a click on Return to Game — never an ESC, which toggles.
		// JANITOR ON: retired — the gate below sees the pause menu every tick
		// and the janitor clicks Return to Game (the session's claim honoured).
		if !janitorOn && sd.ses.Claims()&screen.PauseMenu == 0 && time.Since(lastDeEsc) > 3*time.Second {
			lastDeEsc = time.Now()
			if activity.ClearPause(gr, m) {
				logger.Warn("menu sentry: pause menu on screen with no sanction — clicked Return to Game")
				continue
			}
		}

		demands := roster.Demands(s) // registry order, Order stamped
		// A bench must be real. The old empty-field fallback immediately
		// re-granted the activity the watchdog had just convicted (observed:
		// "cooling advance for 15s" followed by an advance grant 38ms later),
		// turning recovery into the same failed plan repeated forever. The
		// arbiter filters benched bids; only when EVERY bidder is benched and
		// one is within two seconds of release are they re-seated early.
		if len(demands) > 0 {
			soonest, all := time.Duration(1<<63-1), true
			for _, d := range demands {
				left, ok := arb.BenchLeft(d.Who)
				if !ok {
					all = false
					break
				}
				soonest = min(soonest, left)
			}
			if all && soonest <= 2*time.Second {
				for _, d := range demands {
					arb.Unbench(d.Who)
				}
				logger.Info("bench: every bidder benched — re-seating early", "left", soonest.Round(100*time.Millisecond))
			}
		}
		// One context per tick: the lifecycle calls inside Decide and the Step
		// below see the same world.
		actx := &activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: grid, Cap: &cap, Snap: s, SwapKey: hid.GetASCIICode(*swapKey), InvKey: hid.GetASCIICode(*invKeyF), Regrid: regrid, Mem: mem,
			Seen: sh.Eye.Latest(), Held: arb.Held}
		if janitorOn {
			actx.Screen = gk.Stable()
		}
		grant, gch := core.Decide(actx, demands)
		lastBids = len(demands)

		// THE GATE (v2 step 6, janitor ON): the holder's Needs against the
		// stable screen Reading. A foreign panel or cursor item earns ONE
		// janitor action; the holder's clock pauses and it does not Step. The
		// monitors below are skipped too — a gated holder is not stuck.
		if janitorOn {
			open, was := gk.step(tick, s, arb, roster)
			if was > 2*time.Second {
				// A long block starved the position monitors: start them fresh
				// (the refocus precedent) so the wait is not read as a wedge.
				wd.Reset()
			}
			if was > 0 {
				gateOpenAt = time.Now()
			}
			if !open {
				continue
			}
		}

		// SELF-OBSERVATION (the owner's ask: "tell what the bot is up to, moments where
		// it's stuck, looping, thrashing — and unstuck itself"). The watchdog is pure:
		// it reads the position and grant streams and returns a verdict with its
		// evidence and a remedy. The executive (a) hands the verdict to the culprit
		// (Judged — Advance climbs its ladder), (b) benches it, (c) opens a
		// prescription the Unstick activity bids on. No monitor moves the character
		// (docs/AZBOT_V2.md step 9): the escape strides, the pocket breaker's
		// blocking TP and the deadman/pacer TP all live in Unstick now.
		holderName := ""
		if grant != nil {
			holderName = grant.Demand.Who
		}
		wd.Observe(watchdog.Sample{Pos: s.Me.Pos, Holder: holderName, InTown: s.Me.InTown, Dead: s.Me.HPPct <= 0})
		enemyAt := make([]data.Position, 0, len(s.Enemies))
		for _, e := range s.Enemies {
			enemyAt = append(enemyAt, e.Pos)
		}
		// A volleying archer and a melee Stand hold ground by design: fight/stand
		// with a target in range vouches for stillness (Stuck/Orbit suppressed,
		// Thrash still watched; the deadman box answers whoever holds).
		if v := wd.Check(watchdog.Context{
			Holder:       holderName,
			StationaryOK: watchdog.StationaryOK(holderName, s.Me.Pos, enemyAt),
			CrossingHot:  activity.CrossingHot(),
			CanPortal:    !s.Me.InTown && s.Me.HPPct > 0 && cap.TownTP != nil,
		}); v.Pathology != watchdog.Healthy {
			why := "judged: " + v.Summary()
			emit(trace.Watchdog(v.Pathology.String(), v.Remedy.String(), v.Culprit, v.Evidence, tick))
			logger.Warn("PATHOLOGY", "kind", v.Pathology.String(), "remedy", v.Remedy.String(),
				"culprit", v.Culprit, "holder", v.Holder, "evidence", v.Evidence)
			mem.PutJSON("pathology.last", memory.ScopeGame,
				memory.Provenance{Source: "measured", Evidence: v.Evidence},
				map[string]any{"kind": v.Pathology.String(), "remedy": v.Remedy.String(), "at": time.Now().UnixMilli()})
			// (a) The culprit hears its verdict first (the place's holder when
			// the place, not a holder, is convicted).
			judged := v.Culprit
			if judged == "" {
				judged = v.Holder
			}
			if j, ok := roster.Get(judged).(activity.Judgeable); ok {
				j.Judged(actx, v)
			}
			// (b) Bench the culprit: the next Decide releases it with the
			// verdict as its reason. Survival and recovery are never benched;
			// a convicted holder of those classes still has its episode ended.
			convicted := false
			classOf := func(who string) arbiter.Class {
				cls := arbiter.ClassIdle
				for _, d := range demands {
					if d.Who == who {
						cls = d.Class
					}
				}
				return cls
			}
			if v.Pathology == watchdog.Thrash {
				// Thrash is a decision problem between bidders: silence the
				// least important one that is not fighting or surviving. Benching
				// the fight (or ending Stand) mid-pack left her swinging at
				// nothing; when only those classes ping-pong, log and let them be.
				v.Culprit = ""
				for i := len(v.Involved) - 1; i >= 0; i-- {
					if c := v.Involved[i]; classOf(c) > arbiter.ClassFight && (v.Culprit == "" || classOf(c) > classOf(v.Culprit)) {
						v.Culprit = c
					}
				}
			}
			if v.Culprit != "" {
				cls := classOf(v.Culprit)
				if cls > arbiter.ClassRecover {
					arb.Bench(v.Culprit, time.Now().Add(v.BenchFor), why)
					convicted = v.Culprit == holderName
				} else if v.Culprit == holderName {
					core.End(actx, holderName, phase.Abandoned, phase.Judged, why)
					convicted = true
				}
			}
			// (c) Prescribe: Unstick bids from the next tick. Thrash is a
			// decision problem — bench only, no prescription.
			if v.Remedy != watchdog.RemedyNone {
				rx := unstick.Rx{Kind: v.Pathology.String(), Portal: v.Remedy == watchdog.RemedyPortal,
					Site: v.Site, Area: int(s.Me.Area), Opened: time.Now(), Culprit: v.Culprit, Evidence: v.Evidence}
				if roster.Unstick.Prescribe(rx) {
					logger.Info("prescription opened", "remedy", v.Remedy.String(), "kind", rx.Kind,
						"site", fmt.Sprintf("(%d,%d)", rx.Site.X, rx.Site.Y))
				}
			}
			if convicted {
				continue // the convicted holder does not Step this tick
			}
		}
		if grant == nil {
			if time.Since(statusAt) > 5*time.Second {
				logger.Info("status: idle", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
					"area", int(s.Me.Area), "hp", s.Me.HPPct, "lvl", s.Me.Level)
				statusAt = time.Now()
			}
			// THE IDLE LOG (was the idle breaker, the owner, 04:47: 'hangs on
			// Akara'). It used to cool every service to shove the march back on
			// the wheel; in v2 no monitor actuates, so sustained idle is NAMED:
			// who is benched and why, and whether a prescription waits.
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) > 12*time.Second && time.Since(idleSaidAt) > 12*time.Second {
				idleSaidAt = time.Now()
				var benchedNow []string
				for _, a := range roster.Acts {
					if bwhy, ok := arb.Benched(a.Name()); ok {
						benchedNow = append(benchedNow, a.Name()+"("+bwhy+")")
					}
				}
				rx := "-"
				if o, ok := roster.Unstick.Open(); ok {
					rx = o.Kind
				}
				logger.Warn("idle: nobody bids", "for", time.Since(idleSince).Round(time.Second),
					"town", s.Me.InTown, "benched", strings.Join(benchedNow, " "), "rx", rx)
				// The town deadlock valve (a decision, not an actuation): an abandoned
				// errand keeps ServicesPending true, so Travel stands down for it and
				// nobody bids ("hangs on Akara"). Silencing services frees the march;
				// the errands retry next trip. Retired when the Director owns TownVisit.
				activity.CoolAllServices(90 * time.Second)
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		idleSince = time.Time{}
		if gch.Changed() {
			logger.Info("grant", "from", gch.From, "to", grant.Demand.Who, "class", grant.Demand.Class.String(),
				"urgency", fmt.Sprintf("%.2f", grant.Demand.Urgency), "why", gch.Why())
		}
		// WAIT (contract v2): a holder that answered Wait keeps the grant but
		// is not Stepped until its WakeAt. Arbitration above still ran, so a
		// survival bid preempts a sleeper like any holder (and drops the park).
		var st exec.Status
		if core.Asleep(grant.Demand.Who, time.Now()) {
			st = exec.Status{V: phase.Wait}
		} else {
			st = roster.Get(grant.Demand.Who).Step(actx)
			core.Park(grant.Demand.Who, st)
		}
		if st.V.Terminal() {
			logger.Info("verdict", "activity", grant.Demand.Who, "verdict", st.V.String())
			core.End(actx, grant.Demand.Who, st.V, st.Why, st.Evidence) // was arb.Release()
		} else {
			// Progress evidence for the arbiter's mute clock: a ledger outcome, a
			// phase change, or an honest Wait with a wake time.
			who := grant.Demand.Who
			if la := led.LastAppend(); la.After(lastEvidence) {
				lastEvidence = la
				arb.MarkProgress(who)
			}
			if ph := who + "/" + st.Phase; ph != lastPhase {
				lastPhase = ph
				arb.MarkProgress(who)
			}
			if st.V == phase.Wait && st.WakeAt.After(time.Now()) {
				arb.MarkProgress(who)
			}
		}
		// THE STALL ALARM (the owner, session 2: "bouts of idleness while
		// surrounded by monsters"; 13:03 anatomy: six Survive grants, 26s,
		// zero outcomes — a mute holder bled him out invisibly). A holder with
		// no progress evidence for its bar is named in the log while it
		// happens: Survive at 2s, everyone else 4s. Silence is the arbiter's
		// mute clock — held time, so gate blocks and preemption never count.
		// Engage-transition grace (21:39: the ledger is naturally silent while
		// the OWNER drives, so the alarm fired the instant F10 handed back).
		if m.Engage.Engaged() != prevEngaged {
			prevEngaged = m.Engage.Engaged()
			engagedAt = time.Now()
			arb.MarkProgress(holderWho(arb))
		}
		// Gate grace (janitor ON): a holder the gate held was silent by order.
		if grant != nil && !st.V.Terminal() && holderWho(arb) == grant.Demand.Who &&
			time.Since(engagedAt) > 5*time.Second && time.Since(gateOpenAt) > 5*time.Second {
			who := grant.Demand.Who
			bar := 4 * time.Second
			if grant.Demand.Class == arbiter.ClassSurvive {
				bar = 2 * time.Second
			}
			silent := arb.Silence(who)
			if silent > bar && time.Since(stallWarnAt) > 2*time.Second {
				logger.Warn("STALL — mute holder", "class", grant.Demand.Class.String(),
					"holder", who, "silent", silent.Round(100*time.Millisecond),
					"hp", s.Me.HPPct, "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y))
				stallWarnAt = time.Now()
			}
			// NO MUTE GRANT (2026-09-23, the owner: "the occasional idling"):
			// the arbiter's mute policy — twice the bar of silence ends the
			// episode as Abandoned(Mute) and benches the holder briefly so the
			// next bidder acts. Survive is exempt: its silence is a bug to fix,
			// but yanking it mid-crowd is worse; recovery is ended, not benched.
			if grant.Demand.Class != arbiter.ClassSurvive && arb.Mute(who, 2*bar) {
				detail := fmt.Sprintf("stall: silent %s", silent.Round(100*time.Millisecond))
				logger.Warn("STALL — mute holder ended", "holder", who, "silent", silent.Round(100*time.Millisecond))
				core.End(actx, who, phase.Abandoned, phase.Mute, detail)
				if grant.Demand.Class > arbiter.ClassRecover {
					arb.Bench(who, time.Now().Add(5*time.Second), "mute: "+detail)
				}
			}
		}
		// THE CURSOR-ITEM DROP (the owner, 02:13: "inventory open and an item
		// held by the cursor, locking the bot — all it has to do is lmb to
		// drop it"). Parking-by-Equip was the polite cure and it starves when
		// the item has no docket home; every other activity WAITS on
		// CursorItem — the lock. Three seconds of held item → one LMB at his
		// feet (a unique gets re-looted by doctrine; junk stays where junk
		// belongs) → one ESC for the byte-blind bag that identify/equip
		// opened (the menu sentry cures a stray pause menu within a tick).
		// JANITOR ON: replaced by the gate's cursor rule — a foreign item on
		// clear ground is dropped at the same spot, over an open bag it is left
		// (logged), and the drop is judged by a later Reading. Never an ESC.
		if !janitorOn {
			if s.Me.CursorItem {
				if cursorItemAt.IsZero() {
					cursorItemAt = time.Now()
				}
				if time.Since(cursorItemAt) > 3*time.Second && time.Since(cursorDropAt) > 10*time.Second &&
					m.Engage.Engaged() {
					logger.Warn("watchdog: CURSOR-ITEM DROP — lmb at feet, esc the bag")
					m.BareClick(gr.GameAreaSizeX/2, gr.GameAreaSizeY/2+140)
					time.Sleep(400 * time.Millisecond)
					m.RealEsc()
					cursorDropAt = time.Now()
					cursorItemAt = time.Time{}
				}
			} else {
				cursorItemAt = time.Time{}
			}
		}
		if time.Since(statusAt) > 10*time.Second {
			// mp/maxmana joined 2026-07-20 11:30 (the owner: "he has about 33
			// mana now and gains mana per hit — the bot is still not aware as
			// much as we'd like"): the read was always live; the TELEMETRY
			// wasn't, and stale assumptions grew in the dark.
			logger.Info("status", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
				"area", int(s.Me.Area), "hp", s.Me.HPPct, "mp", s.Me.MPPct, "maxmana", s.Me.MaxMana,
				"lvl", s.Me.Level, "gold", s.Me.Gold,
				"weapon", s.Me.WeaponKind, "arrows", s.Me.Arrows, "holder", grant.Demand.Who)
			statusAt = time.Now()
		}
	}
	// THE NORMAL EXIT, reached only when the session judged it safe (Stopped):
	// the feet were stopped above; the defers release modifiers and heal the
	// input patches exactly as every clean exit does.
	activity.Cov.Flush() // the last seen tiles reach the WAL before the scribe stops
	close(stop)
	logger.Info("azbot done", "ses", sd.ses.String())
}
