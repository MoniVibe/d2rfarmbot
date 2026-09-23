// look — a PURE READER that translates D2R memory into a compact, human/agent
// readable situation report: self, belt, nearby monsters, ground loot, exits,
// a walkability map around the player, and the bot's latest log decisions.
// No input, no patches — safe beside a live azbot and independent of focus.
//
// Usage: look.exe [-r 20] [-log path.out] [-n 8]
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
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

func cheb(a, b data.Position) int {
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

func monsterState(m uint32) string {
	switch mode.NpcMode(m) {
	case mode.NpcDeath:
		return "dying"
	case mode.NpcDead:
		return "dead"
	case mode.NpcGettingHit:
		return "hit"
	case mode.NpcAttacking1, mode.NpcAttacking2, mode.NpcCastingSpell:
		return "attacking"
	case mode.NpcWalking, mode.NpcRunning:
		return "moving"
	case mode.NpcStandingStill:
		return "idle"
	}
	return fmt.Sprintf("mode%d", m)
}

var qualName = map[item.Quality]string{1: "low", 2: "normal", 3: "superior", 4: "magic", 5: "set", 6: "rare", 7: "unique", 8: "crafted"}

func newestLog(dir string) string {
	best, bt := "", int64(0)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".out") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().UnixNano() > bt {
			best, bt = filepath.Join(dir, e.Name()), fi.ModTime().UnixNano()
		}
	}
	return best
}

func tail(path string, n int, keep func(string) bool) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := sc.Text(); keep(l) {
			out = append(out, l)
			if len(out) > n {
				out = out[1:]
			}
		}
	}
	return out
}

