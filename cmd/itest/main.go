package main

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Standalone probe: does Interception injection actually reach the OS input stream?
// Injects N via all keyboard device slots and checks GetAsyncKeyState.

var (
	dll       = windows.NewLazyDLL("interception.dll")
	createCtx = dll.NewProc("interception_create_context")
	sendProc  = dll.NewProc("interception_send")
	user32    = windows.NewLazySystemDLL("user32.dll")
	getAsync  = user32.NewProc("GetAsyncKeyState")
	mapVK     = user32.NewProc("MapVirtualKeyW")
)

type keyStroke struct {
	code  uint16
	state uint16
	info  uint32
}

func main() {
	if err := dll.Load(); err != nil {
		fmt.Println("dll load fail:", err)
		return
	}
	ctx, _, _ := createCtx.Call()
	fmt.Printf("context=0x%x\n", ctx)
	if ctx == 0 {
		fmt.Println("no context — driver not active")
		return
	}
	vk := uintptr(0x4E) // N
	sc, _, _ := mapVK.Call(vk, 0)
	fmt.Printf("N scan=0x%x\n", sc)

	st0, _, _ := getAsync.Call(vk)
	fmt.Printf("before:     GetAsyncKeyState(N)=0x%x\n", uint16(st0))

	down := keyStroke{code: uint16(sc), state: 0}
	for dev := 1; dev <= 10; dev++ {
		r, _, _ := sendProc.Call(ctx, uintptr(dev), uintptr(unsafe.Pointer(&down)), 1)
		if r != 0 {
			fmt.Printf("down -> dev %d ret=%d\n", dev, r)
		}
	}
	time.Sleep(80 * time.Millisecond)
	st1, _, _ := getAsync.Call(vk)
	fmt.Printf("AFTER down: GetAsyncKeyState(N)=0x%x   <-- 0x8000 bit set means injection REACHED the OS input stream\n", uint16(st1))

	up := keyStroke{code: uint16(sc), state: 1}
	for dev := 1; dev <= 10; dev++ {
		sendProc.Call(ctx, uintptr(dev), uintptr(unsafe.Pointer(&up)), 1)
	}
	time.Sleep(50 * time.Millisecond)
	st2, _, _ := getAsync.Call(vk)
	fmt.Printf("AFTER up:   GetAsyncKeyState(N)=0x%x\n", uint16(st2))
}
