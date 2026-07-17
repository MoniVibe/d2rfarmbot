package game

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hectorgimenez/d2go/pkg/memory"
	"golang.org/x/sys/windows"
)

const fullAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_WRITE | windows.PROCESS_VM_READ

// ResumeThread (bind from kernel32; not exported by this x/sys/windows). Used ONLY for recovery —
// resuming a thread can never deadlock (unlike SuspendThread, which was removed for that reason).
var procResumeThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("ResumeThread")

// ResumeAllThreads clears a LEAKED suspend on every D2R thread (recovery for an earlier crash that
// died mid-suspend, which leaves a thread's message pump frozen so SendMessage/HoldKey hang). Safe:
// ResumeThread on a non-suspended thread is a no-op. Returns how many resume calls actually lowered
// a suspend count.
func (i *MemoryInjector) ResumeAllThreads() int {
	var tids []uint32
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0
	}
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if windows.Thread32First(snap, &te) == nil {
		for {
			if te.OwnerProcessID == i.pid {
				tids = append(tids, te.ThreadID)
			}
			if windows.Thread32Next(snap, &te) != nil {
				break
			}
		}
	}
	windows.CloseHandle(snap)
	resumed := 0
	for _, tid := range tids {
		h, e := windows.OpenThread(0x0002, false, tid) // THREAD_SUSPEND_RESUME
		if e != nil {
			continue
		}
		for k := 0; k < 8; k++ {
			r, _, _ := procResumeThread.Call(uintptr(h))
			if r == 0xFFFFFFFF || r <= 1 { // r = previous suspend count; <=1 means now 0
				break
			}
			resumed++
		}
		windows.CloseHandle(h)
	}
	return resumed
}

type MemoryInjector struct {
	isLoaded              bool
	pid                   uint32
	handle                windows.Handle
	getCursorPosAddr      uintptr
	getCursorPosOrigBytes [32]byte
	trackMouseEventAddr   uintptr
	trackMouseEventBytes  [32]byte
	getKeyStateAddr       uintptr
	getKeyStateOrigBytes  [18]byte
	getAsyncKeyStateAddr      uintptr
	getAsyncKeyStateOrigBytes [18]byte
	getKeyboardStateAddr      uintptr
	getKeyboardStateOrigBytes [24]byte
	setCursorPosAddr      uintptr
	setCursorPosOrigBytes [16]byte
	// Modern cursor-read exports. Newer D2R builds read the in-game cursor via the physical
	// (DPI-actual) pointer or GetCursorInfo rather than the classic GetCursorPos koolo patches,
	// so patching these is what actually feeds the in-game force-move a synthetic cursor —
	// backgrounded, out-of-exe (system DLLs), no debugger. All resolved from user32.
	getPhysicalCursorPosAddr      uintptr
	getPhysicalCursorPosOrigBytes [32]byte
	getCursorInfoAddr             uintptr
	getCursorInfoOrigBytes        [32]byte
	logger *slog.Logger
}

// Stub templates. Each hot stub is installed ONCE, then the hot path mutates only DATA (the cursor
// X/Y immediates, or the keystate compare byte) — never code — because a half-written multi-byte
// code rewrite can be executed mid-write by a D2R thread (illegal instruction -> AV).
//
// The `wild` indexes are the mutable data slots, ignored when checking whether a stub is intact.
var (
	// cmp cl,<key> ; sete al ; shl ax,15 ; ret   — key at index 2
	keyStateStub     = []byte{0x80, 0xF9, 0x00, 0x0F, 0x94, 0xC0, 0x66, 0xC1, 0xE0, 0x0F, 0xC3}
	keyStateStubWild = []int{2}
	// mov rax,<imm64 packed X|Y> ; mov [rcx],rax ; mov al,1 ; ret   — imm64 at indexes 2..9
	physStub     = []byte{0x48, 0xB8, 0, 0, 0, 0, 0, 0, 0, 0, 0x48, 0x89, 0x01, 0xB0, 0x01, 0xC3}
	physStubWild = []int{2, 3, 4, 5, 6, 7, 8, 9}
)

