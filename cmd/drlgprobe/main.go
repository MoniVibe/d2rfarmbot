// drlgprobe — a PURE READER: dumps the raw Room2 layout of the current level
// to locate the preset-unit list (objects, tiles/warps) on this D2R build.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/d2go/pkg/utils"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

func findD2RPID() uint32 {
	snap, _ := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "d2r.exe") {
			return e.ProcessID
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
	rooms := flag.Int("rooms", 3, "rooms to dump raw")
	mapcheck := flag.Bool("mapcheck", false, "fetch koolo-map with the live seed and print this level exits")
	actlevels := flag.Bool("actlevels", false, "find the act first-level pointer")
	roomlist := flag.Bool("roomlist", false, "current level rooms in subtiles: ROOM x y w h")
	rtiles := flag.Bool("rtiles", false, "dump Room2+0x78 room-tile structs")
	seeds := flag.Bool("seeds", false, "list candidate map-seed values")
	tiles := flag.Bool("tiles", false, "hunt the Room2 room-tile (warp) list")
	live := flag.Bool("live", false, "ReadLiveLevels summary + object/tile presets")
	all := flag.Bool("all", false, "with -live: monster presets too")
	levels := flag.Bool("levels", false, "probe the DrlgLevel struct for the next-level link")
	presets := flag.Bool("presets", false, "walk every room's preset list")
	flag.Parse()
	if err := config.Load(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	pid := findD2RPID()
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), quiet)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	p := gr.Process
	u64 := func(a uintptr) uintptr { return uintptr(p.ReadUInt(a, memory.Uint64)) }
	u32 := func(a uintptr) uint { return p.ReadUInt(a, memory.Uint32) }
	d := gr.GetData()
	fmt.Printf("area=%d pos=%v\n", d.PlayerUnit.Area, d.PlayerUnit.Position)
	g, err := gr.ReadCurrentRoomGraph()
	if err != nil {
		fmt.Println("graph:", err)
		os.Exit(1)
	}
	fmt.Printf("level=%d rooms=%d external=%d\n", g.LevelID, len(g.Rooms), len(g.External))
	if *mapcheck {
		if err := gr.FetchMapData(); err != nil {
			fmt.Println("fetch:", err)
			os.Exit(1)
		}
		dd := gr.GetData()
		ad, ok := dd.Areas[dd.PlayerUnit.Area]
		fmt.Println("seed", gr.MapSeed(), "area", int(dd.PlayerUnit.Area), "mapped", ok)
		if ok && ad.Grid != nil {
			fmt.Println("map frame", ad.Grid.OffsetX, ad.Grid.OffsetY, ad.Grid.Width, ad.Grid.Height)
		}
		if f, err := gr.ReadLiveLevelFrame(); err == nil {
			fmt.Println("live frame", f.OriginX, f.OriginY, f.SizeX, f.SizeY)
		}
		for _, e := range ad.AdjacentLevels {
			fmt.Println("  exit to", int(e.Area), "at", e.Position, "entrance", e.IsEntrance, "dist", cheb(dd.PlayerUnit.Position, e.Position))
		}
		for _, o := range ad.Objects {
			if o.IsWaypoint() {
				fmt.Println("  waypoint at", o.Position)
			}
		}
		return
	}
	if *actlevels {
		act := u64(d.PlayerUnit.Address + 0x20)
		misc := u64(act + 0x78)
		for _, base := range []struct {
			tag string
			p   uintptr
		}{{"act", act}, {"misc", misc}, {"act+08", u64(act + 0x08)}, {"act+18", u64(act + 0x18)}, {"act+48", u64(act + 0x48)}, {"act+70", u64(act + 0x70)}, {"act+98", u64(act + 0x98)}, {"act+a0", u64(act + 0xa0)}, {"act+b0", u64(act + 0xb0)}} {
			for off := uintptr(0); off < 0x2000; off += 8 {
				q := u64(base.p + off)
				if q < 0x10000 || q > 0x7fffffffffff {
					continue
				}
				no := u32(q + 0x1F8)
				if no == 0 || no > 200 {
					continue
				}
				n, ids := 0, []uint{}
				for l := q; l != 0 && n < 200; l = u64(l + 0x1B8) {
					n++
					ids = append(ids, u32(l+0x1F8))
				}
				if n > 1 {
					fmt.Println(base.tag, "off", off, "chain", n, ids)
				}
			}
		}
		return
	}
	if *roomlist {
		for _, r := range g.Rooms {
			fmt.Printf("ROOM %d %d %d %d\n", r.Rect.X*5, r.Rect.Y*5, r.Rect.W*5, r.Rect.H*5)
		}
		return
	}
	if *rtiles {
		for addr, r := range g.Rooms {
			for t, k := u64(addr+0x78), 0; t != 0 && k < 8; k++ {
				dest := u64(t)
				dl := u64(dest + 0x90)
				fmt.Printf("room(%d,%d) tile %x: dest room(%d,%d) level %d | q:", r.Rect.X, r.Rect.Y, t, u32(dest+0x60), u32(dest+0x64), u32(dl+0x1F8))
				for i := uintptr(0); i < 0x28; i += 8 {
					fmt.Printf(" %x", u64(t+i))
				}
				fmt.Println()
				nx := u64(t + 0x08)
				if nx == t {
					break
				}
				t = nx
			}
		}
		return
	}
	if *seeds {
		// Candidate seed values: every distinct nonzero u32 in the Act struct
		// and around the ActMisc seed-hash block. The caller tests each one
		// against the live level frames with koolo-map.
		act := u64(d.PlayerUnit.Address + 0x20)
		misc := u64(act + 0x70)
		seen := map[uint]bool{}
		emit := func(tag string, base uintptr, from, to uintptr) {
			for off := from; off < to; off += 4 {
				v := u32(base + off)
				if v == 0 || v < 0x10000 || seen[v] {
					continue
				}
				seen[v] = true
				fmt.Printf("%s+%03x %d\n", tag, off, v)
			}
		}
		emit("act", act, 0, 0x100)
		emit("misc", misc, 0x800, 0x900)
		// Hash-derived: MapAssist's reversal of EndSeedHash, for every field
		// that might be the end hash (the init hash sits 0x28 before it).
		for off := uintptr(0x700); off < 0xA00; off += 4 {
			end := u32(misc + off)
			if end == 0 {
				continue
			}
			if sd, ok := utils.GetMapSeed(u32(misc+off-0x28), end); ok && !seen[sd] {
				seen[sd] = true
				fmt.Printf("hash+%03x %d\n", off, sd)
			}
		}
		return
	}
	if *tiles {
		// For every room of every chained level: which pointer fields lead
		// (directly, or through a small struct's first 3 qwords) to a Room2
		// of ANOTHER level? The room-tile list is the one that does in rooms
		// holding a tile preset.
		isRoom := func(q uintptr) (uint, bool) {
			if q < 0x10000 || q > 0x7fffffffffff {
				return 0, false
			}
			l := u64(q + 0x90)
			if l < 0x10000 || l > 0x7fffffffffff {
				return 0, false
			}
			no := u32(l + 0x1F8)
			w, h := u32(q+0x68), u32(q+0x6C)
			return no, no > 0 && no < 200 && w > 0 && w < 200 && h > 0 && h < 200
		}
		cur, _ := gr.ReadCurrentRoomGraph()
		var lvl uintptr
		for a := range cur.Rooms {
			lvl = u64(a + 0x90)
			break
		}
		hits := map[string]int{}
		for l, k := lvl, 0; l != 0 && k < 10; l, k = u64(l+0x1B8), k+1 {
			own := u32(l + 0x1F8)
			for r := u64(l + 0x10); r != 0; r = u64(r + 0x48) {
				for off := uintptr(0); off < 0xB0; off += 8 {
					q := u64(r + off)
					if no, ok := isRoom(q); ok && no != own {
						hits[fmt.Sprint("direct +", off)]++
						fmt.Println("level", own, "room", u32(r+0x60), u32(r+0x64), "off", off, "-> room of level", no)
					}
					if q < 0x10000 || q > 0x7fffffffffff {
						continue
					}
					for j := uintptr(0); j < 0x18; j += 8 {
						if no, ok := isRoom(u64(q + j)); ok && no != own {
							hits[fmt.Sprint("via +", off, " [", j, "]")]++
							fmt.Println("level", own, "room", u32(r+0x60), u32(r+0x64), "off", off, "field", j, "-> room of level", no)
						}
					}
				}
			}
		}
		fmt.Println("HITS", hits)
		return
	}
	if *live {
		lv, err := gr.ReadLiveLevels()
		if err != nil {
			fmt.Println("live:", err)
			os.Exit(1)
		}
		for id, l := range lv {
			fmt.Println("LEVEL", int(id), "rooms", len(l.Rooms), "presets", len(l.Presets), "origin", l.Origin, "size", l.Size)
			for _, e := range l.Exits {
				fmt.Println("   EXIT to", int(e.Area), "at", e.Position)
			}
			for _, p := range l.Presets {
				if p.Type != game.PresetMonster || *all {
					fmt.Println("   type", p.Type, "txt", p.Txt, "at", p.Pos)
				}
			}
		}
		return
	}
	if *levels {
		var lvl uintptr
		for a := range g.Rooms {
			lvl = u64(a + 0x90)
			break
		}
		fmt.Println("level", lvl, "no", u32(lvl+0x1F8))
		for l, k := lvl, 0; l != 0 && k < 200; l, k = u64(l+0x1B8), k+1 {
			rooms, pres := 0, 0
			for r := u64(l + 0x10); r != 0 && rooms < 2000; r = u64(r + 0x48) {
				rooms++
				for p := u64(r + 0x98); p != 0 && pres < 20000; p = u64(p + 0x10) {
					pres++
				}
			}
			fmt.Println(" chain", k, "level", u32(l+0x1F8), "rooms", rooms, "presets", pres, "pos", u32(l+0x24), u32(l+0x28), "size", u32(l+0x2C), u32(l+0x30))
		}
		for off := uintptr(0); off < 0x230; off += 8 {
			q := u64(lvl + off)
			if q < 0x10000 || q > 0x7fffffffffff {
				continue
			}
			no := u32(q + 0x1F8)
			r2 := u64(q + 0x10)
			fmt.Println(" off", off, "->", q, "as-level no", no, "room2", r2)
		}
		return
	}
	if *presets {
		for addr, r := range g.Rooms {
			for pr, k := u64(addr+0x98), 0; pr != 0 && k < 64; pr, k = u64(pr+0x10), k+1 {
				fmt.Printf("room(%d,%d %dx%d) preset %x:", r.Rect.X, r.Rect.Y, r.Rect.W, r.Rect.H, pr)
				for i := uintptr(0); i < 0x40; i += 4 {
					fmt.Printf(" %d", u32(pr+i))
				}
				fmt.Println()
			}
		}
		return
	}
	n := 0
	for addr, r := range g.Rooms {
		if n >= *rooms {
			break
		}
		n++
		fmt.Printf("\nROOM %x rect=%+v room1=%x\n", addr, r.Rect, r.Room1Ptr)
		for off := uintptr(0); off < 0x100; off += 8 {
			v := u64(addr + off)
			fmt.Printf("  +%02x %016x  u32 %d %d\n", off, v, u32(addr+off), u32(addr+off+4))
		}
		for off := uintptr(0x70); off < 0x100; off += 8 {
			q := u64(addr + off)
			if q < 0x10000 || q > 0x7fffffffffff {
				continue
			}
			fmt.Printf("  ptr +%02x -> %x :", off, q)
			for i := uintptr(0); i < 0x40; i += 4 {
				fmt.Printf(" %d", u32(q+i))
			}
			fmt.Println()
		}
	}
}

func cheb(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return max(dx, dy)
}
