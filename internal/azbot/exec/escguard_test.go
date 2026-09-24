package exec

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE ESC GUARD (docs/AZBOT_V2.md, "Guard tests"): every blind ESC on
// 2026-09-23 raised the pause menu or landed in a sub-panel that ate the next
// run's keys. An ESC may be sent only by the janitor (from a positively seen
// panel), by Respawn on a death screen, by the session's single OpenPause ESC
// (relog.go's Perform), and by the few legacy paths below —
// each a ratchet: its count may go down, never up, and each names the
// migration step that deletes it.
//
// Counted per file, outside // comments and _test.go files: "RealEsc(",
// the 0x1B literal, and VK_ESCAPE / VKEscape spellings.
var escPattern = regexp.MustCompile(`RealEsc\(|0[xX]1[bB]\b|VK_ESCAPE|VKEscape`)

// escAllowed: repo-relative path → the most ESC sites it may hold.
var escAllowed = map[string]struct {
	max  int
	note string
}{
	// The janitor's policy and primitives.
	"internal/azbot/screen/close.go": {3, "janitor policy: VKEscape = 0x1B, CloseStep's ESC for a seen panel"},
	"internal/azbot/motor/motor.go":  {2, "the primitive: RealEsc's definition"},

	// Respawn: kept by design ("only when Mode=Dead" with the janitor on).
	// TODO(step 8, Session FSM): the OFF-path respawn press goes with the legacy executive.
	"internal/azbot/activity/activity.go": {1, "Respawn: death-screen ESC"},

	// Step 8 (Session FSM + Relog): the ESC ritual (×3 per attempt, a closing
	// ESC between attempts) became the session's Relogging phases. What stays
	// is the motor half of the ONE OpenPause ESC, which exec.Session asks for
	// only on a stable Reading that is World with nothing blocking.
	"internal/azbot/activity/relog.go": {1, "the session's OpenPause ESC (Relog.Perform)"},

	// Step 7 retired safeEsc (pause.go) and Equip/Spend closePanel
	// (services.go): the town services close their own panels by sight via
	// screen.CloseStep (servicelife.go tidy), which holds no ESC literal.

	// cmd/azbot/main.go, janitor-OFF legacy executive and manual harnesses
	// (step 9 deleted the watchdog ESC probe pair):
	// TODO(step 6 close-out: delete the janitor-OFF path once the stray panel
	// drill passes): the cursor-drop RealEsc;
	// TODO(step 10, Calibrate): -wpcaltest's lane ESC after photographing the waypoint panel.
	// (step 8 turned -relogtest into the session drill and took its ESC pair.)
	"cmd/azbot/main.go": {2, "legacy OFF path + harnesses"},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = up
	}
}

// escSites counts ESC sites in Go source, ignoring // comments.
func escSites(src string) int {
	n := 0
	for _, ln := range strings.Split(src, "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		n += len(escPattern.FindAllString(ln, -1))
	}
	return n
}

func TestEscGuard(t *testing.T) {
	root := repoRoot(t)
	found := map[string]int{}
	for _, dir := range []string{"internal/azbot", "cmd/azbot"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := escSites(string(b)); n > 0 {
				rel, _ := filepath.Rel(root, path)
				found[filepath.ToSlash(rel)] = n
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
		a, ok := escAllowed[f]
		switch {
		case !ok:
			t.Errorf("%s: %d ESC site(s) outside the allowlist — ESC belongs to the janitor (exec.Gate/screen.CloseStep), Respawn and Relog only", f, n)
		case n > a.max:
			t.Errorf("%s: %d ESC sites, allowlist says at most %d (%s) — the ratchet only goes down", f, n, a.max, a.note)
		case n < a.max:
			t.Logf("%s: %d ESC sites, allowlist %d — lower the ratchet", f, n, a.max)
		}
	}
}

func TestEscSitesIgnoresComments(t *testing.T) {
	src := "a := 1 // m.RealEsc() here is prose\nm.RealEsc()\nx.Press(0x1B)\nvk := screen.VKEscape\n"
	if n := escSites(src); n != 3 {
		t.Fatalf("escSites = %d, want 3", n)
	}
}