// stubIntact reads back the bytes at addr and reports whether OUR stub is still the code living
// there, ignoring the mutable data slots in `wild`.
//
// This exists because cached "is my stub installed?" booleans are a lie waiting to happen: the flag
// lives in OUR process while the thing it describes lives in D2R's, where a second farmbot instance,
// -fixinput's HealInput, a re-Load, or an aliased export can overwrite it without our knowledge. A
// stale flag sends the hot path down its partial-write branch, splicing data bytes into the MIDDLE
// of whatever code is actually there — which is precisely how GetCursorPos became a wild jump and
// crashed the game at random. That bug was fixed by adding an invalidate call; this replaces the
// pattern instead, so the next person to write to one of these addresses cannot reintroduce it by
// forgetting. One extra ReadProcessMemory per call (~50/s) is a rounding error.
func (i *MemoryInjector) stubIntact(addr uintptr, want []byte, wild []int) bool {
	if addr == 0 {
		return false
	}
	got := make([]byte, len(want))
	if err := windows.ReadProcessMemory(i.handle, addr, &got[0], uintptr(len(got)), nil); err != nil {
		return false
	}
	skip := make(map[int]bool, len(wild))
	for _, w := range wild {
		skip[w] = true
	}
	for n := range want {
		if !skip[n] && got[n] != want[n] {
			return false
		}
	}
	return true
}

// keyStateDisabled is the compare byte that makes a resident keystate stub report "not held" for
// every real key (0xFF is not a valid VK). Toggling this single byte is atomic — no code rewrite.
const keyStateDisabled = 0xFF

// writeCode writes executable-stub bytes to the target. Used ONLY for the rare one-time stub
// installs and exit restores (~10 writes per run) — NOT the hot path, which mutates data only
// (atomic X/Y immediates, single key bytes). A previous version suspended all D2R threads around
// the write for a bulletproof no-race guarantee, but that intermittently DEADLOCKED (suspending a
// thread holding the snapshot/loader lock). Since the hot-path data-only design already removes
// the ~50-rewrites/sec churn that caused crashes, a plain write here (a handful per run) leaves a
// negligible residual race and — critically — cannot hang the game.
func (i *MemoryInjector) writeCode(addr uintptr, b []byte) error {
	if addr == 0 || len(b) == 0 {
		return nil
	}
	return windows.WriteProcessMemory(i.handle, addr, &b[0], uintptr(len(b)), nil)
}

// pristineBytes returns the first n bytes of a user32 export from THIS process (unpatched). System
// DLLs share the same file+image base across processes in a session, so these are the exact
// original bytes for D2R's user32 — a cascade-immune restore source (never re-reads a patched stub).
func (i *MemoryInjector) pristineBytes(fnName string, n int) []byte {
	own := syscall.MustLoadDLL("USER32.dll")
	p, e := own.FindProc(fnName)
	if e != nil {
		return nil
	}
	addr := p.Addr()
	if addr == 0 {
		return nil
	}
	b := make([]byte, n)
	copy(b, unsafe.Slice((*byte)(unsafe.Pointer(addr)), n))
	return b
}

func InjectorInit(logger *slog.Logger, pid uint32) (*MemoryInjector, error) {
	i := &MemoryInjector{pid: pid, logger: logger}
	pHandle, err := windows.OpenProcess(fullAccess, false, pid)
	if err != nil {
		return nil, fmt.Errorf("error opening process: %w", err)
	}
	i.handle = pHandle

	return i, nil
}

func (i *MemoryInjector) Load() error {
	if i.isLoaded {
		return nil
	}

	modules, err := memory.GetProcessModules(i.pid)
	if err != nil {
		return fmt.Errorf("error getting process modules: %w", err)
	}

	syscall.MustLoadDLL("USER32.dll")

	for _, module := range modules {
		// GetCursorPos
		if strings.Contains(strings.ToLower(module.ModuleName), "user32.dll") {
			i.getCursorPosAddr, err = syscall.GetProcAddress(module.ModuleHandle, "GetCursorPos")
			i.getKeyStateAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetKeyState")
			i.trackMouseEventAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "TrackMouseEvent")
			i.setCursorPosAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "SetCursorPos")

			err = windows.ReadProcessMemory(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)), nil)
			if err != nil {
				return fmt.Errorf("error reading memory: %w", err)
			}

			err = i.stopTrackingMouseLeaveEvents()
			if err != nil {
				return err
			}

			err = i.OverrideSetCursorPos()
			if err != nil {
				return err
			}

			err = windows.ReadProcessMemory(i.handle, i.getKeyStateAddr, &i.getKeyStateOrigBytes[0], uintptr(len(i.getKeyStateOrigBytes)), nil)
			if err != nil {
				return fmt.Errorf("error reading memory: %w", err)
			}

			// GetAsyncKeyState — modern/CoreWindow menus often still poll this for hotkeys.
			i.getAsyncKeyStateAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetAsyncKeyState")
			if i.getAsyncKeyStateAddr != 0 {
				_ = windows.ReadProcessMemory(i.handle, i.getAsyncKeyStateAddr, &i.getAsyncKeyStateOrigBytes[0], uintptr(len(i.getAsyncKeyStateOrigBytes)), nil)
			}
			i.getKeyboardStateAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetKeyboardState")
			if i.getKeyboardStateAddr != 0 {
				_ = windows.ReadProcessMemory(i.handle, i.getKeyboardStateAddr, &i.getKeyboardStateOrigBytes[0], uintptr(len(i.getKeyboardStateOrigBytes)), nil)
			}

			// Modern cursor reads (may or may not exist / be used by this build).
			i.getPhysicalCursorPosAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetPhysicalCursorPos")
			if i.getPhysicalCursorPosAddr != 0 {
				_ = windows.ReadProcessMemory(i.handle, i.getPhysicalCursorPosAddr, &i.getPhysicalCursorPosOrigBytes[0], uintptr(len(i.getPhysicalCursorPosOrigBytes)), nil)
			}
			i.getCursorInfoAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetCursorInfo")
			if i.getCursorInfoAddr != 0 {
				_ = windows.ReadProcessMemory(i.handle, i.getCursorInfoAddr, &i.getCursorInfoOrigBytes[0], uintptr(len(i.getCursorInfoOrigBytes)), nil)
			}
		}
	}
	if i.getCursorPosAddr == 0 || i.getKeyStateAddr == 0 {
		return errors.New("could not find GetCursorPos address")
	}

	i.isLoaded = true
	return nil
}

