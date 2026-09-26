// stridecal — MEASURE force-move strides before anyone tunes them. Drives short
// planned strides through the exact path azbot's nav follower uses
// (a planned verbs.Stride -> motor.StrideEdge -> posted WM_KEYDOWN + GetKeyState
// override, released by motor.MoveStop), across holds and 8 world directions, and
// records what the game actually did with each one.
//
// Why: recorded runs show planned 100-300ms pulses reading gain=0 again and again
// while a 2s stride moved ~30 tiles, ~95% of them with D2R unfocused ("posted
// input"). This tool separates hold length, focus state and heading so the cause is
// measured, not guessed.
//
// RUN ONLY WITH azbot STOPPED (they share the cursor and the force-move key). Safest
// in town with -town. No sentinel runs here: nobody drinks. It refuses to start with a
// living monster within 30 tiles, a menu/panel up, or HP under 50%, and aborts on any
// HP drop or a monster arriving. Hard runtime cap (-budget, default 4m30s). Every key
// and input patch is released/healed on exit, Ctrl+C included.
//
// Usage (from the azbot directory, so logs/ and logs/aimcal.json resolve):
//
//	stridecal.exe -town                  # in town, whatever focus the game has now
//	stridecal.exe -town -mode focused    # foreground D2R before every trial (takes focus!)
//	stridecal.exe -town -mode posted     # D2R must stay in the background (run from a console in front)
//	  -holds 80,120,200,300,500,800,1200,2000  -reps 2  -dist 10  -budget 4m30s
//	  -move e  -dpiscale 1.25  -aimcal logs/aimcal.json  -spoof=true
//
// -spoof (default true, azbot's -fakefocus default): post the WM_ACTIVATE family once
// at start and via motor.HoverReady before each trial (azbot's own <=1.5s cadence).
// Note azbot's stride path itself never calls HoverReady — only hover verbs do — so a
// -spoof=false run in posted mode is the "navigation only, no recent spoof" condition.
//
// Per trial: start position, Stride.Do (the verb's own hold loop: it polls every 120ms,
// so the real hold is quantised — held_ms records it), then 300ms settle and an end
// read. A 40ms background sampler records when the position first changed and when the
// player mode first entered Walking/Running/WalkingInTown (ms from the Do call; the
// key-down edge lands ~90ms in, after StrideEdge's 60ms separation + aim pulse).
// Note Stride stops "arrived-early" within 2 tiles of its target, so with -dist 10 the
// 1200/2000ms holds are cut short (~8 tiles); raise -dist to measure them uncut.
// Between trials it strides back toward the start (logged as kind=return rows).
//
// Output: logs/stridecal_<unix>.csv (one row per stride) and a summary on stdout:
// per mode x hold -> n, median gain, % zero-gain, median heading error, median real
// hold, median first-motion latency.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
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

// headingErr: degrees between the intended world vector (start->target) and the
// travelled one (start->end). NaN when nothing moved.
func headingErr(start, target, end data.Position) float64 {
	ix, iy := float64(target.X-start.X), float64(target.Y-start.Y)
	ax, ay := float64(end.X-start.X), float64(end.Y-start.Y)
	if ax == 0 && ay == 0 {
		return math.NaN()
	}
	d := math.Atan2(ay, ax) - math.Atan2(iy, ix)
	for d > math.Pi {
		d -= 2 * math.Pi
	}
	for d < -math.Pi {
		d += 2 * math.Pi
	}
	return math.Abs(d) * 180 / math.Pi
}

func moving(md mode.PlayerMode) bool {
	return md == mode.Walking || md == mode.Running || md == mode.WalkingInTown
}

// sampler watches one stride from a second goroutine (the sentinel reads concurrently
// with the executive in azbot, so GetData is safe to share).
type sampler struct {
	firstPosMS  int64 // -1 = never moved while sampled
	firstModeMS int64 // -1 = mode never entered walk/run
	modes       []string
	n           int
	minHP       int
}

