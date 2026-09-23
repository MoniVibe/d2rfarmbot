// aimcal — measure the world-aim projection for THIS client. Sweeps the (injected)
// cursor over a box around the player, and at every probe that the GAME reports
// hovering an enemy, pairs the cursor pixel with that enemy's tile offset at that
// moment. Least squares then fits:  px = a*(dx-dy) + cx,  py = b*(dx+dy) + cy
// in LOGICAL client pixels (the space verbs compute bx/by in).
// RUN ONLY WITH azbot STOPPED — it drives the cursor. Heals input on exit.
// Usage: aimcal.exe [-half 300] [-step 20] [-dpiscale 1.25]
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

	"github.com/hectorgimenez/d2go/pkg/data"
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

type sample struct{ px, py, u, v float64 }

// fit returns slope and intercept of y = k*x + c by least squares.
func fit(xs, ys []float64) (k, c, rms float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, sy / n, 0
	}
	k = (n*sxy - sx*sy) / den
	c = (sy - k*sx) / n
	for i := range xs {
		r := ys[i] - (k*xs[i] + c)
		rms += r * r
	}
	return k, c, sqrt(rms / n)
}

func sqrt(x float64) float64 {
	z := x
	if z == 0 {
		return 0
	}
	for i := 0; i < 30; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func main() {
	half := flag.Int("half", 300, "half-size of the swept box in logical px around the client center")
	step := flag.Int("step", 20, "sweep step in logical px")
	dpi := flag.Float64("dpiscale", 1.25, "display scale")
	out := flag.String("out", "logs/aimcal.json", "where to save the calibration azbot loads")
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
	game.SetWorldScale(*dpi) // sweep in LOGICAL px: world = logical * dpi = physical client px
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), quiet)
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

	cx0, cy0 := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	fmt.Printf("client %dx%d logical, center (%d,%d), sweeping ±%d step %d\n",
		gr.GameAreaSizeX, gr.GameAreaSizeY, cx0, cy0, *half, *step)
	var ss []sample
	hits := map[data.UnitID]int{}
	t0 := time.Now()
	for y := cy0 - *half; y <= cy0+*half; y += *step {
		for x := cx0 - *half; x <= cx0+*half; x += *step {
			if x < 10 || y < 10 || x > gr.GameAreaSizeX-10 || y > gr.GameAreaSizeY-10 {
				continue
			}
			hid.AimPhysical(x, y)
			time.Sleep(20 * time.Millisecond)
			d := gr.GetData()
			if hp := d.PlayerUnit.HPPercent(); hp > 0 && hp < 50 {
				fmt.Printf("ABORT: hp %d%% — nobody is drinking while azbot is stopped\n", hp)
				return
			}
			hd := d.HoverData
			if !hd.IsHovered || (hd.UnitType != 1 && hd.UnitType != 4) {
				continue
			}
			me := d.PlayerUnit.Position
			if hd.UnitType == 4 { // ground item: a hover target at a known tile
				for _, it := range d.Inventory.ByLocation(item.LocationGround) {
					if it.UnitID == hd.UnitID {
						dx, dy := float64(it.Position.X-me.X), float64(it.Position.Y-me.Y)
						ss = append(ss, sample{float64(x), float64(y), dx - dy, dx + dy})
						hits[hd.UnitID]++
						break
					}
				}
				continue
			}
			for _, m := range d.Monsters.Enemies() {
				if m.UnitID == hd.UnitID {
					dx, dy := float64(m.Position.X-me.X), float64(m.Position.Y-me.Y)
					ss = append(ss, sample{float64(x), float64(y), dx - dy, dx + dy})
					hits[m.UnitID]++
					break
				}
			}
		}
		hid.SpoofActivate()
	}
	fmt.Printf("sweep %.0fs: %d hover samples on %d distinct enemies\n", time.Since(t0).Seconds(), len(ss), len(hits))
	if len(ss) < 8 {
		fmt.Println("too few samples — stand near several monsters and re-run")
		return
	}
	var us, vs, pxs, pys []float64
	for _, s := range ss {
		us, vs, pxs, pys = append(us, s.u), append(vs, s.v), append(pxs, s.px), append(pys, s.py)
	}
	a, cx, rx := fit(us, pxs)
	b, cy, ry := fit(vs, pys)
	fmt.Printf("fit X: px = %.2f*(dx-dy) + %.1f   rms %.1f   (code assumes 19.8 and %d)\n", a, cx, rx, cx0)
	fmt.Printf("fit Y: py = %.2f*(dx+dy) + %.1f   rms %.1f   (code assumes 9.9 and %d)\n", b, cy, ry, cy0)
	fmt.Printf("=> worldscale for the 19.8/9.9 constants: X %.3f  Y %.3f  (dpi %.2f)\n", a/19.8**dpi, b/9.9**dpi, *dpi)
	fmt.Printf("=> origin offset (logical px): dX %+.1f  dY %+.1f\n", cx-float64(cx0), cy-float64(cy0))
	cal := game.AimCal{Client: fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY),
		KX: a / 19.8, KY: b / 9.9, OX: cx - float64(cx0), OY: cy - float64(cy0)}
	buf, _ := json.MarshalIndent(cal, "", "  ")
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Println("write failed:", err)
		return
	}
	fmt.Println("saved", *out, "— azbot loads it at startup when the client size matches")
}
