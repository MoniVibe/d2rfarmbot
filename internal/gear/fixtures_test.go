package gear

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

// Inline table fixtures. These deliberately use a SUBSET of the real columns —
// the parser must access columns by header name and tolerate missing ones.

const fixtureItemTypes = `ItemType	Code	Equiv1	Equiv2	Body	BodyLoc1	BodyLoc2	MaxSockets3
Any
Armor	armo			0			6
Helm	helm	armo		1	head	head	3
Armor	tors	armo		1	tors	tors	6
Shield	shie	armo		1	rarm	larm	4
Ring	ring			1	rrin	lrin	0
Amulet	amul			1	neck	neck	0
Gem	gem			0			0
Amethyst	gema	gem		0			0
Misc	misc			0			0
`

const fixtureArmor = `name	code	type	reqstr	reqdex	levelreq	minac	maxac	gemsockets	gemapplytype
Cap	cap	helm	0	0	1	3	5	2	1
Great Helm	ghm	helm	63	0	23	30	35	3	1
Buckler	buc	shie	12	0	1	4	6	1	2
Quilted Armor	qui	tors	12	0	1	8	11	2	1
`

const fixtureWeapons = `name	code	type	reqstr	reqdex	levelreq	gemsockets	gemapplytype
Wand	wnd	wand	0	0	1	1	0
`

const fixtureMisc = `name	code	type	reqstr	reqdex	levelreq	gemsockets	gemapplytype
Amulet	amu	amul	0	0	1	0	0
Ring	rin	ring	0	0	1	0	0
Chipped Amethyst	gcv	gema	0	0	1	0	0
Flawed Amethyst	gfv	gema	0	0	1	0	0
Perfect Amethyst	gpv	gema	0	0	1	0	0
Orb of Corruption	ka3	misc	0	0	1	0	0
`

const fixtureGems = `name	letter	transform	code	weaponMod1Code	weaponMod1Param	weaponMod1Min	weaponMod1Max	helmMod1Code	helmMod1Param	helmMod1Min	helmMod1Max	shieldMod1Code	shieldMod1Param	shieldMod1Min	shieldMod1Max
Chipped Amethyst		18	gcv	att	0	40	40	str	0	3	3	ac	0	8	8
Flawed Amethyst		18	gfv	att	0	60	60	str	0	4	4	ac	0	12	12
Perfect Amethyst		18	gpv	att	0	150	150	str	0	10	10	ac	0	30	30
`

// Recipes: one deterministic (3 chipped -> 1 flawed), one corruption-style
// gamble (magic amulet + orb -> useitem with op gate), one disabled, one
// comment, one with a name-based input the parser must skip and count.
const fixtureCubemain = `description	enabled	version	op	param	value	numinputs	input 1	input 2	input 3	output	lvl	plvl	ilvl	mod 1	mod 1 chance	mod 1 param	mod 1 min	mod 1 max
3 Chipped Amethysts = Flawed Amethyst	1	100	0	0	0	3	"gcv,qty=3"			gfv
Corrupt Magic Amulet	1	100	18	361	0	2	"amu,mag"	ka3		useitem	0	0	100	sock	20	0	1	1
Disabled recipe	0	100	0	0	0	1	gcv			gfv
* comment line	1	100	0	0	0	1	gcv			gfv
Torch trade	1	100	0	0	0	2	Hellfire Torch	ka3		useitem
Socket a cap	1	100	0	0	0	2	"cap,nor,nos"	gpv		"cap,sock=1"	0	0	0	ac	100	0	10	10
`

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// writeFixtureDir materializes the fixture tables into a temp excel dir.
func writeFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"itemtypes.txt": fixtureItemTypes,
		"armor.txt":     fixtureArmor,
		"weapons.txt":   fixtureWeapons,
		"misc.txt":      fixtureMisc,
		"gems.txt":      fixtureGems,
		"cubemain.txt":  fixtureCubemain,
	}
	for name, content := range files {
		// Fixtures use spaces for readability nowhere — they are real tabs.
		if !strings.Contains(content, "\t") {
			t.Fatalf("fixture %s lost its tabs", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func loadFixtureTables(t *testing.T) *Tables {
	t.Helper()
	tb, err := LoadTables(writeFixtureDir(t))
	if err != nil {
		t.Fatalf("LoadTables: %v", err)
	}
	return tb
}

// ---------------------------------------------------------------------------
// synthetic d2go items
//
// data.Item.Desc() resolves through the package-global item.Desc map, so
// tests register synthetic descriptions under IDs far above the real range
// and remove them on cleanup.

const testIDBase = 910000

var nextTestID = testIDBase

func newTestItem(t *testing.T, code, name string, quality item.Quality, stats ...stat.Data) data.Item {
	t.Helper()
	nextTestID++
	id := nextTestID
	item.Desc[id] = item.Description{
		ID:   id,
		Name: name,
		Code: code,
	}
	t.Cleanup(func() { delete(item.Desc, id) })
	return data.Item{
		ID:         id,
		UnitID:     data.UnitID(id),
		Name:       item.Name(name),
		Quality:    quality,
		Identified: true,
		Stats:      stat.Stats(stats),
	}
}

func withDesc(t *testing.T, it data.Item, mutate func(*item.Description)) data.Item {
	t.Helper()
	d := item.Desc[it.ID]
	mutate(&d)
	item.Desc[it.ID] = d
	return it
}
