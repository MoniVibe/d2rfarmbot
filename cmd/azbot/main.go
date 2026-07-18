// azbot — the intelligent successor to farmbot. Design: docs/AZBOT_DESIGN.md.
// This entrypoint implements M0 (attach & epistemics gate) + M1 (perceive & survive)
// + the owner's kill-switch. Activities, verbs, and the arbiter arrive in M2+.
package main

import (
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
	roadTest := flag.Bool("roadtest", false, "M2 soak: walk the measured town road out and back on Stride verbs, print the outcome histogram, exit")
	jTest := flag.String("jtest", "", "M3 soak: journey to world x,y on the live grid via the Journey authority, print the verdict, exit")
	fightTest := flag.Bool("fighttest", false, "M4 soak: calibrate capability, cross to Blood Moor, hover-strike nearest enemies with evidence, exit")
	portalTest := flag.Bool("portaltest", false, "manual harness: find the nearest portal in the snapshot, approach if far, click through with the EnterPortal verb, report every state change, exit")
	charsiTest := flag.Bool("charsitest", false, "manual harness: walk to Charsi, open TRADE (menu byte + Down/Enter), screenshot the shop for repair-button calibration; with -repairxy also click it and report the gold delta, exit")
	repairXY := flag.String("repairxy", "", "client x,y of the repair button (measured from logs/charsi_shop.png)")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	flag.Parse()

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
	defer gi.Unload()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("signal — healing D2R input and exiting")
		gi.Unload()
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

	// ---- M4 fight test: calibrate capability, cross to Blood Moor, strike with evidence ----
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
				if v, ok := gr.GetData().PlayerUnit.Stats.FindStat(stat.Gold, 0); ok {
					g0 = v.Value
				}
				uiClick(rx, ry)
				time.Sleep(600 * time.Millisecond)
				g1 := g0
				if v, ok := gr.GetData().PlayerUnit.Stats.FindStat(stat.Gold, 0); ok {
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
		if cap.Melee == nil {
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
			if cap.Melee != nil {
				key = cap.Melee.Key
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
	cap := combat.Calibrate(logger, gr, hid, mem, []string{"f1", "f2", "f3", "f4"})
	arb := &arbiter.Arbiter{}
	acts := map[string]activity.Activity{}
	road := []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}
	mem.PutJSON("road.town.blood_moor_gate", memory.ScopeSeed, memory.Provenance{Source: "hand-piloted", Evidence: "2026-07-18, seed 466817790"}, road)
	for _, a := range []activity.Activity{&activity.Breakout{}, &activity.Flee{}, &activity.Respawn{}, activity.NewFight(), activity.NewLoot(), &activity.Travel{Road: road}, &activity.Explore{}} {
		acts[a.Name()] = a
	}

	var grid *game.Grid
	gridArea := -1
	wasArmed := false
	wasDead := false
	wd := watchdog.New()
	cooldowns := map[string]time.Time{}
	wdCheckAt := time.Time{}
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	statusAt := time.Time{}
	for time.Now().Before(deadline) {
		s := p.Capture()
		if !s.Valid || !m.Engage.Engaged() {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// Grid follows the area (the re-align, owned in one place).
		if int(s.Me.Area) != gridArea {
			if g, _, err := gr.BuildLiveGridRooms(); err == nil {
				grid, gridArea = g, int(s.Me.Area)
				logger.Info("executive: grid re-aligned", "area", gridArea)
			}
		}
		// Self-model events: armed flip OR back-from-death → recalibrate capability.
		deadNow := s.Me.HPPct <= 0
		if (s.Me.Armed != wasArmed || (wasDead && !deadNow)) && !deadNow {
			logger.Info("executive: self-model event — recalibrating", "armed", s.Me.Armed, "revived", wasDead)
			c2 := combat.Calibrate(logger, gr, hid, mem, []string{"f1", "f2", "f3", "f4"})
			cap = c2
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
		wd.Observe(s.Me.Pos, holderName)
		if time.Since(wdCheckAt) > 5*time.Second {
			wdCheckAt = time.Now()
			if v := wd.Check(holderName); v.Pathology != watchdog.Healthy {
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
					verbs.Stride{To: esc, Hold: 2 * time.Second, MinGain: 3}.Do(m, gr, p, led, "watchdog")
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
		v := act.Step(&activity.Ctx{M: m, GR: gr, P: p, Led: led, Grid: grid, Cap: &cap, Snap: s, SwapKey: hid.GetASCIICode(*swapKey)})
		if v != activity.Running {
			logger.Info("verdict", "activity", grant.Demand.Who, "verdict", map[activity.Verdict]string{activity.Done: "done", activity.Abandoned: "abandoned"}[v])
			arb.Release()
		}
		if time.Since(statusAt) > 10*time.Second {
			logger.Info("status", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
				"area", int(s.Me.Area), "hp", s.Me.HPPct, "lvl", s.Me.Level, "gold", s.Me.Gold,
				"armed", s.Me.Armed, "holder", grant.Demand.Who)
			statusAt = time.Now()
		}
	}
	close(stop)
	logger.Info("azbot done")
}