func (i *MemoryInjector) Unload() error {
	if err := i.RestoreMemory(); err != nil {
		i.logger.Error(fmt.Sprintf("error restoring memory: %v", err))
	}
	// Belt-and-braces: unconditionally heal the input functions to pristine bytes from our own
	// user32. RestoreMemory relies on originals captured at Load time, but if a PRIOR run left
	// D2R patched, that capture is itself the patched stub and restore is a no-op — leaving the
	// human's mouse broken (reads the frozen injected cursor). HealInput always writes pristine,
	// breaking that cascade so every clean exit hands input back to the player.
	if _, err := i.HealInput(); err != nil {
		i.logf("heal input on unload failed", err)
	}
	return windows.CloseHandle(i.handle)
}

func (i *MemoryInjector) RestoreMemory() error {
	if !i.isLoaded {
		return nil
	}

	i.isLoaded = false
	// Restore ALL patched functions so D2R is left exactly as we found it (leaving any
	// patched — SetCursorPos/TrackMouseEvent/etc. — can destabilize/crash the game after detach).
	// Errors are logged, not swallowed: a silent partial restore is exactly how a "clean" exit
	// still leaves D2R's in-game mouse/keys flaky until the game is restarted.
	if err := i.RestoreGetCursorPosAddr(); err != nil {
		i.logf("restore GetCursorPos failed", err)
	}
	if err := i.RestoreGetKeyState(); err != nil {
		i.logf("restore GetKeyState failed", err)
	}
	if err := i.RestoreGetAsyncKeyState(); err != nil {
		i.logf("restore GetAsyncKeyState failed", err)
	}
	if err := i.RestoreGetKeyboardState(); err != nil {
		i.logf("restore GetKeyboardState failed", err)
	}
	if i.setCursorPosAddr != 0 && i.setCursorPosOrigBytes[0] != 0 {
		if err := windows.WriteProcessMemory(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)), nil); err != nil {
			i.logf("restore SetCursorPos failed", err)
		}
		i.verifyRestore("SetCursorPos", i.setCursorPosAddr, i.setCursorPosOrigBytes[:])
	}
	if i.trackMouseEventAddr != 0 && i.trackMouseEventBytes[0] != 0 {
		if err := windows.WriteProcessMemory(i.handle, i.trackMouseEventAddr, &i.trackMouseEventBytes[0], uintptr(len(i.trackMouseEventBytes)), nil); err != nil {
			i.logf("restore TrackMouseEvent failed", err)
		}
		i.verifyRestore("TrackMouseEvent", i.trackMouseEventAddr, i.trackMouseEventBytes[:])
	}
	if i.getPhysicalCursorPosAddr != 0 && i.getPhysicalCursorPosOrigBytes[0] != 0 {
		if err := i.RestorePhysicalCursorPos(); err != nil {
			i.logf("restore GetPhysicalCursorPos failed", err)
		}
		i.verifyRestore("GetPhysicalCursorPos", i.getPhysicalCursorPosAddr, i.getPhysicalCursorPosOrigBytes[:])
	}
	if i.getCursorInfoAddr != 0 && i.getCursorInfoOrigBytes[0] != 0 {
		if err := i.RestoreGetCursorInfo(); err != nil {
			i.logf("restore GetCursorInfo failed", err)
		}
		i.verifyRestore("GetCursorInfo", i.getCursorInfoAddr, i.getCursorInfoOrigBytes[:])
	}
	return nil
}

