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
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/sentinel"
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
	acts := []activity.Activity{&activity.Breakout{}, &activity.Flee{}, activity.NewDodge(),
		&activity.Respawn{}, activity.NewRelog(), activity.NewReclaim(), activity.NewFight(),
		activity.NewLoot(), activity.NewFence(), activity.NewRestock(), activity.NewRepair(),
		activity.NewHeal(), activity.NewIdentify(), activity.NewEquip(), activity.NewAdvance(legs),
		&activity.Return{}, &activity.Explore{}}
	arb := &arbiter.Arbiter{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	frame, last := 0, ""
	for sc.Scan() {
		frame++
		var s percept.Snapshot
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			continue
		}
		var demands []arbiter.Demand
		for _, a := range acts {
			if d := a.Demand(&s); d != nil {
				demands = append(demands, *d)
			}
		}
		grant, changed := arb.Decide(demands)
		if changed || (grant == nil && last != "-") {
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
					"weapon", s.Me.WeaponKind, "bids", len(demands))
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
	seconds := flag.Int("seconds", 3600, "run duration in seconds")
	dpiScale := flag.Float64("dpiscale", 1.25, "display scale (this laptop: 1.25)")
	moveKey := flag.String("move", "e", "Force Move key (D2R Options>Controls binding)")
	swapKey := flag.String("swap", "w", "weapon-swap key (the bowzon dance: bow at range, javelin at contact)")
	killKey := flag.String("killswitch", "f10", "hotkey to toggle bot control (f9|f10|f11|pause|scrolllock — NOT f12, Windows reserves it for debuggers). Disengage heals all input patches — the human owns Diablo instantly.")
	belt := flag.String("belt", "1,2,3,4", "belt column keys")
	drinkAt := flag.Int("drinkat", 55, "sentinel drinks at or below this HP%")
	memDir := flag.String("memdir", "logs/azmem", "memory store directory (WAL)")
	goal := flag.String("goal", "farm", "the Director's current goal: farm (routes+explore+loot) | campaign/rampage (Act 1 march) | gamble (Gheed errand when bankrolled). Goals shape WHICH activities bid; the arbiter still owns every moment.")
	meleeKeyF := flag.String("meleekey", "", "OWNER-DECLARED melee skill key (e.g. f1 for Jab) — config beats inference on a scrambled mod; overrides calibration")
	rangedKeyF := flag.String("rangedkey", "", "OWNER-DECLARED bow skill key — overrides calibration (the flinch audit still verifies)")
	invKeyF := flag.String("invkey", "i", "inventory-panel toggle key (the Equip service's door; KeyBindings memory is dead, so declare it if rebound)")
	roadTest := flag.Bool("roadtest", false, "M2 soak: walk the measured town road out and back on Stride verbs, print the outcome histogram, exit")
	jTest := flag.String("jtest", "", "M3 soak: journey to world x,y on the live grid via the Journey authority, print the verdict, exit")
	fightTest := flag.Bool("fighttest", false, "M4 soak: calibrate capability, cross to Blood Moor, hover-strike nearest enemies with evidence, exit")
	portalTest := flag.Bool("portaltest", false, "manual harness: find the nearest portal in the snapshot, approach if far, click through with the EnterPortal verb, report every state change, exit")
	charsiTest := flag.Bool("charsitest", false, "manual harness: walk to Charsi, open TRADE (menu byte + Down/Enter), screenshot the shop for repair-button calibration; with -repairxy also click it and report the gold delta, exit")
	repairXY := flag.String("repairxy", "", "client x,y of the repair button (measured from logs/charsi_shop.png)")
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
	relogTest := flag.Bool("relogtest", false, "manual harness, staged: alone = open the pause menu, screenshot it (logs/relog_pausemenu.png), close it. With -exitxy = click Save+Exit, screenshot the main menu (logs/relog_mainmenu.png). With -playxy too = full relog loop, verify the corpse materialized in town")
	replayF := flag.String("replay", "", "OFFLINE DECISION REPLAY: path to a flight .jsonl — every frame runs Demand + arbiter and prints the grant timeline. No game needed; live failures become desk-checkable evidence (the STE mentality: verify against recorded reality, not her blood)")
	exitXY := flag.String("exitxy", "", "relogtest: screenshot x,y of the pause menu's Save and Exit button")
	playXY := flag.String("playxy", "", "relogtest: screenshot x,y of the main menu's Play button")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	flag.Parse()

	// ---- OFFLINE REPLAY: no attach, no injector, no game — pure decision review ----
	if *replayF != "" {
		legs := activity.FarmItinerary()
		if *goal == "campaign" || *goal == "rampage" {
			legs = activity.Act1Itinerary()
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
	game.SetWorldScale(*dpiScale)

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
	gi, err := game.InjectorInit(logger, pid)
	if err != nil {
		logger.Error("injector init failed", "err", err)
		return
	}
	if err := gi.Load(); err != nil {
		logger.Error("injector load failed", "err", err)
		return
	}
	defer func() { gi.Unload(); gi.Close() }()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("signal — healing D2R input and exiting")
		gi.Unload()
		gi.Close()
		os.Exit(0)
	}()
	hid := game.NewHID(gr, gi)
	// Modifier amnesty at attach AND exit (LIFO: this defer runs before gi.Unload's
	// heal): clear latched shift/ctrl/alt so nobody inherits a stuck modifier.
	hid.ModifierAmnesty()
	game.SendModifierUpReal()
	defer func() { hid.ModifierAmnesty(); game.SendModifierUpReal() }()

	// ---- M0: epistemics gate ----
	p := percept.New(gr)
	// Let the game state settle before judging (load screens read garbage).
	var report percept.AttachReport
	for i := 0; i < 24; i++ {
		report = p.Gate()
		if report.OK() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	logger.Info("attach report", "verdict", report.String())
	if !report.OK() && !*relogTest {
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

	if *relogTest {
		// THE RELOG RITUAL, drilled in stages (the owner's ask: exit game and relog so
		// the corpse materializes IN TOWN — no naked suicide runs across the moor).
		// Menus read hardware-level input only: foreground + SendKeyReal/SendClickRealScreen.
		shot := func(path string) {
			if f, err := os.Create(path); err == nil {
				_ = png.Encode(f, gr.Screenshot())
				f.Close()
				logger.Info("relogtest: screenshot", "path", path)
			}
		}
		// Screenshot pixels are PHYSICAL client px (1920-wide); SendClickRealScreen wants
		// LOGICAL screen coords (the DPI-unaware process's 1536-wide desktop): divide by
		// the display scale, then add the logical client origin. Validated against the
		// gamble-refresh pair: shot(569,744) ↔ logical screen (455,583).
		toScreen := func(px, py int) (int, int) {
			return int(float64(px)/(*dpiScale)) + gr.WindowLeftX, int(float64(py)/(*dpiScale)) + gr.WindowTopY
		}
		win.SetForegroundWindow(hwnd)
		time.Sleep(400 * time.Millisecond)
		atMenu := !report.OK() // already OUT of the game (character select): skip the exit phase
		if atMenu && *playXY == "" {
			shot("logs/relog_mainmenu.png")
			logger.Info("relogtest: at the menu already — measure Play, rerun with -playxy")
			close(stop)
			return
		}
		if !atMenu {
			game.SendKeyReal(0x1B) // ESC — the pause menu
			time.Sleep(900 * time.Millisecond)
		}
		if !atMenu && *exitXY == "" {
			shot("logs/relog_pausemenu.png")
			game.SendKeyReal(0x1B) // close it again — touch nothing else
			logger.Info("relogtest: stage A done — measure Save+Exit from the screenshot, rerun with -exitxy")
			close(stop)
			return
		}
		if !atMenu {
			var ex, ey int
			fmt.Sscanf(*exitXY, "%d,%d", &ex, &ey)
			sx, sy := toScreen(ex, ey)
			logger.Info("relogtest: clicking Save+Exit", "shot", *exitXY, "screen", fmt.Sprintf("(%d,%d)", sx, sy))
			game.SendClickRealScreen(sx, sy)
			// Wait for the world to actually unload (position reads go garbage).
			gone := false
			for i := 0; i < 40; i++ {
				time.Sleep(500 * time.Millisecond)
				pos := gr.GetData().PlayerUnit.Position
				if pos.X == 0 && pos.Y == 0 {
					gone = true
					break
				}
			}
			logger.Info("relogtest: world unloaded", "gone", gone)
			time.Sleep(3 * time.Second) // let the main menu settle
			if *playXY == "" {
				shot("logs/relog_mainmenu.png")
				logger.Info("relogtest: stage B done — measure Play from the screenshot, rerun with -playxy (game is AT THE MENU)")
				close(stop)
				return
			}
		}
		var px2, py2 int
		fmt.Sscanf(*playXY, "%d,%d", &px2, &py2)
		psx, psy := toScreen(px2, py2)
		logger.Info("relogtest: clicking Play", "shot", *playXY, "screen", fmt.Sprintf("(%d,%d)", psx, psy))
		game.SendClickRealScreen(psx, psy)
		// Gate loop: wait for a sane in-game read.
		ok := false
		for i := 0; i < 60; i++ {
			time.Sleep(1 * time.Second)
			if p.Gate().OK() {
				ok = true
				break
			}
		}
		if !ok {
			shot("logs/relog_stuck.png")
			logger.Error("relogtest: never gated back in — screenshot saved")
			close(stop)
			return
		}
		time.Sleep(2 * time.Second)
		d := gr.GetData()
		logger.Info("relogtest: BACK IN GAME", "pos", fmt.Sprintf("(%d,%d)", d.PlayerUnit.Position.X, d.PlayerUnit.Position.Y),
			"area", int(d.PlayerUnit.Area), "seed", gr.MapSeed())
		logger.Info("relogtest: corpse", "found", d.Corpse.Found,
			"pos", fmt.Sprintf("(%d,%d)", d.Corpse.Position.X, d.Corpse.Position.Y))
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
			const hpID = 602 // "Herb" — the mod's HP potion, proven by purchase
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
		grid, _, err := gr.BuildLiveGridRooms()
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
			if cap.Contact != nil {
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
		grid, _, err := gr.BuildLiveGridRooms()
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
	led.Sink = func(o verbs.Outcome) {
		if o.Result != verbs.ResDone { // done outcomes are the quiet normal; exceptions speak
			logger.Info("outcome", "verb", o.Verb, "holder", o.Holder, "result", o.Result.String(), "ev", o.Evidence)
		}
	}
	// WARNING 6 (the 12:00 lesson): a swap mid-TRADE leaves the vendor panel up
	// for the successor — calibration then presses keys into a deaf panel and
	// every service fires into ghost windows. Readable vendor stock at attach
	// is PROOF a panel is open: ESC is safe (WARNING 4) and mandatory.
	for i := 0; i < 3; i++ {
		if len(gr.GetData().Inventory.ByLocation(item.LocationVendor)) == 0 {
			break
		}
		logger.Info("startup: inherited trade panel — closing before calibration")
		m.KeyLane().Press(0x1B)
		time.Sleep(600 * time.Millisecond)
	}
	// calibrate wraps probing + the owner's declared build: the owner KNOWS the char
	// (classic-bot law — kolbot/koolo configs declared skills; nobody inferred them).
	calibrate := func() combat.Capability {
		c := combat.Calibrate(logger, gr, hid, mem, []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8"})
		if *meleeKeyF != "" {
			c.Contact = &combat.Binding{Key: hid.GetASCIICode(*meleeKeyF)}
			logger.Info("capability: owner-declared melee key", "key", *meleeKeyF)
		}
		if *rangedKeyF != "" {
			c.Reach = &combat.Binding{Key: hid.GetASCIICode(*rangedKeyF)}
			logger.Info("capability: owner-declared ranged key", "key", *rangedKeyF)
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
	cap := calibrate()
	arb := &arbiter.Arbiter{}
	acts := map[string]activity.Activity{}
	road := []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
	mem.PutJSON("road.town.blood_moor_gate", memory.ScopeSeed, memory.Provenance{Source: "hand-piloted", Evidence: "2026-07-18, seed 466817790"}, road)
	// Seed the one door already proven — under the seed it was MEASURED in (the world
	// re-rolls per game; other seeds learn their own doors from the live oracle).
	if _, have := mem.Get(activity.BorderKey(466817790, area.RogueEncampment, area.BloodMoor)); !have {
		mem.PutJSON(activity.BorderKey(466817790, area.RogueEncampment, area.BloodMoor), memory.ScopeSeed,
			memory.Provenance{Source: "hand-piloted", Evidence: "road end, seed 466817790"}, road[len(road)-1])
	}
	// The goal shapes the itinerary: farm grinds the proven circuit's summit; campaign /
	// rampage marches the full act to Andariel's chamber. The arbiter owns every moment
	// either way — Fight and Loot preempt the march by class, which IS the rampage.
	legs := activity.FarmItinerary()
	if *goal == "campaign" || *goal == "rampage" {
		legs = activity.Act1Itinerary()
	}
	adv := activity.NewAdvance(legs)
	fight := activity.NewFight()
	fight.March = adv.MarchGoal // P-5.8: the door mouth is shot open
	for _, a := range []activity.Activity{&activity.Breakout{}, &activity.Stand{}, &activity.Flee{March: adv.MarchGoal}, activity.NewDodge(), &activity.Respawn{}, activity.NewRelog(), activity.NewReclaim(), fight, activity.NewLoot(), activity.NewFence(), activity.NewRestock(), activity.NewRepair(), activity.NewHeal(), activity.NewIdentify(), activity.NewEquip(), activity.NewSpend(), adv, activity.NewWithdraw(), &activity.Return{}, &activity.Travel{Road: road}, &activity.Explore{}} {
		acts[a.Name()] = a
	}

	var grid *game.Grid
	gridArea := -1
	var gridAt time.Time
	// THE CARTOGRAPHER: every area crossing she ever makes — by march, by wander, by
	// portal — records BOTH sides of the door as seed facts. Advance consumes them as
	// layer-1 border knowledge; the world's doors accumulate from use.
	lastArea := area.ID(0)
	var lastPos data.Position
	recordCrossing := func(s *percept.Snapshot) {
		if s.Me.Area != lastArea && lastArea != 0 && s.Me.Area != 0 {
			// Portals teleport (town↔field): only record when the two sides are near
			// each other — a real walked/clicked door, not a TP jump.
			if chebyshev(lastPos, s.Me.Pos) <= 40 {
				seed := gr.MapSeed()
				prov := memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("crossed %d->%d seed %d", int(lastArea), int(s.Me.Area), seed)}
				mem.PutJSON(activity.BorderKey(seed, lastArea, s.Me.Area), memory.ScopeSeed, prov, lastPos)
				mem.PutJSON(activity.BorderKey(seed, s.Me.Area, lastArea), memory.ScopeSeed, prov, s.Me.Pos)
				logger.Info("cartographer: door learned", "from", int(lastArea), "to", int(s.Me.Area),
					"at", fmt.Sprintf("(%d,%d)", lastPos.X, lastPos.Y))
			}
		}
		lastArea, lastPos = s.Me.Area, s.Me.Pos
	}
	// Regrid: mid-area grid regrowth for the leg-walker — rooms stream in as she walks,
	// and a grid built at the border knows nothing of the far exit.
	regrid := func() *game.Grid {
		if g, _, err := gr.BuildLiveGridRooms(); err == nil {
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
	var ring []*percept.Snapshot
	wasArmed := false
	wasDead := false
	wd := watchdog.New()
	cooldowns := map[string]time.Time{}
	wdCheckAt := time.Time{}
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	statusAt := time.Time{}
	sawInvalid := false
	wasFocused := true
	var trail []string // the last moments, for the owner's death reports
	var trailAt time.Time
	holderOf := func(a *arbiter.Arbiter) string {
		if g := a.Current(); g != nil {
			return g.Demand.Who
		}
		return "-"
	}
	lastRefocus := time.Time{}
	lastUnpause := time.Time{}
	for time.Now().Before(deadline) {
		// WARNING 7: an unfocused world is UNKNOWN — it froze before dawn (zero-gain
		// strides) and it RAN at 07:26 (131→55 blood across an 11-minute "pause"
		// while the executive stood by). Never aim input at an unfocused window;
		// instead REQUEST THE WINDOW BACK, rate-limited, only while the bot holds
		// the controls (the owner's F10 and their alt-tab are respected).
		if !m.GameFocused() {
			if wasFocused {
				logger.Info("executive: game unfocused — world state UNKNOWN, requesting focus back")
				wasFocused = false
			}
			if m.Engage.Engaged() && time.Since(lastRefocus) > 10*time.Second {
				game.ForceForegroundHWND(gr.HWND)
				lastRefocus = time.Now()
				logger.Info("executive: refocus requested", "verdict", m.GameFocused())
			}
			time.Sleep(400 * time.Millisecond)
			continue
		}
		if !wasFocused {
			logger.Info("executive: game refocused — resuming")
			wasFocused = true
			wd = watchdog.New() // pause-time position history would read as pathology
		}
		s := p.Capture()
		if !s.Valid || !m.Engage.Engaged() {
			sawInvalid = sawInvalid || !s.Valid
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// NEW GAME detection: a validity gap (relog, load screen) may mean a fresh
		// world — the seed re-rolls per game. FetchMapData no-ops when the seed is
		// unchanged; on a real change it re-fetches and the grid realigns below.
		if sawInvalid {
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
		recordCrossing(s)
		activity.ObserveBlood(s) // P-2.0: one blood truth for every Demand this cycle
		// Grid follows the area (the re-align, owned in one place) — and REGROWS on a
		// clock in the field: rooms stream in as she walks, and a grid built at the
		// border brands every unloaded room a wall. Loot/Reclaim/Fight journeys were
		// planning against that stale truth (Advance was the only one regridding);
		// fresh journeys now always start on current rooms.
		if int(s.Me.Area) != gridArea {
			if g, _, err := gr.BuildLiveGridRooms(); err == nil {
				grid, gridArea = g, int(s.Me.Area)
				gridAt = time.Now()
				logger.Info("executive: grid re-aligned", "area", gridArea)
			}
		} else if !s.Me.InTown && time.Since(gridAt) > 6*time.Second {
			if g, _, err := gr.BuildLiveGridRooms(); err == nil {
				grid = g
				gridAt = time.Now()
			}
		}
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
		if flightW != nil && time.Since(flightAt) >= time.Second {
			if b, err := json.Marshal(s); err == nil {
				flightW.Write(b)
				flightW.WriteByte('\n')
				flightW.Flush()
			}
			flightAt = time.Now()
		}
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
			fight.Recalibrated() // the audit follows the hands (P-7.1)
			wasArmed = s.Me.Armed
		}
		wasDead = deadNow

		var demands []arbiter.Demand
		for _, a := range acts {
			if d := a.Demand(s); d != nil {
				// The watchdog's prescriptions: a cooled activity may not win again
				// until its moment passes — survival and recovery are never cooled.
				if until, cooled := cooldowns[d.Who]; cooled && time.Now().Before(until) &&
					d.Class > arbiter.ClassRecover {
					continue
				}
				demands = append(demands, *d)
			}
		}
		grant, changed := arb.Decide(demands)

		// SELF-OBSERVATION (the owner's ask: "tell what the bot is up to, moments where
		// it's stuck, looping, thrashing — and unstuck itself"): the bot consumes its own
		// position and grant streams, names the pathology out loud, cools the culprit,
		// and breaks the physical state with one fresh-bearing stride.
		holderName := ""
		if grant != nil {
			holderName = grant.Demand.Who
		}
		// The dodge reflex blips sub-second by design — dodge↔fight alternation IS the
		// arrow dance, not thrash (the watchdog cooled fight mid-dance, measured 02:51).
		obsHolder := holderName
		if obsHolder == "dodge" {
			obsHolder = ""
		}
		wd.Observe(s.Me.Pos, obsHolder)
		if time.Since(wdCheckAt) > 5*time.Second {
			wdCheckAt = time.Now()
			// A volleying archer holds ground by design: fight + a target in bow range
			// vouches for stillness (Stuck/Orbit suppressed; Thrash still watched).
			stationaryOK := false
			if holderName == "fight" {
				for _, e := range s.Enemies {
					if chebyshev(s.Me.Pos, e.Pos) <= 28 {
						stationaryOK = true
						break
					}
				}
			}
			if v := wd.Check(holderName, stationaryOK); v.Pathology != watchdog.Healthy {
				logger.Warn("PATHOLOGY", "kind", v.Pathology.String(), "detail", v.Detail,
					"cooling", v.CoolWho, "for", time.Until(v.CoolUntil).Round(time.Second))
				mem.PutJSON("pathology.last", memory.ScopeGame,
					memory.Provenance{Source: "measured", Evidence: v.Detail},
					map[string]any{"kind": v.Pathology.String(), "at": time.Now().UnixMilli()})
				if v.CoolWho != "" {
					cooldowns[v.CoolWho] = v.CoolUntil
				}
				arb.Release()
				// One decisive displacement in a fresh bearing breaks the physical loop.
				esc := data.Position{X: s.Me.Pos.X - 20, Y: s.Me.Pos.Y - 20}
				if v.Pathology == watchdog.Stuck {
					o := verbs.Stride{To: esc, Hold: 2 * time.Second, MinGain: 3}.Do(m, gr, p, led, "watchdog")
					// WARNING 4: the pause menu is byte-blind and can arrive from
					// outside (a swap mid-relog, measured 08:41) — a REFUSED watchdog
					// stride in a safe town is its only shadow. The probe is SELF-
					// CONTAINED: RealEsc (menus are deaf to lane input), stride retest
					// in the SAME cycle, and a restoring RealEsc when the retest still
					// fails — the world is never left ambiguous for the next holder
					// (the 08:46 spend burned its belief clicking into a menu a blind
					// half-probe had just raised).
					if o.Result != verbs.ResDone && s.Me.InTown && !s.Me.CursorItem && time.Since(lastUnpause) > 30*time.Second {
						safeTown := true
						for _, e := range s.Enemies {
							if chebyshev(s.Me.Pos, e.Pos) <= 12 {
								safeTown = false
								break
							}
						}
						if safeTown {
							m.RealEsc()
							time.Sleep(500 * time.Millisecond)
							o2 := verbs.Stride{To: esc, Hold: 700 * time.Millisecond, MinGain: 1}.Do(m, gr, p, led, "watchdog/shadow")
							if o2.Result == verbs.ResDone {
								logger.Info("watchdog: ESC probe — a menu WAS up; the world moves again")
							} else {
								m.RealEsc() // raised on clear ground: restore before releasing
								logger.Info("watchdog: ESC probe — stride still refused; not the menu (fence?), state restored")
							}
							lastUnpause = time.Now()
						}
					}
				}
				continue
			}
		}
		if grant == nil {
			if time.Since(statusAt) > 5*time.Second {
				logger.Info("status: idle", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
					"area", int(s.Me.Area), "hp", s.Me.HPPct, "lvl", s.Me.Level)
				statusAt = time.Now()
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if changed {
			logger.Info("grant", "to", grant.Demand.Who, "class", grant.Demand.Class.String(),
				"urgency", fmt.Sprintf("%.2f", grant.Demand.Urgency))
		}
		act := acts[grant.Demand.Who]
		v := act.Step(&activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: grid, Cap: &cap, Snap: s, SwapKey: hid.GetASCIICode(*swapKey), InvKey: hid.GetASCIICode(*invKeyF), Regrid: regrid, Mem: mem})
		if v != activity.Running {
			logger.Info("verdict", "activity", grant.Demand.Who, "verdict", map[activity.Verdict]string{activity.Done: "done", activity.Abandoned: "abandoned"}[v])
			arb.Release()
		}
		if time.Since(statusAt) > 10*time.Second {
			logger.Info("status", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
				"area", int(s.Me.Area), "hp", s.Me.HPPct, "lvl", s.Me.Level, "gold", s.Me.Gold,
				"weapon", s.Me.WeaponKind, "arrows", s.Me.Arrows, "holder", grant.Demand.Who)
			statusAt = time.Now()
		}
	}
	close(stop)
	logger.Info("azbot done")
}
