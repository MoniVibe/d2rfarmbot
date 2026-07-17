package astar

import (
	"container/heap"
	"math"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

var directions = []data.Position{
	{X: 0, Y: 1}, {X: 1, Y: 0}, {X: 0, Y: -1}, {X: -1, Y: 0}, // Main directions
	{X: 1, Y: 1}, {X: -1, Y: 1}, {X: 1, Y: -1}, {X: -1, Y: -1}, // Diagonal directions
}

type Node struct {
	data.Position
	Cost     int
	Priority int
	Index    int
	Parent   *Node // NEW: For easier tracking
}

func direction(from, to data.Position) (dx, dy int) {
	dx = to.X - from.X
	dy = to.Y - from.Y
	return
}

func CalculatePath(g *game.Grid, start, goal data.Position) ([]data.Position, int, bool) {
	pq := make(PriorityQueue, 0)
	heap.Init(&pq)

	// Using map to store already visited nodes for better efficiency
	nodes := make(map[data.Position]*Node)

	startNode := &Node{Position: start, Cost: 0, Priority: heuristic(start, goal)}
	nodes[start] = startNode
	heap.Push(&pq, startNode)

	for pq.Len() > 0 {
		current := heap.Pop(&pq).(*Node)

		if current.Position == goal {
			var path []data.Position
			p := current
			for p != nil {
				path = append([]data.Position{p.Position}, path...)
				p = p.Parent
			}
			return path, len(path), true
		}

		// Generating neighbors
		for _, dir := range directions {
			neighborPos := data.Position{X: current.X + dir.X, Y: current.Y + dir.Y}

			// Check if neighbor is in map range
			if neighborPos.X < 0 || neighborPos.X >= g.Width || neighborPos.Y < 0 || neighborPos.Y >= g.Height {
				continue
			}

			// Check for corner cutting
			if dir.X != 0 && dir.Y != 0 { // Ако е диагонал
				// Additional checks around.
				if g.CollisionGrid[current.Y][current.X+dir.X] > game.CollisionTypeWalkable || g.CollisionGrid[current.Y+dir.Y][current.X] > game.CollisionTypeWalkable {
					continue
				}
			}

			// Calculate prices to move to neighbor
			newCost := current.Cost + getCost(g, neighborPos)

			// Added "punishment" for changing directions to eliminate zig-zag
			if current.Parent != nil {
				curDirX, curDirY := direction(current.Parent.Position, current.Position)
				newDirX, newDirY := direction(current.Position, neighborPos)
				if curDirX != newDirX || curDirY != newDirY {
					newCost++ // Punishment for turning
				}
			}

			// If we found a better path to this neighbor
			if node, found := nodes[neighborPos]; !found || newCost < node.Cost {
				priority := newCost + heuristic(neighborPos, goal)
				neighborNode := &Node{
					Position: neighborPos,
					Cost:     newCost,
					Priority: priority,
					Parent:   current,
				}
				nodes[neighborPos] = neighborNode
				heap.Push(&pq, neighborNode)
			}
		}
	}

	return nil, 0, false
}

// Changed: getCost calculates neighbors too
func getCost(grid *game.Grid, pos data.Position) int {
	baseCost := 0
	tileType := grid.CollisionGrid[pos.Y][pos.X]

	switch tileType {
	case game.CollisionTypeWalkable:
		baseCost = 1
	case game.CollisionTypeMonster:
		baseCost = 16
	case game.CollisionTypeObject:
		baseCost = 4
	case game.CollisionTypeLowPriority:
		baseCost = 20
	default:
		return math.MaxInt32 // blocked
	}

	// CHANGED: Add "penalty" for proximity to walls/obstacles
	// This makes the path stay further away from walls
	for _, d := range directions {
		checkPos := data.Position{X: pos.X + d.X, Y: pos.Y + d.Y}
		if checkPos.X >= 0 && checkPos.X < grid.Width && checkPos.Y >= 0 && checkPos.Y < grid.Height {
			if grid.CollisionGrid[checkPos.Y][checkPos.X] == game.CollisionTypeNonWalkable {
				baseCost += 2 // We add an additional cost if it is next to a wall
			}
		}
	}

	return baseCost
}

func heuristic(a, b data.Position) int {
	// 
	dx := math.Abs(float64(a.X - b.X))
	dy := math.Abs(float64(a.Y - b.Y))
	return int(dx + dy)
}