func (i *MemoryInjector) logf(msg string, err error) {
	if i.logger != nil {
		i.logger.Warn(msg, "err", err)
	}
}

// verifyRestore reads a just-restored region back and warns if it doesn't match the
// original bytes we intended to write. Guards against a botched restore silently
// leaving D2R's input functions mangled (which shows up as flaky in-game mouse/keys
// that only a D2R restart clears).
func (i *MemoryInjector) verifyRestore(name string, addr uintptr, want []byte) {
	if i.logger == nil || addr == 0 {
		return
	}
	got := make([]byte, len(want))
	if err := windows.ReadProcessMemory(i.handle, addr, &got[0], uintptr(len(got)), nil); err != nil {
		i.logger.Warn("restore verify read failed", "fn", name, "err", err)
		return
	}
	if !bytes.Equal(got, want) {
		i.logger.Warn("restore verify MISMATCH — D2R input may be left mangled; restart D2R before next run", "fn", name)
	}
}

func (i *MemoryInjector) CursorPos(x, y int) error {
	if !i.isLoaded {
		return nil
	}

	/*
		push rax
		mov rax, rcx
		mov dword ptr [rax], 1 // X
		mov dword ptr [rax+4], 2 // Y
		pop rax
		mov al, 1
		ret
	*/
	bytes := []byte{0x50, 0x48, 0x89, 0xC8, 0xC7, 0x00, 0x01, 0x00, 0x00, 0x00, 0xC7, 0x40, 0x04, 0x02, 0x00, 0x00, 0x00, 0x58, 0xB0, 0x01, 0xC3}

	buff := make([]byte, 4)
	binary.LittleEndian.PutUint32(buff, uint32(x))
	copy(bytes[6:], buff)

	binary.LittleEndian.PutUint32(buff, uint32(y))
	copy(bytes[13:], buff)

	// ALIASING HAZARD: on this Windows build GetCursorPos and GetPhysicalCursorPos are THE SAME
	// FUNCTION (verified: both resolve to one address; likewise SetCursorPos/SetPhysicalCursorPos).
	// So this write lands on top of the physical stub. That is safe now only because
	// OverridePhysicalCursorPos re-reads the address before its partial write (stubIntact) instead
	// of trusting a flag — do not reintroduce a cached "installed" bool here.
	return windows.WriteProcessMemory(i.handle, i.getCursorPosAddr, &bytes[0], uintptr(len(bytes)), nil)
}

// AliasesPhysicalCursorPos reports whether GetCursorPos and GetPhysicalCursorPos are the same
// function on this build. When they are, patching one IS patching the other — so writing both
// (with different coordinate scales, as MovePointer used to) makes them fight over one address.
func (i *MemoryInjector) AliasesPhysicalCursorPos() bool {
	return i.getCursorPosAddr != 0 && i.getCursorPosAddr == i.getPhysicalCursorPosAddr
}


// OverrideGetKeyState makes GetKeyState report `key` as held. The stub (cmp cl,key; sete al;
// shl ax,15; ret) is installed ONCE (thread-suspended); thereafter we mutate only the compare
// byte at offset 2 — an atomic 1-byte write, so the per-movement-step churn never rewrites code
// (that was the crash race). The final pristine restore happens on Unload/HealInput.
func (i *MemoryInjector) OverrideGetKeyState(key byte) error {
	if !i.isLoaded || i.getKeyStateAddr == 0 {
		return nil
	}
	// Verify the stub is still OUR code before touching only its data byte — never trust a cached
	// flag (see stubIntact). If anything healed/overwrote this address, reinstall the whole stub.
	if !i.stubIntact(i.getKeyStateAddr, keyStateStub, keyStateStubWild) {
		stub := append([]byte(nil), keyStateStub...)
		stub[2] = key
		return i.writeCode(i.getKeyStateAddr, stub)
	}
	return windows.WriteProcessMemory(i.handle, i.getKeyStateAddr+2, &key, 1, nil) // atomic
}
func (i *MemoryInjector) OverrideSetCursorPos() error {
	/*
		Just do nothing, this prevents the game from moving our cursor, for example when opening inventory or wp list
		mov eax, 1
		ret
	*/

	// Save the original bytes first so RestoreMemory can put them back (else the game is
	// left with a no-op SetCursorPos after detach, which can crash it).
	_ = windows.ReadProcessMemory(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)), nil)
	blob := []byte{0xB8, 0x01, 0x00, 0x00, 0x00, 0xC3}
	return windows.WriteProcessMemory(i.handle, i.setCursorPosAddr, &blob[0], uintptr(len(blob)), nil)
}