func main() {
	radius := flag.Int("r", 20, "map radius in tiles (0 = no map)")
	logPath := flag.String("log", "", "azbot log to tail (default: newest logs/*.out)")
	nLog := flag.Int("n", 8, "decision lines to show from the log")
	flag.Parse()

	if err := config.Load(); err != nil {
		fmt.Println("config load failed:", err)
		os.Exit(1)
	}
	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("D2R.exe not running")
		os.Exit(1)
	}
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), quiet)
	if err != nil {
		fmt.Println("reader failed:", err)
		os.Exit(1)
	}
	d := gr.GetData()
	pu := d.PlayerUnit
	me := pu.Position

	// ---- self ----
	ar := pu.Area
	fmt.Printf("== %s  area=%d %q  pos=(%d,%d)  town=%v\n", pu.Name, int(ar), ar.Area().Name, me.X, me.Y, ar.IsTown())
	fmt.Printf("   hp%%=%d mp%%=%d  mode=%d\n", pu.HPPercent(), pu.MPPercent(), int(pu.Mode))
	var belt []string
	for _, it := range d.Inventory.Belt.Items {
		belt = append(belt, fmt.Sprintf("%d:%d", it.Position.X, int(it.ID)))
	}
	fmt.Printf("   belt[%d] slot:id = %s\n", len(belt), strings.Join(belt, " "))

	// ---- monsters (nearest first, living before dead) ----
	type mref struct {
		d    int
		line string
		dead bool
	}
	var ms []mref
	for _, m := range d.Monsters {
		dd := cheb(me, m.Position)
		if dd > 40 {
			continue
		}
		st := monsterState(uint32(m.Mode))
		ms = append(ms, mref{dd, fmt.Sprintf("   d=%2d  unit=%-5d npc=%-4d %-9s at(%d,%d) off(%+d,%+d)",
			dd, m.UnitID, int(m.Name), st, m.Position.X, m.Position.Y, m.Position.X-me.X, m.Position.Y-me.Y),
			st == "dead" || st == "dying"})
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].dead != ms[j].dead {
			return !ms[i].dead
		}
		return ms[i].d < ms[j].d
	})
	alive := 0
	for _, m := range ms {
		if !m.dead {
			alive++
		}
	}
	fmt.Printf("-- monsters within 40: %d alive, %d dead\n", alive, len(ms)-alive)
	for i, m := range ms {
		if i >= 12 || m.dead {
			break
		}
		fmt.Println(m.line)
	}

	// ---- ground items ----
	var gi []string
	for _, it := range d.Inventory.ByLocation(item.LocationGround) {
		dd := cheb(me, it.Position)
		if dd > 40 {
			continue
		}
		gi = append(gi, fmt.Sprintf("   d=%2d  unit=%-5d id=%-4d %-8s base=%q at(%d,%d)",
			dd, it.UnitID, int(it.ID), qualName[it.Quality], it.Desc().Name, it.Position.X, it.Position.Y))
	}
	sort.Strings(gi)
	fmt.Printf("-- ground items within 40: %d\n", len(gi))
	for i, l := range gi {
		if i >= 10 {
			break
		}
		fmt.Println(l)
	}

	// ---- exits / portals / waypoints ----
	fmt.Printf("-- exits:")
	for _, e := range d.Entrances {
		fmt.Printf("  [%s d=%d]", e.Name, cheb(me, e.Position))
	}
	for _, o := range d.Objects {
		if o.IsWaypoint() || o.IsPortal() || o.IsRedPortal() {
			fmt.Printf("  [%s d=%d]", o.Desc().Name, cheb(me, o.Position))
		}
	}
	fmt.Println()

	// ---- map: world axes (x right, y down; the screen is this rotated 45°) ----
	if *radius > 0 {
		g, _, err := gr.BuildLiveGrid()
		if err != nil || g == nil {
			fmt.Println("-- map: live grid unavailable:", err)
		} else {
			marks := map[[2]int]byte{}
			for _, it := range d.Inventory.ByLocation(item.LocationGround) {
				marks[[2]int{it.Position.X, it.Position.Y}] = '!'
			}
			for _, e := range d.Entrances {
				marks[[2]int{e.Position.X, e.Position.Y}] = 'D'
			}
			for _, o := range d.Objects {
				if o.IsWaypoint() {
					marks[[2]int{o.Position.X, o.Position.Y}] = 'W'
				} else if o.IsPortal() || o.IsRedPortal() {
					marks[[2]int{o.Position.X, o.Position.Y}] = 'O'
				}
			}
			for _, m := range d.Monsters {
				st := monsterState(uint32(m.Mode))
				if st != "dead" && st != "dying" {
					marks[[2]int{m.Position.X, m.Position.Y}] = 'm'
				}
			}
			marks[[2]int{me.X, me.Y}] = '@'
			fmt.Printf("-- map r=%d (@ me, m monster, ! item, D door, W waypoint, O portal, # blocked, ? unknown)\n", *radius)
			for y := me.Y - *radius; y <= me.Y+*radius; y++ {
				var sb strings.Builder
				sb.WriteString("   ")
				for x := me.X - *radius; x <= me.X+*radius; x++ {
					if c, ok := marks[[2]int{x, y}]; ok {
						sb.WriteByte(c)
						continue
					}
					rp := g.RelativePosition(data.Position{X: x, Y: y})
					switch {
					case rp.X < 0 || rp.Y < 0 || rp.X >= g.Width || rp.Y >= g.Height:
						sb.WriteByte('?')
					case g.IsWalkable(data.Position{X: x, Y: y}):
						sb.WriteByte('.')
					default:
						sb.WriteByte('#')
					}
				}
				fmt.Println(sb.String())
			}
		}
	}

	// ---- the bot's recent decisions ----
	lp := *logPath
	if lp == "" {
		lp = newestLog("logs")
	}
	if lp != "" && *nLog > 0 {
		fmt.Printf("-- bot log (%s), last %d decisions:\n", filepath.Base(lp), *nLog)
		keep := func(l string) bool {
			return strings.Contains(l, "msg=grant") || strings.Contains(l, "msg=outcome") ||
				strings.Contains(l, "PATHOLOGY") || strings.Contains(l, "sentinel") || strings.Contains(l, "STALL")
		}
		for _, l := range tail(lp, *nLog, keep) {
			if i := strings.Index(l, "level="); i >= 0 && len(l) > 19 {
				l = l[11:19] + " " + l[i:]
			}
			if len(l) > 200 {
				l = l[:200]
			}
			fmt.Println("  ", l)
		}
	}
}
