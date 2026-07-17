package main

// -cursorscan: find D2R's INTERNAL cursor-position variable by differential memory
// scanning. Why: the user wants "aquarium" botting — D2R plays itself in the background
// with the real mouse/keyboard untouched. Keys and right-click already work backgrounded
// via window messages + in-memory GetKeyState patches; the ONLY input D2R refuses to take
// synthetically is the cursor position (it derives it from RawInput). If we find where the
// game STORES that derived position, we can write it directly per tick — no focus, no real
// cursor, no driver input at runtime. The build is frozen, so a found offset is permanent.
//
// Method: foreground D2R once (calibration only), drive the real cursor to known client
// points (Interception closed-loop + SetCursorPos, so whichever path the game reads gets
// the same value), snapshot all writable committed memory at each point, and intersect the
// addresses whose values track the cursor under ANY plausible encoding:
//   int32/float32 pairs x {client px, screen px} x DPI scales {1, 1.25, 1.5, 2}
//   plus world-subtile coords via the inverse isometric transform (in case the game stores
//   the mouse's WORLD target rather than a pixel position — even better for us).

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	memCommit   = 0x1000
	memPrivate  = 0x20000
	memImage    = 0x1000000
	pageRW      = 0x04
	pageWCopy   = 0x08
	pageExecRW  = 0x40
	scanCandCap = 3_000_000
)

type scanRegion struct {
	base uintptr
	size uintptr
}

type cursorScanner struct {
	h       windows.Handle
	modBase uintptr
	modSize uint32
	logger  *slog.Logger
}

func newCursorScanner(logger *slog.Logger, pid uint32, modBase uintptr, modSize uint32) (*cursorScanner, error) {
	// QUERY_INFORMATION | VM_READ | VM_WRITE | VM_OPERATION (write needed for the drive test)
	h, err := windows.OpenProcess(0x0400|0x0010|0x0020|0x0008, false, pid)
	if err != nil {
		return nil, err
	}
	return &cursorScanner{h: h, modBase: modBase, modSize: modSize, logger: logger}, nil
}

// encodeClient returns the int32 pair to WRITE for a desired client point (sx,sy) under
// the detected encoding (e.g. "screen*1.5/int32"). Mirrors cursorHypotheses' formulas.
func encodeClient(encoding string, sx, sy, winLeft, winTop int) (int32, int32) {
	scale := 1.0
	switch {
	case strings.Contains(encoding, "*2"):
		scale = 2.0
	case strings.Contains(encoding, "*1.5"):
		scale = 1.5
	case strings.Contains(encoding, "*1.2"), strings.Contains(encoding, "*1.25"):
		scale = 1.25
	}
	x, y := float64(sx), float64(sy)
	if strings.HasPrefix(encoding, "screen") {
		x, y = float64(winLeft+sx), float64(winTop+sy)
	}
	return int32(x * scale), int32(y * scale)
}

func (s *cursorScanner) writePair(addr uintptr, ix, iy int32) error {
	var buf [8]byte
	putLE32(buf[:], uint32(ix))
	putLE32(buf[4:], uint32(iy))
	var written uintptr
	return windows.WriteProcessMemory(s.h, addr, &buf[0], 8, &written)
}

