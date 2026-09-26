package exec

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE MOVE GUARD (docs/AZBOT_V3_FOUNDATIONS.md, A3: one MoveTo, ratchet test):
// the character moves through ONE planned mover, moveto.MoveTo. Only the mover
// itself, the journey it drives, and the verbs package (the primitives) may
// build locomotion by hand:
//
//   - a Stride literal (verbs.Stride{…} — a force-move stride),
//   - a ClickMove literal (the click gait),
//   - journey.New( (a route owned outside the mover),
//   - motor StrideEdge( (the raw key-edge primitive).
//
// Everything else calls moveTo / drillMove. Counted per file, outside //
// comments and _test.go files. A file outside the allowed packages may hold
// no site unless moveAllowed lists it — each listed count is a ratchet: it
// may go down, never up.
var movePattern = regexp.MustCompile(`\bStride\{|\bClickMove\{|\bjourney\.New\(|\.StrideEdge\(`)

// moveOwners: packages whose files may build locomotion freely.
var moveOwners = []string{
	"internal/azbot/moveto/",
	"internal/azbot/journey/",
	"internal/azbot/verbs/",
}

// moveAllowed: repo-relative path → the most sites it may hold (justified
// exceptions only).
var moveAllowed = map[string]struct {
	max  int
	note string
}{
	// The Stride verb's own calibration harness: it measures the raw primitive
	// MoveTo is built on (hold → displacement, reach, heading error), so it
	// must drive verbs.Stride directly. TODO(none while the verb exists):
	// retire together with the verb, never migrate onto MoveTo.
	"cmd/stridecal/main.go": {1, "stridecal: the raw verb's measurement harness"},
}

// moveSites counts hand-built locomotion in Go source, ignoring // comments.
func moveSites(src string) int {
	n := 0
	for _, ln := range strings.Split(src, "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		n += len(movePattern.FindAllString(ln, -1))
	}
	return n
}

func TestMoveGuard(t *testing.T) {
	root := repoRoot(t)
	found := map[string]int{}
	for _, dir := range []string{"internal/azbot", "cmd/azbot", "cmd/stridecal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, own := range moveOwners {
				if strings.HasPrefix(rel, own) {
					return nil
				}
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := moveSites(string(b)); n > 0 {
				found[rel] = n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		n := found[f]
		a, ok := moveAllowed[f]
		switch {
		case !ok:
			t.Errorf("%s: %d hand-built movement site(s) — the character moves through moveto.MoveTo only (activity: moveTo; drills: drillMove)", f, n)
		case n > a.max:
			t.Errorf("%s: %d movement sites, allowlist says at most %d (%s) — the ratchet only goes down", f, n, a.max, a.note)
		case n < a.max:
			t.Logf("%s: %d movement sites, allowlist %d — lower the ratchet", f, n, a.max)
		}
	}
	for f, a := range moveAllowed {
		if _, ok := found[f]; !ok && a.max > 0 {
			t.Logf("%s: no movement sites left, allowlist %d — delete the entry", f, a.max)
		}
	}
}

func TestMoveSitesPattern(t *testing.T) {
	src := "// verbs.Stride{To: x} is prose\n" +
		"verbs.Stride{To: x}.Do(m, gr, p, led, who)\n" +
		"o := verbs.ClickMove{To: x}\n" +
		"j := journey.New(gr, g, goal, who)\n" +
		"m.StrideEdge(1, 2)\n" +
		"type Stride struct {\n" + // the type itself is no site
		"moveTo(ctx, goal, moveto.Opts{})\n"
	if n := moveSites(src); n != 4 {
		t.Fatalf("moveSites = %d, want 4", n)
	}
}
