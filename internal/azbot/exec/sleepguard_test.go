package exec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// THE SLEEP RATCHET (docs/AZBOT_V2.md, "Guard tests"): Step is ≤120ms and a
// long wait is Status{V: Wait, WakeAt}, never a sleep — a sleeping holder is
// deaf to survival for the whole nap. Each file under internal/azbot/activity
// may hold at most the time.Sleep calls recorded here; the count only goes
// down. A file missing from the map may hold none.
//
// Counted with go/ast (calls to time.Sleep, _test.go files excluded), so
// comments and strings never count.
var sleepAllowed = map[string]int{
	// Short settles inside one click sequence (<150ms) may stay; everything
	// else is on its way to Wait. Step 7 took the town services to zero
	// (services.go 23, shopgrid.go 2, pause.go's safeEsc 2 — 50 → 23 in all).
	"activity.go":   2,
	"advance.go":    10, // TODO(step 11): Advance onto nav
	"hovertrack.go": 1,
	"imbibe.go":     1,
	"pause.go":      1, // EnsureWorld's click peel (janitor-OFF legacy)
	"reclaim.go":    1,
	"relog.go":      5, // TODO(step 8): Session Relogging phases
	"selftest.go":   2, // the manual -selftest harness
}

// sleepCalls counts time.Sleep calls in one Go source file.
func sleepCalls(t *testing.T, path string) int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The local name of the "time" import (usually "time").
	name := ""
	for _, im := range f.Imports {
		if im.Path.Value == `"time"` {
			name = "time"
			if im.Name != nil {
				name = im.Name.Name
			}
		}
	}
	if name == "" {
		return 0
	}
	n := 0
	ast.Inspect(f, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				n++
			}
		}
		return true
	})
	return n
}

func TestSleepRatchet(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal/azbot/activity")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	var files []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		files = append(files, n)
	}
	sort.Strings(files)
	for _, f := range files {
		n := sleepCalls(t, filepath.Join(dir, f))
		total += n
		max := sleepAllowed[f]
		switch {
		case n > max:
			t.Errorf("activity/%s: %d time.Sleep calls, ratchet allows %d — a long wait is Status{V: Wait, WakeAt}, not a sleep", f, n, max)
		case n < max:
			t.Logf("activity/%s: %d time.Sleep calls, ratchet %d — lower the ratchet", f, n, max)
		}
	}
	t.Logf("activity: %d time.Sleep calls in total", total)
}

func TestSleepCallsCountsOnlyCalls(t *testing.T) {
	src := "package x\n\nimport tm \"time\"\n\n// time.Sleep(1) in prose\nfunc f() {\n\ttm.Sleep(1)\n\t_ = \"time.Sleep(2)\"\n\tg := tm.Sleep\n\t_ = g\n\ttm.Sleep(tm.Millisecond)\n}\n"
	p := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := sleepCalls(t, p); n != 2 {
		t.Fatalf("sleepCalls = %d, want 2", n)
	}
}
