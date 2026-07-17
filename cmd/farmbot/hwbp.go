package main

// -findwriter <hexaddr>: hardware-breakpoint hunter. Becomes D2R's debugger, arms a CPU
// data-write breakpoint (debug registers DR0/DR7) on the cursor-mirror address across every
// thread, and captures the instruction pointer of whatever code writes it from the hardware
// input. That write site is the lever for hands-off movement: if we can neutralize it, our
// own memory writes to the mirror will persist and steer force-move with ZERO real input.
//
// Safety: DebugSetProcessKillOnExit(false) so D2R survives our detach; debug registers are
// cleared on every touched thread before DebugActiveProcessStop, so no orphaned breakpoint
// can fault the game after we leave. The whole debug loop is pinned to one OS thread
// (WaitForDebugEvent has debugger-thread affinity).

import (
	"fmt"
	"log/slog"
	"runtime"
	"time"
	"unsafe"

	d2gomem "github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	kernel32DB              = windows.NewLazySystemDLL("kernel32.dll")
	procDebugActiveProcess  = kernel32DB.NewProc("DebugActiveProcess")
	procDebugActiveProcStop = kernel32DB.NewProc("DebugActiveProcessStop")
	procDebugSetKillOnExit  = kernel32DB.NewProc("DebugSetProcessKillOnExit")
	procWaitForDebugEventEx = kernel32DB.NewProc("WaitForDebugEventEx")
	procContinueDebugEvent  = kernel32DB.NewProc("ContinueDebugEvent")
	procGetThreadContext    = kernel32DB.NewProc("GetThreadContext")
	procSetThreadContext    = kernel32DB.NewProc("SetThreadContext")
	procOpenThreadDB        = kernel32DB.NewProc("OpenThread")
)

const (
	dbgExceptionEvent    = 1
	dbgCreateThreadEvent = 2
	dbgCreateProcEvent   = 3
	dbgExitThreadEvent   = 4
	dbgExitProcEvent     = 5

	excSingleStep = 0x80000004 // hardware data breakpoint reports as single-step
	excBreakpoint = 0x80000003 // initial attach breakpoint

	dbgContinue           = 0x00010002
	dbgExceptionNotHandled = 0x80010001

	ctxDebugRegs = 0x00100010 // CONTEXT_AMD64 | CONTEXT_DEBUG_REGISTERS
	ctxControlIntDebug = 0x00100013

	threadAccess = 0x0008 | 0x0010 | 0x0040 // GET_CONTEXT|SET_CONTEXT|QUERY_INFORMATION

	// DR7: enable L0, R/W0=01 (write), LEN0=11 (4 bytes)
	dr7WriteWatch4 = (1 << 0) | (0b01 << 16) | (0b11 << 18)
)

// amd64 CONTEXT accessed through a 16-byte-aligned byte buffer (Go structs are only
// 8-aligned; GetThreadContext requires 16). Field offsets are fixed by the ABI.
type ctxBuf struct {
	b []byte
	p uintptr
}

func newCtxBuf() *ctxBuf {
	b := make([]byte, 1232+16)
	p := (uintptr(unsafe.Pointer(&b[0])) + 15) &^ 15
	return &ctxBuf{b: b, p: p}
}
func (c *ctxBuf) setU32(off uintptr, v uint32) { *(*uint32)(unsafe.Pointer(c.p + off)) = v }
func (c *ctxBuf) setU64(off uintptr, v uint64) { *(*uint64)(unsafe.Pointer(c.p + off)) = v }
func (c *ctxBuf) getU64(off uintptr) uint64    { return *(*uint64)(unsafe.Pointer(c.p + off)) }

const (
	ctxOffFlags = 0x30
	ctxOffDr0   = 0x48
	ctxOffDr7   = 0x70
	ctxOffRip   = 0xF8
)

func openThreadDB(tid uint32) uintptr {
	h, _, _ := procOpenThreadDB.Call(uintptr(threadAccess), 0, uintptr(tid))
	return h
}

// setWatch arms (or with dr0=0,dr7=0 disarms) the data-write breakpoint on one thread.
func setWatch(h uintptr, addr uint64, dr7 uint64) bool {
	c := newCtxBuf()
	c.setU32(ctxOffFlags, ctxDebugRegs)
	if r, _, _ := procGetThreadContext.Call(h, c.p); r == 0 {
		return false
	}
	c.setU64(ctxOffDr0, addr)
	c.setU64(ctxOffDr7, dr7)
	c.setU32(ctxOffFlags, ctxDebugRegs)
	r, _, _ := procSetThreadContext.Call(h, c.p)
	return r != 0
}

func readRip(h uintptr) uint64 {
	c := newCtxBuf()
	c.setU32(ctxOffFlags, ctxControlIntDebug)
	if r, _, _ := procGetThreadContext.Call(h, c.p); r == 0 {
		return 0
	}
	return c.getU64(ctxOffRip)
}

