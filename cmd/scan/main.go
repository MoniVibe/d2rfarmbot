package main

import (
	"fmt"
	"os"
	"time"

	"github.com/hectorgimenez/d2go/pkg/memory"
)

// Standalone pattern scanner: attaches to a running D2R.exe and reports which
// offset signatures are present. Run it while D2R sits in a game to test whether
// the "missing" in-game patterns were merely not decrypted yet at char-select.
func main() {
	fmt.Println("Looking for a running D2R.exe...")
	var p memory.Process
	var err error
	for i := 0; i < 30; i++ {
		p, err = memory.NewProcess()
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		fmt.Println("Could not find/attach D2R.exe:", err)
		fmt.Println("Make sure D2R is running (ideally loaded INTO a game) and rerun this as admin.")
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "-dump" {
		fmt.Println("Attached. Dumping decrypted module to", os.Args[2])
		if err := memory.DumpModule(p, os.Args[2]); err != nil {
			fmt.Println("dump error:", err)
			return
		}
		fmt.Println("dump complete")
		return
	}
	fmt.Println("Attached. Scanning offset patterns:")
	fmt.Println("--------------------------------------------------")
	memory.DiagnosePatterns(p)
}