func sample(gr *game.MemoryReader, start data.Position, t0 time.Time, stop <-chan struct{}, out *sampler, wg *sync.WaitGroup) {
	defer wg.Done()
	out.firstPosMS, out.firstModeMS, out.minHP = -1, -1, 101
	last := ""
	for {
		select {
		case <-stop:
			return
		default:
		}
		d := gr.GetData()
		ms := time.Since(t0).Milliseconds()
		out.n++
		if pos := d.PlayerUnit.Position; pos.X != 0 || pos.Y != 0 {
			if out.firstPosMS < 0 && pos != start {
				out.firstPosMS = ms
			}
			if hp := d.PlayerUnit.HPPercent(); hp < out.minHP {
				out.minHP = hp
			}
			if out.firstModeMS < 0 && moving(d.PlayerUnit.Mode) {
				out.firstModeMS = ms
			}
			if m := strconv.Itoa(int(d.PlayerUnit.Mode)); m != last {
				out.modes = append(out.modes, m)
				last = m
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
}

type row struct {
	kind                string
	rep                 int
	mode                string
	focStart, focEnd    bool
	spoofed             bool
	dir                 string
	dirDeg              int
	holdReq, held       int64
	start, target, end  data.Position
	gainRelease, gain   int
	headErr             float64
	firstPos, firstMode int64
	modes               string
	samples             int
	result, evidence    string
}

const csvHeader = "unix_ms,kind,rep,mode,focused_start,focused_end,spoofed,dir,dir_deg,hold_req_ms,held_ms," +
	"start_x,start_y,target_x,target_y,end_x,end_y,gain_at_release,gain,heading_err_deg," +
	"first_pos_move_ms,first_mode_move_ms,player_modes,samples,result,evidence"

func (r row) csv() string {
	he := ""
	if !math.IsNaN(r.headErr) {
		he = fmt.Sprintf("%.1f", r.headErr)
	}
	return fmt.Sprintf("%d,%s,%d,%s,%v,%v,%v,%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%s,%d,%d,%s,%d,%s,%q",
		time.Now().UnixMilli(), r.kind, r.rep, r.mode, r.focStart, r.focEnd, r.spoofed, r.dir, r.dirDeg,
		r.holdReq, r.held, r.start.X, r.start.Y, r.target.X, r.target.Y, r.end.X, r.end.Y,
		r.gainRelease, r.gain, he, r.firstPos, r.firstMode, r.modes, r.samples, r.result, r.evidence)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func fmtMed(v float64, unit string) string {
	if math.IsNaN(v) {
		return "-"
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}

func main() {
	dpi := flag.Float64("dpiscale", 1.25, "display scale (azbot -dpiscale)")
	moveKeyF := flag.String("move", "e", "Force Move key (azbot -move)")
	aimCalPath := flag.String("aimcal", "logs/aimcal.json", "world-projection calibration (applied when its client size matches)")
	allowTown := flag.Bool("town", false, "allow running in town (safest; otherwise town is refused)")
	modeF := flag.String("mode", "auto", "auto (record focus as found) | focused (foreground D2R before each trial) | posted (require D2R in the background)")
	spoof := flag.Bool("spoof", true, "post WM_ACTIVATE-family spoofs (azbot -fakefocus): once at start, then motor.HoverReady before each trial")
	holdsF := flag.String("holds", "80,120,200,300,500,800,1200,2000", "hold durations in ms")
	reps := flag.Int("reps", 2, "repetitions per direction x hold")
	dist := flag.Int("dist", 10, "target distance in tiles (Stride stops within 2 tiles of it)")
	reach := flag.Float64("reach", 0, "cap the cursor's distance from the player in logical px (0 = carrot box edge, what azbot uses); run once per value to measure travel vs reach")
	budget := flag.Duration("budget", 4*time.Minute+30*time.Second, "hard runtime cap for the trial loop")
	flag.Parse()

	if *modeF != "auto" && *modeF != "focused" && *modeF != "posted" {
		fmt.Println("-mode must be auto, focused or posted")
		os.Exit(2)
	}
	var holds []int
	for _, h := range strings.Split(*holdsF, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && v > 0 && v <= 3000 {
			holds = append(holds, v)
		}
	}
	if len(holds) == 0 || *reps < 1 || *dist < 3 {
		fmt.Println("bad -holds / -reps / -dist")
		os.Exit(2)
	}
	if *budget > 5*time.Minute {
		*budget = 5 * time.Minute
	}

	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := config.Load(); err != nil {
		fmt.Println("config load failed:", err)
		return
	}
	pid := findD2RPID()
	if pid == 0 {
		fmt.Println("PRECONDITION attached: FAIL — D2R.exe not running")
		return
	}
	game.SetPhysicalScale(*dpi)
	game.SetWorldScale(*dpi) // azbot's default: -worldscale 0 = the display scale
	gr, err := game.NewGameReader(config.Characters["Main"], "Main", pid, findHWND(pid), quiet)
	if err != nil {
		fmt.Println("PRECONDITION attached: FAIL — reader:", err)
		return
	}
	client := fmt.Sprintf("%dx%d", gr.GameAreaSizeX, gr.GameAreaSizeY)
	aimNote := "none (raw constants)"
	if buf, err := os.ReadFile(*aimCalPath); err == nil {
		var cal game.AimCal
		if json.Unmarshal(buf, &cal) == nil && cal.Client == client {
			game.SetAimCalibration(cal)
			aimNote = "loaded " + *aimCalPath
		} else {
			aimNote = "IGNORED (client size mismatch)"
		}
	}
	fmt.Printf("PRECONDITION attached: ok  pid=%d client=%s aimcal=%s\n", pid, client, aimNote)

	// ---- read-only preconditions, before any patch is loaded ----
	p := percept.New(gr)
	rep := p.Gate()
	fmt.Printf("PRECONDITION in-game: %v  (%s)\n", rep.OK(), rep.String())
	if !rep.OK() {
		fmt.Println("REFUSED: not in game (or paused/death screen)")
		return
	}
	s0 := p.Capture()
	if !s0.Valid {
		fmt.Println("PRECONDITION position: FAIL — REFUSED")
		return
	}
	home := s0.Me.Pos
	fmt.Printf("PRECONDITION position: ok (%d,%d) area=%d town=%v hp=%d%%\n", home.X, home.Y, int(s0.Me.Area), s0.Me.InTown, s0.Me.HPPct)
	nearest := func(s *percept.Snapshot) (int, int) {
		n, best := 0, 1<<30
		for _, e := range s.Enemies {
			if d := cheb(s.Me.Pos, e.Pos); d <= 30 {
				n++
				if d < best {
					best = d
				}
			}
		}
		return n, best
	}
	if n, d := nearest(s0); n > 0 {
		fmt.Printf("PRECONDITION no monsters within 30: FAIL — %d living, nearest %d tiles. REFUSED\n", n, d)
		return
	}
	fmt.Println("PRECONDITION no monsters within 30: ok")
	if s0.Me.InTown && !*allowTown {
		fmt.Println("PRECONDITION not in town: FAIL — pass -town to allow (town is the safest place). REFUSED")
		return
	}
	fmt.Printf("PRECONDITION town: in_town=%v allowed=%v\n", s0.Me.InTown, *allowTown)
	if s0.MenuOpen || s0.QuitMenu {
		fmt.Println("PRECONDITION no menu/panel: FAIL — close it first. REFUSED")
		return
	}
	if s0.Me.HPPct < 50 {
		fmt.Printf("PRECONDITION hp>=50%%: FAIL (%d%%) — no sentinel drinks here. REFUSED\n", s0.Me.HPPct)
		return
	}
	var grid *game.Grid
	if g, _, err := gr.BuildLiveGridRooms(); err == nil {
		grid = g
		fmt.Printf("live grid: origin (%d,%d) %dx%d — directions with a wall in the first 4 tiles are skipped\n", g.OffsetX, g.OffsetY, g.Width, g.Height)
	} else {
		fmt.Println("live grid unavailable — every direction is tried and recorded:", err)
	}
	walkable := func(pp data.Position) bool {
		if grid == nil {
			return true
		}
		rp := grid.RelativePosition(pp)
		if rp.X < 0 || rp.Y < 0 || rp.X >= grid.Width || rp.Y >= grid.Height {
			return true // unknown ground: the fused map's optimistic convention
		}
		return grid.IsWalkable(pp)
	}

	hwnd := findHWND(pid)
	gameFocused := func() bool { return win.GetForegroundWindow() == hwnd }
	if *modeF == "posted" && gameFocused() {
		fmt.Println("REFUSED: -mode posted but D2R has the foreground — put another window (this console) in front and re-run")
		return
	}

	// ---- actuation: the same injector/HID/motor stack azbot builds ----
	gi, err := game.InjectorInit(quiet, pid)
	if err != nil {
		fmt.Println("injector init failed:", err)
		return
	}
	if err := gi.Load(); err != nil {
		fmt.Println("injector load failed:", err)
		return
	}
	hid := game.NewHID(gr, gi)
	m := motor.New(quiet, hid, gi, hid.GetASCIICode(*moveKeyF))
	m.SetPanelScale(*dpi)
	motor.FakeFocus = *spoof
	var cleaned atomic.Bool
	cleanup := func() {
		if !cleaned.CompareAndSwap(false, true) {
			return
		}
		m.MoveStop() // key up + GetKeyState/GetAsyncKeyState overrides restored
		hid.ModifierAmnesty()
		game.SendModifierUpReal()
		gi.Unload() // heal every input patch
		gi.Close()
		fmt.Println("input released and healed")
	}
	defer cleanup()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nsignal — releasing force-move and healing D2R input")
		cleanup()
		os.Exit(1)
	}()
	hid.ModifierAmnesty()
	if *spoof {
		hid.SpoofActivate()
		time.Sleep(40 * time.Millisecond)
	}

	if err := os.MkdirAll("logs", 0o755); err != nil {
		fmt.Println("logs dir:", err)
		return
	}
	csvPath := filepath.Join("logs", fmt.Sprintf("stridecal_%d.csv", time.Now().Unix()))
	f, err := os.Create(csvPath)
	if err != nil {
		fmt.Println("csv create failed:", err)
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Fprintln(w, csvHeader)
	fmt.Println("csv:", csvPath)

	led := verbs.NewLedger(64)
	baseHP := s0.Me.HPPct
	abort := ""

	// stride runs ONE planned verbs.Stride under a RoleSteer lease (the dead-man
	// timer backs the key release) with the sampler alongside, then settles and reads.
	stride := func(kind string, repN int, dir string, dirDeg int, to data.Position, holdMS int) (row, bool) {
		r := row{kind: kind, rep: repN, dir: dir, dirDeg: dirDeg, holdReq: int64(holdMS), target: to, headErr: math.NaN()}
		if *modeF == "focused" && !gameFocused() {
			hid.FocusGame()
			time.Sleep(150 * time.Millisecond)
		}
		if *spoof {
			m.HoverReady()             // azbot's cadence: re-posts activation at most every 1.5s when unfocused
			r.spoofed = !gameFocused() // spoof armed AND needed (unfocused)
		}
		s := p.Capture()
		if !s.Valid {
			abort = "invalid read before stride"
			return r, false
		}
		if n, d := nearest(s); n > 0 {
			abort = fmt.Sprintf("monster arrived: %d within 30, nearest %d", n, d)
			return r, false
		}
		if baseHP-s.Me.HPPct >= 5 {
			abort = fmt.Sprintf("hp dropped %d%% -> %d%%", baseHP, s.Me.HPPct)
			return r, false
		}
		r.start = s.Me.Pos
		r.focStart = gameFocused()
		lease, ok := m.ReserveCursor(motor.RoleSteer, "stridecal", time.Duration(holdMS)*time.Millisecond+3*time.Second)
		if !ok {
			abort = "cursor lease refused (motor disengaged?)"
			return r, false
		}
		var smp sampler
		var wg sync.WaitGroup
		stop := make(chan struct{})
		t0 := time.Now()
		wg.Add(1)
		go sample(gr, r.start, t0, stop, &smp, &wg)
		o := verbs.Stride{To: to, Hold: time.Duration(holdMS) * time.Millisecond, MinGain: 1, Reach: *reach}.Do(m, gr, p, led, "stridecal/"+kind)
		lease.Release() // MoveStop again; redundant by design
		time.Sleep(300 * time.Millisecond)
		close(stop)
		wg.Wait()
		e := p.Capture()
		r.focEnd = gameFocused()
		switch {
		case r.focStart && r.focEnd:
			r.mode = "focused"
		case !r.focStart && !r.focEnd:
			r.mode = "posted"
		default:
			r.mode = "mixed"
		}
		r.held = o.HeldMS
		r.result = o.Result.String()
		r.evidence = o.Evidence
		var fx, fy, tx, ty int
		if _, err := fmt.Sscanf(o.Evidence, "from=(%d,%d) to=(%d,%d) gain=%d", &fx, &fy, &tx, &ty, &r.gainRelease); err != nil {
			r.gainRelease = -1
		}
		r.firstPos, r.firstMode, r.samples = smp.firstPosMS, smp.firstModeMS, smp.n
		r.modes = strings.Join(smp.modes, ">")
		if e.Valid {
			r.end = e.Me.Pos
			r.gain = cheb(r.start, r.end)
			r.headErr = headingErr(r.start, to, r.end)
		} else {
			r.gain = -1
		}
		fmt.Fprintln(w, r.csv())
		w.Flush()
		if smp.minHP <= 100 && baseHP-smp.minHP >= 5 {
			abort = fmt.Sprintf("hp dropped during stride %d%% -> %d%%", baseHP, smp.minHP)
			return r, false
		}
		if !e.Valid {
			abort = "invalid read after stride"
			return r, false
		}
		return r, true
	}

	goHome := func() bool {
		for i := 0; i < 2; i++ {
			s := p.Capture()
			if !s.Valid {
				return false
			}
			d := cheb(s.Me.Pos, home)
			if d <= 2 {
				return true
			}
			h := d*130 + 150
			if h < 300 {
				h = 300
			}
			if h > 2000 {
				h = 2000
			}
			if _, ok := stride("return", 0, "home", -1, home, h); !ok {
				return false
			}
		}
		return true
	}

	type dirSpec struct {
		name string
		deg  int
	}
	dirs := []dirSpec{{"+x", 0}, {"+x+y", 45}, {"+y", 90}, {"-x+y", 135}, {"-x", 180}, {"-x-y", 225}, {"-y", 270}, {"+x-y", 315}}
	var trials []row
	skipped := map[string]bool{}
	t0 := time.Now()
	fmt.Printf("running %d dirs x %d holds x %d reps (budget %s, mode=%s, spoof=%v, dist=%d, reach=%.0fpx)\n",
		len(dirs), len(holds), *reps, *budget, *modeF, *spoof, *dist, *reach)
loop:
	for rp := 1; rp <= *reps; rp++ {
		for _, dsp := range dirs {
			a := float64(dsp.deg) * math.Pi / 180
			fx, fy := math.Cos(a), math.Sin(a)
			for _, h := range holds {
				if time.Since(t0) > *budget {
					abort = "budget exhausted (summary covers the trials run)"
					break loop
				}
				if !goHome() {
					if abort == "" {
						abort = "could not read/return between trials"
					}
					break loop
				}
				s := p.Capture()
				if !s.Valid {
					abort = "invalid read"
					break loop
				}
				me := s.Me.Pos
				open := true
				for i := 1; i <= 4; i++ {
					if !walkable(data.Position{X: me.X + int(math.Round(fx*float64(i))), Y: me.Y + int(math.Round(fy*float64(i)))}) {
						open = false
						break
					}
				}
				if !open {
					if !skipped[dsp.name] {
						fmt.Printf("  skip dir %s: wall within 4 tiles of (%d,%d)\n", dsp.name, me.X, me.Y)
					}
					skipped[dsp.name] = true
					continue
				}
				to := data.Position{X: me.X + int(math.Round(fx*float64(*dist))), Y: me.Y + int(math.Round(fy*float64(*dist)))}
				r, ok := stride("trial", rp, dsp.name, dsp.deg, to, h)
				if r.mode != "" {
					trials = append(trials, r)
					fmt.Printf("  rep%d %-5s hold=%4dms held=%4dms %-7s gain=%2d head=%s firstmove=%dms modes=%s\n",
						rp, dsp.name, h, r.held, r.mode, r.gain, fmtMed(r.headErr, "°"), r.firstPos, r.modes)
				}
				if !ok {
					break loop
				}
			}
		}
	}
	m.MoveStop()
	if abort != "" {
		fmt.Println("STOPPED:", abort)
	}
	if abort == "" || strings.HasPrefix(abort, "budget") {
		goHome()
	}

	// ---- summary: per mode x hold ----
	type key struct {
		mode string
		hold int64
	}
	groups := map[key][]row{}
	for _, r := range trials {
		if r.gain < 0 {
			continue
		}
		k := key{r.mode, r.holdReq}
		groups[k] = append(groups[k], r)
	}
	var keys []key
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].mode != keys[j].mode {
			return keys[i].mode < keys[j].mode
		}
		return keys[i].hold < keys[j].hold
	})
	fmt.Printf("\n%d trials in %.0fs  (csv %s)\n", len(trials), time.Since(t0).Seconds(), csvPath)
	fmt.Printf("%-8s %6s %3s %8s %7s %9s %8s %10s\n", "mode", "hold", "n", "med_gain", "zero%", "med_head", "med_held", "med_move1")
	for _, k := range keys {
		var gains, heads, helds, firsts []float64
		zero := 0
		for _, r := range groups[k] {
			gains = append(gains, float64(r.gain))
			helds = append(helds, float64(r.held))
			if r.gain == 0 {
				zero++
			}
			if !math.IsNaN(r.headErr) {
				heads = append(heads, r.headErr)
			}
			if r.firstPos >= 0 {
				firsts = append(firsts, float64(r.firstPos))
			}
		}
		n := len(groups[k])
		fmt.Printf("%-8s %4dms %3d %8s %6.0f%% %9s %6sms %8sms\n", k.mode, k.hold, n, fmtMed(median(gains), ""),
			100*float64(zero)/float64(n), fmtMed(median(heads), "°"), fmtMed(median(helds), ""), fmtMed(median(firsts), ""))
	}
	fmt.Println("med_held = the verb's real hold (120ms poll quantum); med_move1 = ms from Stride.Do to first position change (key-down lands ~90ms in)")
}
