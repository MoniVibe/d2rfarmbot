package main

// Non-intrusive movement research: can we make the character walk by WRITING her movement
// destination in memory — no cursor, no foreground, no driver, nothing that touches the
// user's real input? The player's DynamicPath struct (playerUnit+0x38) holds her current
// position and, on classic D2, the move-target. If a target field here drives the walk, that
// is the aquarium movement primitive: writable every session (the path pointer is resolved
// live from the unit chain, no memory scan needed).
//
// -pathprobe: dump the path struct so we can identify the position/target words on THIS
// build (d2go reads room1 at path+0x20, shifted from classic +0x30, so offsets differ).

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/koolo/internal/game"
	"golang.org/x/sys/windows"
)

func (s *cursorScanner) readU64(addr uintptr) uint64 {
	var b [8]byte
	var read uintptr
	if err := windows.ReadProcessMemory(s.h, addr, &b[0], 8, &read); err != nil || read < 8 {
		return 0
	}
	return uint64(leU32(b[:])) | uint64(leU32(b[4:]))<<32
}

func (s *cursorScanner) readBytes(addr uintptr, n int) []byte {
	b := make([]byte, n)
	var read uintptr
	if err := windows.ReadProcessMemory(s.h, addr, &b[0], uintptr(n), &read); err != nil {
		return nil
	}
	return b[:read]
}

func (s *cursorScanner) writeU16(addr uintptr, v uint16) error {
	b := [2]byte{byte(v), byte(v >> 8)}
	var w uintptr
	return windows.WriteProcessMemory(s.h, addr, &b[0], 2, &w)
}

func u16(b []byte, off int) uint16 {
	if off+2 > len(b) {
		return 0
	}
	return uint16(b[off]) | uint16(b[off+1])<<8
}

// runPathProbe dumps the player's DynamicPath struct and annotates which u16 words match
// her current world position — those are the position/target candidates. Read-only.
func runPathProbe(logger *slog.Logger, gr *game.MemoryReader, pid uint32) {
	s, err := newCursorScanner(logger, pid, 0, 0)
	if err != nil {
		logger.Error("pathprobe: OpenProcess failed", "err", err)
		return
	}
	defer windows.CloseHandle(s.h)

	pu := gr.GetData().PlayerUnit
	if pu.Address == 0 {
		logger.Error("pathprobe: player unit address is 0 (not in game / unit table not decrypted)")
		return
	}
	pathAddr := uintptr(s.readU64(pu.Address + 0x38))
	logger.Info("pathprobe: chain", "playerUnit", fmt.Sprintf("0x%X", pu.Address),
		"pathAddr", fmt.Sprintf("0x%X", pathAddr), "livePos", fmt.Sprintf("(%d,%d)", pu.Position.X, pu.Position.Y))
	if pathAddr == 0 {
		return
	}

	// Watch the struct over a few seconds; if you nudge her manually, target words will
	// change while position words trail. All read-only.
	for iter := 0; iter < 6; iter++ {
		blob := s.readBytes(pathAddr, 0x40)
		me := gr.GetData().PlayerUnit.Position
		line := ""
		for off := 0; off+2 <= len(blob); off += 2 {
			w := u16(blob, off)
			tag := ""
			// flag words within a few subtiles of the current X or Y (pos/target candidates)
			if abs(int(w)-me.X) <= 3 {
				tag = "≈X"
			} else if abs(int(w)-me.Y) <= 3 {
				tag = "≈Y"
			}
			if tag != "" {
				line += fmt.Sprintf("+0x%02X=%d%s  ", off, w, tag)
			}
		}
		logger.Info("pathprobe: word-scan", "iter", iter, "pos", fmt.Sprintf("(%d,%d)", me.X, me.Y), "matches", line)
		time.Sleep(700 * time.Millisecond)
	}

	// Full hex of the first 0x40 bytes for manual layout reading.
	blob := s.readBytes(pathAddr, 0x40)
	hexline := ""
	for i, b := range blob {
		hexline += fmt.Sprintf("%02x ", b)
		if (i+1)%16 == 0 {
			hexline += "| "
		}
	}
	logger.Info("pathprobe: raw", "bytes", hexline)
	logger.Info("pathprobe: done — words tagged ≈X/≈Y are position/target candidates; the pair that CHANGES when she starts walking is the destination")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// runPathDrive tests whether WRITING a path target field makes the character walk — the
// non-intrusive movement primitive we want (no cursor, no focus, no key/driver input).
// For each candidate target-pair offset, it writes a destination a few subtiles away in a
// couple of directions and watches whether her position moves toward it. moveHold optionally
// holds Force-Move during the write (some builds only consume the target while the walk
// command is active); noKey skips that so we can see if the write ALONE drives her.
func runPathDrive(logger *slog.Logger, gr *game.MemoryReader, pid uint32, noKey bool, moveHold func(time.Duration)) {
	s, err := newCursorScanner(logger, pid, 0, 0)
	if err != nil {
		logger.Error("pathdrive: OpenProcess failed", "err", err)
		return
	}
	defer windows.CloseHandle(s.h)

	pu := gr.GetData().PlayerUnit
	pathAddr := uintptr(s.readU64(pu.Address + 0x38))
	if pathAddr == 0 {
		logger.Error("pathdrive: path address is 0")
		return
	}
	logger.Info("pathdrive: start", "pathAddr", fmt.Sprintf("0x%X", pathAddr),
		"pos", fmt.Sprintf("(%d,%d)", pu.Position.X, pu.Position.Y), "holdForceMove", !noKey)

	// (Xoff, Yoff) byte offsets of candidate target words, from the pathprobe dump.
	cands := []struct{ name string; xo, yo uintptr }{
		{"pair@0x10", 0x10, 0x12},
		{"pair@0x14", 0x14, 0x16},
		{"pair@0x18", 0x18, 0x1A},
	}
	// small destinations in 4 directions; one axis should be open even near a wall
	dests := []struct{ name string; dx, dy int }{
		{"south", 0, 25}, {"north", 0, -25}, {"east", 25, 0}, {"west", -25, 0},
	}

	for _, c := range cands {
		for _, d := range dests {
			me := gr.GetData().PlayerUnit.Position
			tx, ty := me.X+d.dx, me.Y+d.dy
			// spam the target while (optionally) holding force-move; a per-frame write
			// beats the movement system resetting the target to current pos each tick.
			stop := make(chan struct{})
			go func() {
				t := time.NewTicker(8 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case <-t.C:
						cur := gr.GetData().PlayerUnit.Position
						_ = s.writeU16(pathAddr+c.xo, uint16(cur.X+d.dx))
						_ = s.writeU16(pathAddr+c.yo, uint16(cur.Y+d.dy))
					}
				}
			}()
			if !noKey && moveHold != nil {
				moveHold(700 * time.Millisecond)
			} else {
				time.Sleep(700 * time.Millisecond)
			}
			close(stop)
			time.Sleep(200 * time.Millisecond)
			after := gr.GetData().PlayerUnit.Position
			ddx, ddy := after.X-me.X, after.Y-me.Y
			moved := abs(ddx) > 2 || abs(ddy) > 2
			logger.Info("pathdrive", "field", c.name, "dir", d.name,
				"wroteTarget", fmt.Sprintf("(%d,%d)", tx, ty),
				"posDelta", fmt.Sprintf("(%+d,%+d)", ddx, ddy), "MOVED", moved)
		}
	}
	logger.Info("pathdrive: done — a field+dir with MOVED=true and posDelta toward the target is the aquarium movement primitive (pure memory, no input)")
}
