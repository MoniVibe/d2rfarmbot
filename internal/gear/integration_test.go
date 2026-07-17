package gear

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRealTables parses the actual mod data when it is installed on this
// machine, guarded by a file-existence check so CI machines without the game
// skip it. The mod ships ~15.8k enabled cubemain rows; we require the parser
// to understand the overwhelming majority of them.
func TestLoadRealTables(t *testing.T) {
	if _, err := os.Stat(filepath.Join(DefaultExcelDir, "cubemain.txt")); err != nil {
		t.Skipf("real game tables not present: %v", err)
	}

	tb, err := LoadTables(DefaultExcelDir)
	if err != nil {
		t.Fatalf("LoadTables(real): %v", err)
	}

	t.Logf("items=%d types=%d gems=%d recipes parsed=%d skipped=%d",
		tb.Stats.ItemsLoaded, tb.Stats.TypesLoaded, tb.Stats.GemsLoaded,
		tb.Stats.RecipesParsed, tb.Stats.RecipesSkipped)
	for reason, n := range tb.Stats.SkipReasons {
		t.Logf("  skip %4d  %s", n, reason)
	}

	if tb.Stats.RecipesParsed <= 10000 {
		t.Errorf("RecipesParsed = %d, want > 10000", tb.Stats.RecipesParsed)
	}
	if tb.Stats.ItemsLoaded < 500 {
		t.Errorf("ItemsLoaded = %d, suspiciously low", tb.Stats.ItemsLoaded)
	}
	if tb.Stats.GemsLoaded < 30 {
		t.Errorf("GemsLoaded = %d, suspiciously low", tb.Stats.GemsLoaded)
	}

	// Sanity: the type tree must resolve classic relationships.
	if !tb.IsA("cap", "helm") || !tb.IsA("cap", "armo") {
		t.Error("type tree broken: cap should be helm and armo")
	}
	if tb.BodySlot("cap") == "" {
		t.Error("BodySlot(cap) empty on real tables")
	}
}
