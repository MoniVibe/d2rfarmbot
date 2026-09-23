// panelprobe — find the cursor convention the trade/inventory panels hover in.
// With a panel OPEN (and azbot stopped), sweeps the left 60% of the client under
// several conventions and counts game-confirmed hovers of panel items (the per-item
// IsHovered flag and the global HoverData). The winning convention is the one the
// bot's shop locator must use. Heals input on exit.
// Usage: panelprobe.exe [-step 24] [-dpiscale 1.25]
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data/item"
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
	step := flag.Int("step", 24, "sweep step in the convention's own px")
	dpi := flag.Float64("dpiscale", 1.25, "display scale")
	flag.Parse()
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := config.Load(); err != nil {
		fmt.Println("config load failed:", err)
		return
	}
	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("D2R.exe not running")
		return
	}
	game.SetPhysicalScale(*dpi)
	game.SetWorldScale(*dpi)
	hwnd := findHWND(pid)
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, hwnd, quiet)
	if err != nil {
		fmt.Println("reader failed:", err)
		return
	}
	gi, err := game.InjectorInit(quiet, pid)
	if err != nil {
		fmt.Println("injector init failed:", err)
		return
	}
	if err := gi.Load(); err != nil {
		fmt.Println("injector load failed:", err)
		return
	}
	defer func() { gi.Unload(); gi.Close() }()
	hid := game.NewHID(gr, gi)
	hid.SpoofActivate()

	panelItems := func() []item.LocationType {
		return []item.LocationType{item.LocationVendor, item.LocationInventory, item.LocationStash, item.LocationSharedStash}
	}
	d0 := gr.GetData()
	for _, l := range panelItems() {
		fmt.Printf("items at %s: %d\n", l, len(d0.Inventory.ByLocation(l)))
	}
	w, h := gr.GameAreaSizeX, gr.GameAreaSizeY
	type conv struct {
		name string
		aim  func(x, y int)
	}
	convs := []conv{
		{"panel scale=dpi (AimPanelScaled dpi)", func(x, y int) { hid.AimPanelScaled(x, y, *dpi); hid.MouseMoveClient(x, y) }},
		{"panel scale=1 (legacy UIClick)", func(x, y int) { hid.AimPanelScaled(x, y, 1); hid.MouseMoveClient(x, y) }},
		{"world aim (AimPhysical, uncalibrated)", func(x, y int) { hid.AimPhysical(x, y) }},
		{"MovePointer (world + GetCursorPos)", func(x, y int) { hid.MovePointer(x, y) }},
	}
	for _, c := range convs {
		flagHits, globalHits := 0, 0
		first := ""
		t0 := time.Now()
		for y := h / 8; y <= h*9/10; y += *step {
			for x := w / 40; x <= w*6/10; x += *step {
				c.aim(x, y)
				time.Sleep(30 * time.Millisecond)
				d := gr.GetData()
				if d.HoverData.IsHovered {
					globalHits++
				}
				for _, l := range panelItems() {
					for _, it := range d.Inventory.ByLocation(l) {
						if it.IsHovered {
							flagHits++
							if first == "" {
								first = fmt.Sprintf("%s id=%d grid(%d,%d) at (%d,%d)", l, int(it.ID), it.Position.X, it.Position.Y, x, y)
							}
						}
					}
				}
			}
			hid.SpoofActivate()
		}
		fmt.Printf("%-42s %4.0fs  item-flag hits %4d  global-hover hits %4d  first: %s\n",
			c.name, time.Since(t0).Seconds(), flagHits, globalHits, first)
	}
}