func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func (s *cursorScanner) regions() []scanRegion {
	var regs []scanRegion
	var mbi windows.MemoryBasicInformation
	addr := uintptr(0)
	for addr < 0x7FFF00000000 {
		if err := windows.VirtualQueryEx(s.h, addr, &mbi, unsafe.Sizeof(mbi)); err != nil {
			break
		}
		if mbi.State == memCommit &&
			(mbi.Type == memPrivate || mbi.Type == memImage) &&
			(mbi.Protect&pageRW != 0 || mbi.Protect&pageWCopy != 0 || mbi.Protect&pageExecRW != 0) &&
			mbi.RegionSize < 1<<30 {
			regs = append(regs, scanRegion{mbi.BaseAddress, mbi.RegionSize})
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return regs
}

// hypotheses returns the expected (x,y) value pairs for a cursor parked at client (sx,sy).
type scanHypo struct {
	name   string
	ex, ey float64
	tol    float64
}

func cursorHypotheses(sx, sy, winLeft, winTop, areaW, areaH, meX, meY int) []scanHypo {
	var hs []scanHypo
	for _, sc := range []float64{1.0, 1.25, 1.5, 2.0} {
		hs = append(hs,
			scanHypo{fmt.Sprintf("client*%.2g", sc), float64(sx) * sc, float64(sy) * sc, 3},
			scanHypo{fmt.Sprintf("screen*%.2g", sc), float64(winLeft+sx) * sc, float64(winTop+sy) * sc, 3},
		)
	}
	// world-subtile target via the inverse iso transform (gameToScreen constants 19.8/9.9
	// at the 853x480 reference client — mouse world-pos would live near these values)
	dxs := float64(sx - areaW/2)
	dys := float64(sy - areaH/2)
	diffX := (dxs/19.8 + dys/9.9) / 2
	diffY := (dys/9.9 - dxs/19.8) / 2
	hs = append(hs, scanHypo{"worldSubtile", float64(meX) + diffX, float64(meY) + diffY, 5})
	return hs
}

func pairMatches(hs []scanHypo, ix, iy int32, fx, fy float32) bool {
	for _, h := range hs {
		if math.Abs(float64(ix)-h.ex) <= h.tol && math.Abs(float64(iy)-h.ey) <= h.tol {
			return true
		}
		fxf, fyf := float64(fx), float64(fy)
		if !math.IsNaN(fxf) && !math.IsNaN(fyf) &&
			math.Abs(fxf-h.ex) <= h.tol && math.Abs(fyf-h.ey) <= h.tol {
			return true
		}
	}
	return false
}

func matchName(hs []scanHypo, ix, iy int32, fx, fy float32) string {
	for _, h := range hs {
		if math.Abs(float64(ix)-h.ex) <= h.tol && math.Abs(float64(iy)-h.ey) <= h.tol {
			return h.name + "/int32"
		}
		fxf, fyf := float64(fx), float64(fy)
		if !math.IsNaN(fxf) && !math.IsNaN(fyf) &&
			math.Abs(fxf-h.ex) <= h.tol && math.Abs(fyf-h.ey) <= h.tol {
			return h.name + "/float32"
		}
	}
	return "?"
}

// fullScan walks every region and returns addresses whose (v, v+4) pair matches any hypothesis.
func (s *cursorScanner) fullScan(hs []scanHypo) []uintptr {
	var cands []uintptr
	buf := make([]byte, 1<<20)
	for _, rg := range s.regions() {
		for off := uintptr(0); off < rg.size; off += uintptr(len(buf)) {
			n := uintptr(len(buf))
			if off+n > rg.size {
				n = rg.size - off
			}
			var read uintptr
			if err := windows.ReadProcessMemory(s.h, rg.base+off, &buf[0], n, &read); err != nil || read < 8 {
				continue
			}
			for i := uintptr(0); i+8 <= read; i += 4 {
				ix := int32(leU32(buf[i:]))
				iy := int32(leU32(buf[i+4:]))
				fx := math.Float32frombits(leU32(buf[i:]))
				fy := math.Float32frombits(leU32(buf[i+4:]))
				if pairMatches(hs, ix, iy, fx, fy) {
					cands = append(cands, rg.base+off+i)
					if len(cands) >= scanCandCap {
						s.logger.Warn("cursorscan: candidate cap hit — results may be incomplete")
						return cands
					}
				}
			}
		}
	}
	return cands
}

// refine re-reads only the candidate addresses (chunked by 64KB block) and keeps matches.
func (s *cursorScanner) refine(cands []uintptr, hs []scanHypo) []uintptr {
	sort.Slice(cands, func(i, j int) bool { return cands[i] < cands[j] })
	var out []uintptr
	buf := make([]byte, 1<<16)
	i := 0
	for i < len(cands) {
		blockBase := cands[i] &^ 0xFFFF
		var read uintptr
		err := windows.ReadProcessMemory(s.h, blockBase, &buf[0], uintptr(len(buf)), &read)
		j := i
		for j < len(cands) && cands[j]&^0xFFFF == blockBase {
			if err == nil {
				off := cands[j] - blockBase
				if off+8 <= read {
					ix := int32(leU32(buf[off:]))
					iy := int32(leU32(buf[off+4:]))
					fx := math.Float32frombits(leU32(buf[off:]))
					fy := math.Float32frombits(leU32(buf[off+4:]))
					if pairMatches(hs, ix, iy, fx, fy) {
						out = append(out, cands[j])
					}
				}
			}
			j++
		}
		i = j
	}
	return out
}

func leU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// runCursorScan is the -cursorscan entrypoint, called from main with live game handles.
// moveHold holds the Force-Move key for dur without touching the real cursor (supplied by
// main so cursorscan.go stays free of injector/keybind details). If drive is true, after
// locating the cursor variable it runs the WRITE-DRIVE test: the aquarium proof — steer the
// character by WRITING the cursor variable while the real mouse sits still.
func runCursorScan(logger *slog.Logger, gr *game.MemoryReader, hid *game.HID, gi *game.MemoryInjector, pid uint32, modBase uintptr, modSize uint32, drive, noFG bool, moveHold func(time.Duration)) {
	s, err := newCursorScanner(logger, pid, modBase, modSize)
	if err != nil {
		logger.Error("cursorscan: OpenProcess failed", "err", err)
		return
	}
	defer windows.CloseHandle(s.h)

	me := gr.GetData().PlayerUnit.Position

	aim := func(sx, sy int) {
		game.ForceForegroundHWND(gr.HWND)
		hid.MovePointer(sx, sy) // interception (closed-loop rel) + injection + WM_MOUSEMOVE
		win.SetCursorPos(int32(gr.WindowLeftX+sx), int32(gr.WindowTopY+sy))
		time.Sleep(300 * time.Millisecond) // let the game tick its input state
	}
	hypos := func(sx, sy int) []scanHypo {
		return cursorHypotheses(sx, sy, gr.WindowLeftX, gr.WindowTopY,
			gr.GameAreaSizeX, gr.GameAreaSizeY, me.X, me.Y)
	}

	probes := [][2]int{{150, 150}, {620, 310}, {300, 420}, {680, 120}}

	logger.Info("cursorscan: pass 1 (full memory) — commandeering the cursor briefly for calibration")
	aim(probes[0][0], probes[0][1])
	t0 := time.Now()
	cands := s.fullScan(hypos(probes[0][0], probes[0][1]))
	logger.Info("cursorscan: pass 1 done", "candidates", len(cands), "took", time.Since(t0).Round(time.Millisecond))

	for pi := 1; pi < len(probes); pi++ {
		if len(cands) == 0 {
			break
		}
		aim(probes[pi][0], probes[pi][1])
		cands = s.refine(cands, hypos(probes[pi][0], probes[pi][1]))
		logger.Info("cursorscan: refined", "pass", pi+1, "candidates", len(cands))
	}

	if len(cands) == 0 {
		logger.Info("cursorscan: NO surviving addresses — the internal cursor is not stored in a scanned encoding (try int16/float64/other bases next, or ask the advisor for the RawInput-buffer RE route)")
		return
	}

	// Final probe: park somewhere new and dump survivor values + encoding so the writer
	// mode can be built against the right interpretation.
	fx, fy := 500, 260
	aim(fx, fy)
	hs := hypos(fx, fy)
	var winAddr uintptr
	var winEnc string
	shown := 0
	for _, a := range cands {
		if shown >= 60 {
			logger.Info("cursorscan: (more survivors omitted)", "total", len(cands))
			break
		}
		var b [8]byte
		var read uintptr
		if err := windows.ReadProcessMemory(s.h, a, &b[0], 8, &read); err != nil || read < 8 {
			continue
		}
		ix := int32(leU32(b[:]))
		iy := int32(leU32(b[4:]))
		fxv := math.Float32frombits(leU32(b[:]))
		fyv := math.Float32frombits(leU32(b[4:]))
		loc := "heap"
		if a >= s.modBase && a < s.modBase+uintptr(s.modSize) {
			loc = fmt.Sprintf("D2R.exe+0x%X", a-s.modBase)
		}
		enc := matchName(hs, ix, iy, fxv, fyv)
		if winAddr == 0 {
			winAddr, winEnc = a, enc
		}
		logger.Info("cursorscan: SURVIVOR", "addr", fmt.Sprintf("0x%X", uintptr(a)), "loc", loc,
			"int32", fmt.Sprintf("(%d,%d)", ix, iy), "float32", fmt.Sprintf("(%.1f,%.1f)", fxv, fyv),
			"encoding", enc)
		shown++
	}
	logger.Info("cursorscan: done", "survivors", len(cands),
		"note", "survivors matching at ALL probe points track the cursor; D2R.exe-relative ones are permanently patchable on this frozen build")

	if !drive || winAddr == 0 || !strings.Contains(winEnc, "int32") {
		return
	}
	_ = gi
	driveTest(logger, s, gr, hid, winAddr, winEnc, noFG, moveHold)
}

// runCursorDriveAt opens the process fresh and runs the WRITE-DRIVE test against a known
// address (no scan). For fast iteration within one D2R session.
func runCursorDriveAt(logger *slog.Logger, gr *game.MemoryReader, hid *game.HID, pid uint32, addr uintptr, enc string, noFG bool, moveHold func(time.Duration)) {
	s, err := newCursorScanner(logger, pid, 0, 0)
	if err != nil {
		logger.Error("cursoraddr: OpenProcess failed", "err", err)
		return
	}
	defer windows.CloseHandle(s.h)
	_ = hid
	driveTest(logger, s, gr, hid, addr, enc, noFG, moveHold)
}

// driveTest is the AQUARIUM PROOF. It parks the real cursor at screen-center and, per world
// axis, continuously WRITES the cursor variable to an off-center point in that direction
// while holding Force-Move. Reading it: with the real cursor at center (=self, ~0 move), a
// nonzero worldDelta toward the WRITTEN direction means the write steers movement → aquarium
// mode is viable. Zero everywhere means D2R derives movement from RawInput and ignores the
// written shadow. Run with noFG=true to test whether background RawInput (INPUTSINK) still
// overrides the write.
// shadowWalkBG is the fully non-intrusive test: hold Force-Move via the memory key-patch
// (works backgrounded) and spam-write the cursor shadow toward each direction — NO
// foreground grab, NO real-cursor movement, NO driver strokes. If she walks toward the
// written direction, D2R's force-move command reads this shadow and the aquarium works with
// zero disturbance to the user's input. Needs open ground (walls read as 0 either way).
func shadowWalkBG(logger *slog.Logger, s *cursorScanner, gr *game.MemoryReader, addr uintptr, enc string, moveHold func(time.Duration)) {
	logger.Info("shadowwalk: NON-INTRUSIVE test — Force-Move held via memory patch + shadow write, real input untouched",
		"addr", fmt.Sprintf("0x%X", uintptr(addr)), "encoding", enc)
	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	dirs := []struct {
		name   string
		dx, dy int
	}{
		{"south", -200, 100}, {"east", 200, 100}, {"north", 200, -100}, {"west", -200, -100},
	}
	var realBefore win.POINT
	win.GetCursorPos(&realBefore)
	for _, dr := range dirs {
		tsx, tsy := cx+dr.dx, cy+dr.dy
		ix, iy := encodeClient(enc, tsx, tsy, gr.WindowLeftX, gr.WindowTopY)
		before := gr.GetData().PlayerUnit.Position
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(4 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					s.writePair(addr, ix, iy)
				}
			}
		}()
		moveHold(700 * time.Millisecond)
		close(stop)
		time.Sleep(300 * time.Millisecond)
		after := gr.GetData().PlayerUnit.Position
		var realNow win.POINT
		win.GetCursorPos(&realNow)
		logger.Info("shadowwalk", "dir", dr.name, "wrote", fmt.Sprintf("(%d,%d)", ix, iy),
			"posDelta", fmt.Sprintf("(%+d,%+d)", after.X-before.X, after.Y-before.Y),
			"realCursorMoved", realNow.X != realBefore.X || realNow.Y != realBefore.Y)
	}
	logger.Info("shadowwalk: done — nonzero posDelta with realCursorMoved=false = AQUARIUM VIABLE (pure memory, zero input disturbance)")
}

