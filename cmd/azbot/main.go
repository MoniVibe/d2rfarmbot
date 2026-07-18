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

	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/sentinel"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

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
	killKey := flag.String("killswitch", "pause", "hotkey to toggle bot control (pause|f9..f12|scrolllock|single letter). Disengage heals all input patches — the human owns Diablo instantly.")
	belt := flag.String("belt", "1,2,3,4", "belt column keys")
	drinkAt := flag.Int("drinkat", 55, "sentinel drinks at or below this HP%")
	memDir := flag.String("memdir", "logs/azmem", "memory store directory (WAL)")
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
