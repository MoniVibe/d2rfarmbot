# Build profiles

One file per character: `profiles/<Character>.yaml` (the in-game name).
Without a file the bot plays the character from what its hotkeys hold: every
bound skill gets a role from the combat priors (strike, trap, buff, summon,
leap). A profile states the build's intent where those defaults would guess.
All fields are optional.

```yaml
style: summoner hit-and-run   # the build in words (logged)
traps: 5                      # sentries wanted at a pack; 0 = never lay traps
summons:                      # summon skill (in-game name) -> count kept alive; 0 = never
  Raise Skeleton: 8
  Raise Skeletal Mage: 4
  Clay Golem: 1
```

Summon defaults (`internal/azbot/combat/priors.go`): Raise Skeleton 5,
Skeletal Mage 3, golems 1, Raven 5, Spirit Wolf 3, Dire Wolf 3, Grizzly 1,
spirits 1, vines 1, Valkyrie 1, Shadow Warrior/Master 1. Skeletons and mages
are raised from corpses; druid spirits, vines and golems keep one of their
group at a time (the first bound skill).
