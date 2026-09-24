package main

import (
	"flag"
	"log/slog"

	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// moddataF is where the mod's excel tables and string files live: the D2RMM
// output root (data/global/excel + data/local/lng/strings) or a flat dump
// (excel/ + strings/). Empty or "off" skips loading.
var moddataF = flag.String("moddata", gamedata.DefaultRoot,
	"the mod's data root for the gamedata layer (item/monster/level/object/skill tables + strings): the D2RMM output (…\\D2RMM.mpq, holding data\\global\\excel and data\\local\\lng\\strings) or a flat dump with excel\\ and strings\\. off = skip; a missing root only warns — the bot runs without it")

// loadGameData loads the tables once and installs them as gamedata.Get() for
// later consumers (loot names, hitboxes, missions). Never fatal.
func loadGameData(logger *slog.Logger, root string) {
	if root == "" || root == "off" || root == "none" {
		logger.Info("gamedata disabled (-moddata off)")
		return
	}
	db, err := gamedata.Load(root)
	if err != nil {
		logger.Warn("gamedata NOT loaded — running without mod tables (names/hitboxes/levels fall back to the built-ins)", "moddata", root, "err", err)
		return
	}
	gamedata.Set(db)
	logger.Info("gamedata loaded: "+db.Summary(), "excel", db.ExcelDir, "strings", db.StringsDir)
}