// RestoreGetKeyState disables the resident stub by setting its compare byte to a VK that never
// matches (atomic 1-byte write) — the stub reports "not held" for every real key, equivalent to
// no key pressed, without a code rewrite. The true pristine bytes are put back on Unload/HealInput.
func (i *MemoryInjector) RestoreGetKeyState() error {
	// Only poke the compare byte if OUR stub is genuinely still there; otherwise this 1-byte write
	// lands inside whatever code now occupies the address (pristine thunk, another stub) and turns
	// it into garbage.
	if !i.stubIntact(i.getKeyStateAddr, keyStateStub, keyStateStubWild) {
		return nil
	}
	k := byte(keyStateDisabled)
	return windows.WriteProcessMemory(i.handle, i.getKeyStateAddr+2, &k, 1, nil)
}

// OverrideGetAsyncKeyState patches GetAsyncKeyState to report `key` as held (0x8000) for
// any caller — lets us feed the menu a synthetic hotkey in the background (no focus/real input).
// OverrideGetAsyncKeyState — resident-stub twin of OverrideGetKeyState (install once, then atomic
// compare-byte toggles on the hot path; pristine restore on Unload/HealInput).
func (i *MemoryInjector) OverrideGetAsyncKeyState(key byte) error {
	if !i.isLoaded || i.getAsyncKeyStateAddr == 0 {
		return nil
	}
	if !i.stubIntact(i.getAsyncKeyStateAddr, keyStateStub, keyStateStubWild) {
		stub := append([]byte(nil), keyStateStub...)
		stub[2] = key
		return i.writeCode(i.getAsyncKeyStateAddr, stub)
	}
	return windows.WriteProcessMemory(i.handle, i.getAsyncKeyStateAddr+2, &key, 1, nil) // atomic
}

func (i *MemoryInjector) RestoreGetAsyncKeyState() error {
	if !i.stubIntact(i.getAsyncKeyStateAddr, keyStateStub, keyStateStubWild) {
		return nil
	}
	k := byte(keyStateDisabled)
	return windows.WriteProcessMemory(i.handle, i.getAsyncKeyStateAddr+2, &k, 1, nil)
}

// OverrideGetKeyboardState patches GetKeyboardState to mark `key` as down (0x80) in the
// caller's 256-byte array. Stub: mov byte[rcx+key],0x80 ; mov eax,1 ; ret.
func (i *MemoryInjector) OverrideGetKeyboardState(key byte) error {
	if !i.isLoaded || i.getKeyboardStateAddr == 0 {
		return nil
	}
	blob := []byte{0xC6, 0x81, key, 0x00, 0x00, 0x00, 0x80, 0xB8, 0x01, 0x00, 0x00, 0x00, 0xC3}
	return windows.WriteProcessMemory(i.handle, i.getKeyboardStateAddr, &blob[0], uintptr(len(blob)), nil)
}

func (i *MemoryInjector) RestoreGetKeyboardState() error {
	if i.getKeyboardStateAddr == 0 {
		return nil
	}
	return windows.WriteProcessMemory(i.handle, i.getKeyboardStateAddr, &i.getKeyboardStateOrigBytes[0], uintptr(len(i.getKeyboardStateOrigBytes)), nil)
}

func (i *MemoryInjector) RestoreGetCursorPosAddr() error {
	// Restores pristine bytes over the address SHARED with GetPhysicalCursorPos, so the physical
	// stub is gone afterwards. Safe because OverridePhysicalCursorPos re-reads before its partial
	// write (stubIntact) rather than trusting a cached flag.
	return windows.WriteProcessMemory(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)), nil)
}

// HasPhysicalCursorPos / HasCursorInfo report whether the export was resolvable on this build.
func (i *MemoryInjector) HasPhysicalCursorPos() bool { return i.getPhysicalCursorPosAddr != 0 }
func (i *MemoryInjector) HasCursorInfo() bool        { return i.getCursorInfoAddr != 0 }