func driveTest(logger *slog.Logger, s *cursorScanner, gr *game.MemoryReader, hid *game.HID, addr uintptr, enc string, noFG bool, moveHold func(time.Duration)) {
	logger.Info("cursorscan: WRITE-DRIVE test — real cursor parked at center, steering by memory write",
		"addr", fmt.Sprintf("0x%X", uintptr(addr)), "encoding", enc, "foreground", !noFG)
	game.SetHWMoveMode("rel") // control path uses the proven closed-loop driver cursor move
	if !noFG {
		game.ForceForegroundHWND(gr.HWND)
	}
	win.SetCursorPos(int32(gr.WindowLeftX+gr.GameAreaSizeX/2), int32(gr.WindowTopY+gr.GameAreaSizeY/2))
	time.Sleep(300 * time.Millisecond)

	// Sanity: confirm the write lands and the value we read back matches what we wrote.
	tix, tiy := encodeClient(enc, 700, 200, gr.WindowLeftX, gr.WindowTopY)
	_ = s.writePair(addr, tix, tiy)
	var chk [8]byte
	var rd uintptr
	windows.ReadProcessMemory(s.h, addr, &chk[0], 8, &rd)
	logger.Info("cursorscan: write-readback", "wrote", fmt.Sprintf("(%d,%d)", tix, tiy),
		"readBack", fmt.Sprintf("(%d,%d)", int32(leU32(chk[:])), int32(leU32(chk[4:]))))

	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	dirs := []struct {
		name   string
		dx, dy int
	}{
		{"WORLD+X east", 200, 100}, {"WORLD-X west", -200, -100},
		{"WORLD+Y south", -200, 100}, {"WORLD-Y north", 200, -100},
	}
	for _, dr := range dirs {
		tsx, tsy := cx+dr.dx, cy+dr.dy
		ix, iy := encodeClient(enc, tsx, tsy, gr.WindowLeftX, gr.WindowTopY)

		// (a) WRITE path: real cursor stays at center, spam the shadow toward this dir.
		win.SetCursorPos(int32(gr.WindowLeftX+cx), int32(gr.WindowTopY+cy))
		time.Sleep(120 * time.Millisecond)
		before := gr.GetData().PlayerUnit.Position
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(4 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					s.writePair(addr, ix, iy)
				}
			}
		}()
		moveHold(600 * time.Millisecond)
		close(stop)
		time.Sleep(400 * time.Millisecond)
		writeDelta := gr.GetData().PlayerUnit.Position
		wdx, wdy := writeDelta.X-before.X, writeDelta.Y-before.Y

		// (b) POSITIVE CONTROL: drive the real cursor to the SAME point via the proven
		// closed-loop driver move, then hold force-move. If this moves her but (a) didn't,
		// the shadow write is genuinely ignored and RawInput is the true movement input.
		beforeC := gr.GetData().PlayerUnit.Position
		hid.MovePointer(tsx, tsy)
		time.Sleep(120 * time.Millisecond)
		moveHold(600 * time.Millisecond)
		time.Sleep(400 * time.Millisecond)
		ctrlDelta := gr.GetData().PlayerUnit.Position
		cdx, cdy := ctrlDelta.X-beforeC.X, ctrlDelta.Y-beforeC.Y

		logger.Info("cursorscan: drive", "dir", dr.name,
			"wrote", fmt.Sprintf("(%d,%d)", ix, iy),
			"writeDelta(realCursor=center)", fmt.Sprintf("(%+d,%+d)", wdx, wdy),
			"controlDelta(realCursor=target)", fmt.Sprintf("(%+d,%+d)", cdx, cdy))
	}
	logger.Info("cursorscan: WRITE-DRIVE done — if control moved her but write didn't, the shadow is ignored (RawInput is the true input); if write moved her, AQUARIUM is viable")
}
