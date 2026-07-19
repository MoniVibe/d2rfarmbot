// shot — a PURE READER: attach to D2R, save one screenshot, exit. No injector, no
// HID, no input patches, no exit-heal — safe to run beside a live azbot (the probe
// discipline forbids anything that touches input functions; this touches nothing).
// Usage: shot.exe [outpath.png]   (default logs/shot.png)
package main

import (
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"unsafe"

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

func main() {
	out := "logs/shot.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := config.Load(); err != nil {
		fmt.Println("config load failed:", err)
		os.Exit(1)
	}
	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("D2R.exe not running")
		os.Exit(1)
	}
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), logger)
	if err != nil {
		fmt.Println("reader failed:", err)
		os.Exit(1)
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Println("create failed:", err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, gr.Screenshot()); err != nil {
		fmt.Println("encode failed:", err)
		os.Exit(1)
	}
	fmt.Println("saved", out)
}