func runFindWriter(logger *slog.Logger, gr *game.MemoryReader, pid uint32, addr uint64) {
	// D2R base for module-relative reporting.
	var d2rBase, d2rSize uint64
	if mods, err := d2gomem.GetProcessModules(pid); err == nil {
		for _, m := range mods {
			if containsCI(m.ModuleName, "d2r.exe") {
				d2rBase, d2rSize = uint64(m.ModuleBaseAddress), uint64(m.ModuleBaseSize)
				break
			}
		}
	}

	// Read/write handle for reading bytes around the write site (separate from debug).
	rh, err := windows.OpenProcess(0x0400|0x0010, false, pid)
	if err != nil {
		logger.Error("findwriter: OpenProcess(read) failed", "err", err)
		return
	}
	defer windows.CloseHandle(rh)
	readBytes := func(a uint64, n int) []byte {
		buf := make([]byte, n)
		var got uintptr
		if err := windows.ReadProcessMemory(rh, uintptr(a), &buf[0], uintptr(n), &got); err != nil {
			return nil
		}
		return buf[:got]
	}

	// The mirror only updates while the cursor MOVES. Foreground D2R and wiggle the real
	// cursor in a small circle (calibration only) so the write site actually executes.
	game.ForceForegroundHWND(gr.HWND)
	wiggleStop := make(chan struct{})
	go func() {
		cx := gr.WindowLeftX + gr.GameAreaSizeX/2
		cy := gr.WindowTopY + gr.GameAreaSizeY/2
		i := 0
		tk := time.NewTicker(15 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-wiggleStop:
				return
			case <-tk.C:
				dx := (i % 7) * 6
				dy := ((i / 7) % 7) * 6
				win.SetCursorPos(int32(cx-20+dx), int32(cy-20+dy))
				i++
			}
		}
	}()

	// Debugger thread affinity: WaitForDebugEvent must run on the thread that attached.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, _, e := procDebugActiveProcess.Call(uintptr(pid)); r == 0 {
		logger.Error("findwriter: DebugActiveProcess failed (run elevated?)", "err", e)
		close(wiggleStop)
		return
	}
	procDebugSetKillOnExit.Call(0) // D2R survives our detach

	logger.Info("findwriter: attached, arming write breakpoint", "addr", fmt.Sprintf("0x%X", addr),
		"d2rBase", fmt.Sprintf("0x%X", d2rBase))

	threads := map[uint32]uintptr{}
	seen := map[uint64]int{}
	de := make([]byte, 200)
	deb := uintptr(unsafe.Pointer(&de[0]))
	get32 := func(off uintptr) uint32 { return *(*uint32)(unsafe.Pointer(deb + off)) }

	deadline := time.Now().Add(8 * time.Second)
	hits := 0

	cleanup := func() {
		for _, h := range threads {
			setWatch(h, 0, 0) // disarm before detach so no orphan breakpoint faults D2R
		}
		procDebugActiveProcStop.Call(uintptr(pid))
		close(wiggleStop)
	}

	for {
		if time.Now().After(deadline) || len(seen) >= 4 {
			break
		}
		r, _, _ := procWaitForDebugEventEx.Call(deb, 200)
		if r == 0 {
			continue // timeout
		}
		code := get32(0)
		evPid := get32(4)
		tid := get32(8)
		cont := uintptr(dbgContinue)

		switch code {
		case dbgCreateProcEvent:
			// union: hFile(16) hProcess(24) hThread(32)
			h := openThreadDB(tid)
			if h != 0 {
				threads[tid] = h
				setWatch(h, addr, dr7WriteWatch4)
			}
		case dbgCreateThreadEvent:
			h := openThreadDB(tid)
			if h != 0 {
				threads[tid] = h
				setWatch(h, addr, dr7WriteWatch4)
			}
		case dbgExitThreadEvent:
			if h, ok := threads[tid]; ok {
				windows.CloseHandle(windows.Handle(h))
				delete(threads, tid)
			}
		case dbgExceptionEvent:
			excCode := get32(16)
			switch excCode {
			case excSingleStep:
				h := threads[tid]
				if h == 0 {
					h = openThreadDB(tid)
					threads[tid] = h
				}
				rip := readRip(h)
				rel := uint64(0)
				if rip >= d2rBase && rip < d2rBase+d2rSize {
					rel = rip - d2rBase
				}
				hits++
				if _, ok := seen[rel]; !ok {
					seen[rel] = 0
					pre := readBytes(rip-16, 32)
					logger.Info("findwriter: WRITE SITE", "rip", fmt.Sprintf("0x%X", rip),
						"d2rRel", fmt.Sprintf("D2R.exe+0x%X", rel), "bytesAroundRip", hexstr(pre))
				}
				seen[rel]++
				cont = dbgContinue
			case excBreakpoint:
				cont = dbgContinue
			default:
				cont = dbgExceptionNotHandled // let D2R handle its own first-chance exceptions
			}
		case dbgExitProcEvent:
			logger.Warn("findwriter: D2R exited during hunt")
			close(wiggleStop)
			return
		}
		procContinueDebugEvent.Call(uintptr(evPid), uintptr(tid), cont)
	}

	cleanup()
	logger.Info("findwriter: done", "totalHits", hits, "distinctSites", len(seen))
	for rel, n := range seen {
		logger.Info("findwriter: site summary", "site", fmt.Sprintf("D2R.exe+0x%X", rel), "hits", n)
	}
	if len(seen) == 0 {
		logger.Info("findwriter: NO write caught — cursor mirror didn't update (wrong addr, or D2R not receiving the wiggle). Confirm the addr is live with -pathprobe-style read first.")
	}
}

func hexstr(b []byte) string {
	s := ""
	for i, x := range b {
		if i == 16 {
			s += "[RIP] "
		}
		s += fmt.Sprintf("%02x ", x)
	}
	return s
}

func containsCI(s, sub string) bool {
	ls, lsub := toLower(s), toLower(sub)
	return indexOf(ls, lsub) >= 0
}
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
