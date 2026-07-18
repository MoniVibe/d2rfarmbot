// azbot — the intelligent successor to farmbot. Design: docs/AZBOT_DESIGN.md.
// This entrypoint implements M0 (attach & epistemics gate) + M1 (perceive & survive)
// + the owner's kill-switch. Activities, verbs, and the arbiter arrive in M2+.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/sentinel"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
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
	killKey := flag.String("killswitch", "f12", "hotkey to toggle bot control (f9..f12|pause|scrolllock|single letter). Disengage heals all input patches — the human owns Diablo instantly.")
	belt := flag.String("belt", "1,2,3,4", "belt column keys")
	drinkAt := flag.Int("drinkat", 55, "sentinel drinks at or below this HP%")
	memDir := flag.String("memdir", "logs/azmem", "memory store directory (WAL)")
	roadTest := flag.Bool("roadtest", false, "M2 soak: walk the measured town road out and back on Stride verbs, print the outcome histogram, exit")
	jTest := flag.String("jtest", "", "M3 soak: journey to world 'x,y' on the live grid via the Journey authority, print the verdict, exit")
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

	// ---- M1 executive skeleton: perceive on cadence, report status. Activities in M2+. ----
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	statusAt := time.Time{}
	for time.Now().Before(deadline) {
		s := p.Capture()
		if time.Since(statusAt) > 5*time.Second {
			if s.Valid {
				logger.Info("status", "pos", fmt.Sprintf("(%d,%d)", s.Me.Pos.X, s.Me.Pos.Y),
					"area", int(s.Me.Area), "hp", s.Me.HPPct, "lvl", s.Me.Level,
					"gold", s.Me.Gold, "menu", s.MenuOpen, "engaged", m.Engage.Engaged())
			} else {
				logger.Warn("status: perception gap (load screen / not in game)")
			}
			statusAt = time.Now()
		}
		time.Sleep(80 * time.Millisecond) // Executive cadence; motor owns all other timing
	}
	close(stop)
	logger.Info("azbot done")
}