// OverridePhysicalCursorPos patches user32!GetPhysicalCursorPos to return a fixed physical
// (DPI-actual) screen point — same LPPOINT-out/BOOL signature as GetCursorPos, so the same
// stub shape applies. This is the leading candidate for how newer D2R reads the in-game
// cursor (the internal cursor mirror is stored in physical pixels).
func (i *MemoryInjector) OverridePhysicalCursorPos(x, y int) error {
	if !i.isLoaded || i.getPhysicalCursorPosAddr == 0 {
		return nil
	}
	// Stub: mov rax, imm64 ; mov [rcx],rax ; mov al,1 ; ret. GetPhysicalCursorPos(LPPOINT) has the
	// out-pointer in rcx; POINT is {x@0, y@4}, so one 8-byte store writes BOTH. X and Y live
	// CONTIGUOUSLY in the imm64 (X=low32, Y=high32) so the hot path updates both in ONE 8-byte write
	// — no cursor jitter. (The previous version wrote X and Y as two separate 4-byte stores, flashing
	// an intermediate (newX,oldY) that flickered the in-game hover.) Install once (code), then data-only.
	packed := uint64(uint32(x)) | uint64(uint32(y))<<32
	// Read back rather than trust a flag. This address is SHARED with GetCursorPos (they are one
	// function), so CursorPos(), RestoreGetCursorPosAddr(), HealInput(), or a second farmbot can all
	// replace this code underneath us. Splicing the imm64 into whatever is actually there was the
	// random-crash bug; verifying costs one read.
	if !i.stubIntact(i.getPhysicalCursorPosAddr, physStub, physStubWild) {
		stub := append([]byte(nil), physStub...)
		binary.LittleEndian.PutUint64(stub[2:], packed)
		return i.writeCode(i.getPhysicalCursorPosAddr, stub)
	}
	// HOT PATH: update the 8-byte imm64 (X+Y together) in one write — atomic within its cache line,
	// so no torn (newX,oldY) intermediate. Data, not code: no crash race.
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], packed)
	return windows.WriteProcessMemory(i.handle, i.getPhysicalCursorPosAddr+2, &b[0], 8, nil)
}

func (i *MemoryInjector) RestorePhysicalCursorPos() error {
	// Nothing of ours here => nothing to undo. Writing anyway would scribble "pristine" bytes over
	// whatever legitimately occupies the address now.
	if !i.stubIntact(i.getPhysicalCursorPosAddr, physStub, physStubWild) {
		return nil
	}
	// Restore EXACTLY the 16 bytes the stub occupies. The old code wrote 21, spilling 5 bytes past
	// the end of this thunk into its neighbour — and these user32 exports are ~6-12 byte thunks
	// packed into 16-byte slots. Pristine spill is not harmless: SetCursorPos sits nearby and is
	// deliberately kept no-op'd for the whole run, so a partial "restore" over it would truncate its
	// jmp mid-instruction and leave a wild branch. Undo only what we did.
	n := len(physStub)
	if b := i.pristineBytes("GetPhysicalCursorPos", n); b != nil {
		return i.writeCode(i.getPhysicalCursorPosAddr, b)
	}
	return i.writeCode(i.getPhysicalCursorPosAddr, i.getPhysicalCursorPosOrigBytes[:n])
}

// OverrideGetCursorInfo patches user32!GetCursorInfo to report a fixed ptScreenPos (at
// CURSORINFO+0x10) — the other common modern cursor read.
func (i *MemoryInjector) OverrideGetCursorInfo(x, y int) error {
	if !i.isLoaded || i.getCursorInfoAddr == 0 {
		return nil
	}
	// push rax; mov rax,rcx; mov [rax+0x10],X; mov [rax+0x14],Y; pop rax; mov al,1; ret
	blob := []byte{0x50, 0x48, 0x89, 0xC8, 0xC7, 0x40, 0x10, 0, 0, 0, 0, 0xC7, 0x40, 0x14, 0, 0, 0, 0, 0x58, 0xB0, 0x01, 0xC3}
	binary.LittleEndian.PutUint32(blob[7:], uint32(x))
	binary.LittleEndian.PutUint32(blob[14:], uint32(y))
	return windows.WriteProcessMemory(i.handle, i.getCursorInfoAddr, &blob[0], uintptr(len(blob)), nil)
}

func (i *MemoryInjector) RestoreGetCursorInfo() error {
	if i.getCursorInfoAddr == 0 {
		return nil
	}
	return windows.WriteProcessMemory(i.handle, i.getCursorInfoAddr, &i.getCursorInfoOrigBytes[0], uintptr(len(i.getCursorInfoOrigBytes)), nil)
}

