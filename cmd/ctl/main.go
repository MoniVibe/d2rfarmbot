// ctl — a manual controller for the live game, so an operator (human or agent) can
// set up and probe situations without the owner's hands. ONE action per call, then
// the input patches are healed. Run only with azbot stopped (they share the cursor).
//
//	ctl key <k> [-real]        press a key (posted; -real = OS scancode, foregrounds D2R)
//	ctl hover <x,y> [-space S] point the in-game cursor, report what the game hovers
//	ctl click <x,y> [-space S] [-right]
//	ctl state                  panels open, hover, focus
//
// Spaces: world (logical client px, calibrated world projection — what verbs use),
// panel (logical client px at display scale — shop/inventory/waypoint panels),
// panel1 (legacy scale-1 panel clicks).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
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

func usage() {
	fmt.Println("usage: ctl key <k> [-real] | hover <x,y> [-space world|panel|panel1] | click <x,y> [-space ..] [-right] | state")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	space := fs.String("space", "world", "world | panel | panel1")
	real := fs.Bool("real", false, "key: OS-level scancode press (foregrounds D2R)")
	right := fs.Bool("right", false, "click: right button")
	dpi := fs.Float64("dpiscale", 1.25, "display scale")
	var arg string
	rest := os.Args[2:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		arg, rest = rest[0], rest[1:]
	}
	fs.Parse(rest)

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
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), quiet)
	if err != nil {
		fmt.Println("reader failed:", err)
		return
	}
	client := fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY)
	if buf, err := os.ReadFile("logs/aimcal.json"); err == nil {
		var cal game.AimCal
		if json.Unmarshal(buf, &cal) == nil && cal.Client == client {
			game.SetAimCalibration(cal)
		}
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

	aim := func(x, y int) {
		switch *space {
		case "panel":
			hid.AimPanelScaled(x, y, *dpi)
			hid.MouseMoveClient(x, y)
		case "panel1":
			hid.AimPanelScaled(x, y, 1)
			hid.MouseMoveClient(x, y)
		default:
			hid.MovePointer(x, y)
		}
	}
	report := func() {
		time.Sleep(80 * time.Millisecond)
		d := gr.GetData()
		hd := d.HoverData
		fmt.Printf("hover: %v unit=%d type=%d  focused=%v\n", hd.IsHovered, hd.UnitID, hd.UnitType, hid.GameFocused())
		for _, l := range []item.LocationType{item.LocationVendor, item.LocationInventory, item.LocationStash, item.LocationBelt, item.LocationEquipped} {
			for _, it := range d.Inventory.ByLocation(l) {
				if it.IsHovered {
					fmt.Printf("hovered item: %s id=%d %q grid(%d,%d)\n", l, int(it.ID), it.Desc().Name, it.Position.X, it.Position.Y)
				}
			}
		}
		ps := percept.New(gr).Capture()
		fmt.Printf("ui: MenuOpen(0xF4)=%v QuitMenu=%v\n", ps.MenuOpen, ps.QuitMenu)
	}
	parseXY := func() (int, int) {
		var x, y int
		if _, err := fmt.Sscanf(arg, "%d,%d", &x, &y); err != nil {
			usage()
		}
		return x, y
	}

	switch cmd {
	case "key":
		if arg == "" {
			usage()
		}
		vk := hid.GetASCIICode(arg)
		if *real {
			hid.FocusGame()
			time.Sleep(200 * time.Millisecond)
			game.SendKeyRealScan(uint16(vk))
			fmt.Printf("real key %q sent (vk=0x%02X)\n", arg, vk)
		} else {
			hid.PressKey(vk)
			fmt.Printf("posted key %q (vk=0x%02X)\n", arg, vk)
		}
		time.Sleep(400 * time.Millisecond)
		report()
	case "hover":
		x, y := parseXY()
		aim(x, y)
		time.Sleep(60 * time.Millisecond)
		aim(x, y)
		report()
	case "click":
		x, y := parseXY()
		aim(x, y)
		time.Sleep(120 * time.Millisecond)
		if *space == "world" {
			if *right {
				hid.Click(game.RightButton, x, y)
			} else {
				hid.Click(game.LeftButton, x, y)
			}
		} else {
			hid.LeftClickNoMoveClient(x, y)
		}
		fmt.Printf("clicked %s (%d,%d) right=%v\n", *space, x, y, *right)
		time.Sleep(300 * time.Millisecond)
		report()
	case "realclick":
		// SHOT coords (a pixel read off shot.exe's image): OS-level click, game
		// foregrounded — the only click menus/panels honor (MENUS DEMAND TRUE FOREGROUND).
		sx, sy := parseXY()
		img := gr.Screenshot().Bounds()
		phys := float64(gr.GameAreaSizeX) * *dpi // physical client width
		k := phys / float64(img.Dx())             // shot px -> physical client px
		lx := int(float64(sx)*k / *dpi) + gr.WindowLeftX
		ly := int(float64(sy)*k / *dpi) + gr.WindowTopY
		hid.FocusGame()
		time.Sleep(250 * time.Millisecond)
		if *right {
			game.SendRightClickRealScreen(lx, ly)
		} else {
			game.SendClickRealScreen(lx, ly)
		}
		fmt.Printf("real click shot(%d,%d) -> screen(%d,%d) k=%.3f right=%v\n", sx, sy, lx, ly, k, *right)
		time.Sleep(400 * time.Millisecond)
		report()
	case "state":
		report()
	default:
		usage()
	}
}
