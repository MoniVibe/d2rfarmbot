package action

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

func Repair() error {
	ctx := context.Get()
	ctx.SetLastAction("Repair")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		// var
		triggerRepair := false
		logMessage := ""

		_, indestructible := i.FindStat(stat.Indestructible, 0)
		quantity, quantityFound := i.FindStat(stat.Quantity, 0)

		// skip indestructible and no quantity
		if indestructible && !quantityFound {
			continue
		}

		// skip eth and no qnt
		if i.Ethereal && !quantityFound {
			continue
		}

		// qantity check
		if quantityFound {
			// Low quantity (under 15) or broken (quantity 0)
			if quantity.Value < 15 || i.IsBroken {
				triggerRepair = true
				logMessage = fmt.Sprintf("Replenishing %s, quantity is %d", i.Name, quantity.Value)
			}
		} else {
			// check item durability (Durability)
			durability, found := i.FindStat(stat.Durability, 0)
			maxDurability, maxDurabilityFound := i.FindStat(stat.MaxDurability, 0)
			durabilityPercent := -1

			if maxDurabilityFound && found {
				durabilityPercent = int((float64(durability.Value) / float64(maxDurability.Value)) * 100)
			}

			// if item is broken or under 20%
			if i.IsBroken || (durabilityPercent != -1 && durabilityPercent <= 20) {
				triggerRepair = true
				logMessage = fmt.Sprintf("Repairing %s, item durability is %d percent", i.Name, durabilityPercent)
			}
		}

		// trigger
		if triggerRepair {
			ctx.Logger.Info(logMessage)

			repairNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RepairNPC()
			if repairNPC == npc.Hratli {
				MoveToCoords(data.Position{X: 5224, Y: 5045})
			}

			if err := InteractNPC(repairNPC); err != nil {
				return err
			}

			if repairNPC != npc.Halbu {
				ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
			} else {
				ctx.HID.KeySequence(win.VK_HOME, win.VK_RETURN)
			}

			utils.Sleep(100)
			if ctx.Data.LegacyGraphics {
				ctx.HID.Click(game.LeftButton, ui.RepairButtonXClassic, ui.RepairButtonYClassic)
			} else {
				ctx.HID.Click(game.LeftButton, ui.RepairButtonX, ui.RepairButtonY)
			}
			utils.Sleep(500)

			return step.CloseAllMenus()
		}
	}

	return nil
}

func RepairRequired() bool {
	ctx := context.Get()
	ctx.SetLastAction("RepairRequired")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		_, indestructible := i.FindStat(stat.Indestructible, 0)
		quantity, quantityFound := i.FindStat(stat.Quantity, 0)

		if indestructible && !quantityFound {
			continue
		}

		if i.Ethereal && !quantityFound {
			continue
		}

		// qnt check
		if quantityFound {
			if quantity.Value < 15 || i.IsBroken {
				return true
			}
		} else {
			// durability check
			durability, found := i.FindStat(stat.Durability, 0)
			maxDurability, maxDurabilityFound := i.FindStat(stat.MaxDurability, 0)
			
			if i.IsBroken || (maxDurabilityFound && !found) {
				return true
			}

			if found && maxDurabilityFound {
				durabilityPercent := int((float64(durability.Value) / float64(maxDurability.Value)) * 100)
				if durabilityPercent <= 20 {
					return true
				}
			}
		}
	}

	return false
}

func IsEquipmentBroken() bool {
	ctx := context.Get()
	ctx.SetLastAction("EquipmentBroken")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		_, indestructible := i.FindStat(stat.Indestructible, 0)
		_, quantityFound := i.FindStat(stat.Quantity, 0)
		
		// eth quantity
		if i.Ethereal && !quantityFound {
			continue
		}
		
		// indestructible with quantity
		if indestructible && !quantityFound {
			continue
		}

		// Check if item is broken, works for quantity if its 0.
		if i.IsBroken {
			ctx.Logger.Debug("Equipment is broken, returning to town", "item", i.Name)
			return true
		}
	}

	return false
}