// InputHookReport is one input function's byte state inside the target process, compared against
// this process's pristine copy of the same system DLL.
type InputHookReport struct {
	Name      string
	Addr      uintptr
	Target    []byte // bytes as they exist in D2R right now
	Pristine  []byte // bytes from our own (unpatched) user32
	Differs   bool
	HookKind  string  // decoded hook shape, if the bytes look like a jump/stub
	HookDest  uintptr // where the hook jumps to, if decodable
	DestOwner string  // module that owns HookDest ("" if unresolved)
}

// ScanInputHooks answers "is anything ALREADY hooking the input functions we patch?" — READ-ONLY:
// it opens no stubs, writes nothing, and suspends nothing. This matters because two modules inline-
// patching the same function is a classic random crash (whoever writes second stomps the other's
// trampoline; whoever restores second writes stale bytes over a live hook). An overlay (amdihk64 =
// AMD Interceptor Hook) hooking the same cursor/keystate exports we do would be exactly that.
//
// System DLLs share an image base across processes, so our own user32 bytes are a valid pristine
// reference — the same assumption HealInput already relies on.
func (i *MemoryInjector) ScanInputHooks() ([]InputHookReport, error) {
	modules, err := memory.GetProcessModules(i.pid)
	if err != nil {
		return nil, err
	}
	// Sort module bases so we can attribute a hook destination to whoever owns that address range
	// (nearest base at or below the target). Approximate but sufficient to name the culprit.
	type modEntry struct {
		name string
		base uintptr
	}
	var mods []modEntry
	var d2rUser32 syscall.Handle
	for _, m := range modules {
		mods = append(mods, modEntry{m.ModuleName, uintptr(m.ModuleHandle)})
		if strings.Contains(strings.ToLower(m.ModuleName), "user32.dll") {
			d2rUser32 = m.ModuleHandle
		}
	}
	if d2rUser32 == 0 {
		return nil, errors.New("target user32.dll not found")
	}
	sort.Slice(mods, func(a, b int) bool { return mods[a].base < mods[b].base })
	ownerOf := func(addr uintptr) string {
		owner := ""
		for _, m := range mods {
			if m.base <= addr {
				owner = m.name
			} else {
				break
			}
		}
		return owner
	}

	own := syscall.MustLoadDLL("USER32.dll")
	names := []string{
		"GetPhysicalCursorPos", "GetCursorPos", "GetCursorInfo", "SetCursorPos",
		"GetKeyState", "GetAsyncKeyState", "GetKeyboardState", "TrackMouseEvent",
	}
	const n = 16
	var out []InputHookReport
	for _, name := range names {
		p, e := own.FindProc(name)
		if e != nil {
			continue
		}
		dst, e2 := syscall.GetProcAddress(d2rUser32, name)
		if p.Addr() == 0 || dst == 0 || e2 != nil {
			continue
		}
		tgt := make([]byte, n)
		if err := windows.ReadProcessMemory(i.handle, uintptr(dst), &tgt[0], n, nil); err != nil {
			continue
		}
		pris := make([]byte, n)
		copy(pris, unsafe.Slice((*byte)(unsafe.Pointer(p.Addr())), n))

		r := InputHookReport{
			Name: name, Addr: uintptr(dst), Target: tgt, Pristine: pris,
			Differs: !bytes.Equal(tgt, pris),
		}
		// Decode the usual inline-hook shapes so a hit names its owner instead of just "differs".
		switch {
		case tgt[0] == 0xE9: // jmp rel32 — relative to the END of the 5-byte instruction
			rel := int32(binary.LittleEndian.Uint32(tgt[1:5]))
			r.HookKind, r.HookDest = "jmp rel32", uintptr(int64(dst)+5+int64(rel))
		case tgt[0] == 0xFF && tgt[1] == 0x25: // jmp [rip+disp32] — indirect via a pointer slot
			r.HookKind = "jmp [rip+disp32]"
			disp := int32(binary.LittleEndian.Uint32(tgt[2:6]))
			slot := uintptr(int64(dst) + 6 + int64(disp))
			var ptr [8]byte
			if windows.ReadProcessMemory(i.handle, slot, &ptr[0], 8, nil) == nil {
				r.HookDest = uintptr(binary.LittleEndian.Uint64(ptr[:]))
			}
		case tgt[0] == 0x48 && tgt[1] == 0xB8: // mov rax, imm64 (our own stub shape, and a common hook)
			r.HookKind, r.HookDest = "mov rax,imm64", uintptr(binary.LittleEndian.Uint64(tgt[2:10]))
		}
		if r.HookDest != 0 {
			r.DestOwner = ownerOf(r.HookDest)
		}
		out = append(out, r)
	}
	return out, nil
}

