// azsuper — the unattended supervisor. It keeps one thing true: D2R is
// running, a character is in the world, and azbot is driving it.
//
//   - D2R gone (crash) or its crash reporter up: (kill the reporter and the
//     hung game,) relaunch D2R with the mod.
//   - D2R up but not in the world: skip intros (Space) until the character
//     screen, then click Play (the selected character = the last played).
//   - In the world with no azbot: heal input (farmbot -fixinput) and start
//     azbot for -seconds; when it ends, start the next run.
//
// Stop: create logs\super.stop (azbot is then left to its own WindDown).
// Owner rule kept: azbot is never killed here — only started.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	d2rExe   = flag.String("d2r", `C:\Program Files (x86)\Diablo II Resurrected\D2R.exe`, "D2R executable")
	d2rArgs  = flag.String("args", `-mod D2RMM -txt`, "D2R arguments (the mod launch line)")
	seconds  = flag.Int("seconds", 3600, "azbot -seconds per run")
	tag      = flag.String("tag", "s", "log tag prefix: logs\\run_<date><tag><n>.out")
	goal     = flag.String("goal", "campaign", "azbot -goal")
	extra    = flag.String("extra", "", "extra azbot flags")
	dpiScale = flag.Float64("dpiscale", 1.25, "display scale (screen points are physical px)")
	playX    = flag.Int("playx", 922, "character screen Play/Normal button x (physical px)")
	check    = flag.Bool("check", false, "print the world/menu reading once and exit (no input)")
	playY    = flag.Int("playy", 822, "character screen Play/Normal button y (physical px)")
)

func logf(format string, a ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, a...)
	fmt.Println(line)
	if f, err := os.OpenFile(filepath.Join("logs", "super.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, time.Now().Format("2006-01-02 ")+line)
		f.Close()
	}
}

func pidOf(exe string) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			return e.ProcessID
		}
	}
	return 0
}

func killPID(pid uint32) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	windows.TerminateProcess(h, 1)
	windows.CloseHandle(h)
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

func launchD2R() {
	args := strings.Fields(*d2rArgs)
	if len(args) > 0 && args[len(args)-1] == "-txt" {
		args = append(args, "") // the mod launch line ends in -txt ""
	}
	cmd := exec.Command(*d2rExe, args...)
	cmd.Dir = filepath.Dir(*d2rExe)
	if err := cmd.Start(); err != nil {
		logf("relaunch FAILED: %v", err)
		return
	}
	logf("relaunched D2R pid=%d", cmd.Process.Pid)
	cmd.Process.Release()
}

func startAzbot(n int) {
	fix := exec.Command(`.\farmbot.exe`, "-fixinput")
	_ = fix.Run()
	name := filepath.Join("logs", fmt.Sprintf("run_%s%s%d.out", time.Now().Format("2006-01-02"), *tag, n))
	out, err := os.Create(name)
	if err != nil {
		logf("azbot log: %v", err)
		return
	}
	errf, _ := os.Create(name + ".err")
	args := []string{"-goal", *goal, "-seconds", fmt.Sprint(*seconds)}
	args = append(args, strings.Fields(*extra)...)
	cmd := exec.Command(filepath.Join("build", "azbot.exe"), args...)
	cmd.Stdout, cmd.Stderr = out, errf
	cmd.Env = append(os.Environ(), "AZBOT_DELIBERATE=1", "AZBOT_JANITOR=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		logf("azbot start FAILED: %v", err)
		return
	}
	logf("azbot started pid=%d -> %s", cmd.Process.Pid, name)
	go func() { cmd.Wait(); out.Close(); errf.Close() }()
}

// inWorld reports whether a character is loaded (and whether the reader
// could attach at all).
func inWorld(pid uint32) (world, charScreen, ok bool) {
	proc, err := memory.NewProcessForPID(pid)
	if err != nil {
		return false, false, false
	}
	defer proc.Close()
	gr := memory.NewGameReader(proc)
	// IsIngame's static offset is stale on this build (reads false in the
	// world); a loaded player unit with a position is the bot's own test.
	pu := gr.GetData().PlayerUnit
	return pu.Address != 0 && pu.Position.X > 0 && pu.Area > 0, gr.IsInCharacterSelectionScreen(), true
}

func clickPhysical(hwnd win.HWND, x, y int) {
	game.ForceForegroundHWND(hwnd)
	time.Sleep(300 * time.Millisecond)
	p := win.POINT{}
	win.ClientToScreen(hwnd, &p)
	game.SendClickRealScreen(int(float64(x) / *dpiScale)+int(p.X), int(float64(y) / *dpiScale)+int(p.Y))
}

func main() {
	flag.Parse()
	_ = slog.Default()
	if *check {
		pid := pidOf("D2R.exe")
		w, c, ok := inWorld(pid)
		fmt.Println("d2r", pid, "azbot", pidOf("azbot.exe"), "world", w, "charScreen", c, "ok", ok)
		return
	}
	os.MkdirAll("logs", 0o755)
	os.Remove(filepath.Join("logs", "super.stop"))
	logf("supervisor up: seconds=%d tag=%s", *seconds, *tag)
	runs, relaunches := 0, 0
	var menuSince time.Time
	for {
		if _, err := os.Stat(filepath.Join("logs", "super.stop")); err == nil {
			logf("super.stop found: supervisor exits (azbot, if running, winds down on its own)")
			return
		}
		// A crash reporter up means the game is dead even if its process lingers.
		if rep := pidOf("BlizzardError.exe"); rep != 0 {
			logf("crash reporter up (pid %d): clearing it and the hung game", rep)
			killPID(rep)
			if p := pidOf("D2R.exe"); p != 0 {
				killPID(p)
			}
			time.Sleep(3 * time.Second)
		}
		pid := pidOf("D2R.exe")
		if pid == 0 {
			if az := pidOf("azbot.exe"); az != 0 {
				killPID(az) // driving nothing: it would only log errors
				logf("D2R gone: stopped the orphaned azbot pid=%d", az)
			}
			relaunches++
			logf("D2R not running: relaunch #%d", relaunches)
			launchD2R()
			menuSince = time.Now()
			time.Sleep(20 * time.Second)
			continue
		}
		world, charScreen, ok := inWorld(pid)
		if !ok {
			time.Sleep(3 * time.Second)
			continue
		}
		if world {
			menuSince = time.Time{}
			if pidOf("azbot.exe") == 0 {
				runs++
				startAzbot(runs)
				time.Sleep(15 * time.Second)
			}
			time.Sleep(3 * time.Second)
			continue
		}
		// Not in the world. azbot's own relog walks the menus while it runs;
		// act only when nothing drives the game.
		if pidOf("azbot.exe") != 0 {
			time.Sleep(3 * time.Second)
			continue
		}
		if menuSince.IsZero() {
			menuSince = time.Now()
		}
		hwnd := findHWND(pid)
		if hwnd == 0 {
			time.Sleep(3 * time.Second)
			continue
		}
		switch {
		case charScreen || time.Since(menuSince) > 60*time.Second:
			logf("character screen (read=%v, %s in menus): clicking Play", charScreen, time.Since(menuSince).Round(time.Second))
			clickPhysical(hwnd, *playX, *playY)
			time.Sleep(8 * time.Second)
		default:
			game.ForceForegroundHWND(hwnd)
			time.Sleep(200 * time.Millisecond)
			game.SendKeyReal(0x20) // Space: skip intro videos / "press any key"
			time.Sleep(2 * time.Second)
		}
	}
}