// HealInput restores D2R's input functions to pristine bytes copied from THIS process's own
// (unpatched) user32, WITHOUT needing a Load() or the captured originals. Use it to recover a
// D2R that was left with GetPhysicalCursorPos/GetCursorPos/etc. patched after an unclean exit
// (which makes the human's mouse read the frozen injected cursor). System DLLs load at the same
// image base across processes, so the code bytes are identical and safe to copy.
func (i *MemoryInjector) HealInput() (int, error) {
	modules, err := memory.GetProcessModules(i.pid)
	if err != nil {
		return 0, err
	}
	var d2rUser32 syscall.Handle
	for _, m := range modules {
		if strings.Contains(strings.ToLower(m.ModuleName), "user32.dll") {
			d2rUser32 = m.ModuleHandle
			break
		}
	}
	if d2rUser32 == 0 {
		return 0, errors.New("D2R user32.dll not found")
	}
	own := syscall.MustLoadDLL("USER32.dll")
	fns := []struct {
		name string
		sz   int
	}{
		{"GetPhysicalCursorPos", 24}, {"GetCursorPos", 24}, {"GetCursorInfo", 24},
		{"SetCursorPos", 16}, {"GetKeyState", 18}, {"GetAsyncKeyState", 18},
		{"GetKeyboardState", 24}, {"TrackMouseEvent", 32},
	}
	// Writes pristine bytes over every input fn, so NO stub survives this — including the keystate
	// stubs. A live farmbot in another process must not assume otherwise; every Override* re-reads
	// its address (stubIntact) rather than trusting a flag, which is what makes running -fixinput
	// against a running bot safe instead of a guaranteed access violation.
	healed := 0
	for _, f := range fns {
		p, e := own.FindProc(f.name)
		if e != nil {
			continue
		}
		ownAddr := p.Addr()
		d2rAddr, e2 := syscall.GetProcAddress(d2rUser32, f.name)
		if ownAddr == 0 || d2rAddr == 0 || e2 != nil {
			continue
		}
		buf := make([]byte, f.sz)
		copy(buf, unsafe.Slice((*byte)(unsafe.Pointer(ownAddr)), f.sz)) // pristine bytes from our own user32
		if err := i.writeCode(uintptr(d2rAddr), buf); err == nil {       // thread-suspended: no restore race
			healed++
			if i.logger != nil {
				i.logger.Info("healed input fn", "fn", f.name)
			}
		}
	}
	return healed, nil
}

// This is needed in order to let the game keep processing mouse events even if the mouse is not over the window
func (i *MemoryInjector) stopTrackingMouseLeaveEvents() error {
	err := windows.ReadProcessMemory(i.handle, i.trackMouseEventAddr, &i.trackMouseEventBytes[0], uintptr(len(i.trackMouseEventBytes)), nil)
	if err != nil {
		return err
	}

	// and dword ptr [rcx+4], 0xFFFFFFFD
	// Modify TRACKMOUSEEVENT struct to disable mouse leave events, since we are injecting our events even if the mouse is not over the window
	disableMouseLeaveRequest := []byte{0x81, 0x61, 0x04, 0xFD, 0xFF, 0xFF, 0xFF}

	// Already hooked
	if bytes.Contains(i.trackMouseEventBytes[:], disableMouseLeaveRequest) {
		return nil
	}

	// We need to move back the pointer 7 bytes to get the correct position, since we are injecting 7 bytes in front of it
	num := int32(binary.LittleEndian.Uint32(i.trackMouseEventBytes[2:6]))
	num -= 7
	// Build the 6-byte inject payload in a FRESH buffer. Do NOT append onto a
	// sub-slice of the [32]byte saved-original array: its cap is 32, so append
	// writes the adjusted displacement back INTO i.trackMouseEventBytes[2:6],
	// corrupting the pristine copy that RestoreMemory writes back on exit — which
	// left D2R's TrackMouseEvent mangled after every "clean" detach (flaky
	// left-click / hover, and a crash risk on the next TrackMouseEvent call).
	injectBytes := make([]byte, 6)
	injectBytes[0] = i.trackMouseEventBytes[0]
	injectBytes[1] = i.trackMouseEventBytes[1]
	binary.LittleEndian.PutUint32(injectBytes[2:], uint32(num))

	hook := append(disableMouseLeaveRequest, injectBytes...)

	return windows.WriteProcessMemory(i.handle, i.trackMouseEventAddr, &hook[0], uintptr(len(hook)), nil)
}
