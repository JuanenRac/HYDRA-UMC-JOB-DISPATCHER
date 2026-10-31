// HYDRA-UMC-JOB-DISPATCHER - location costs: which robot is "closest" to the place a job asks for
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"fmt"
	"math"
	"sort"
)

// LocationConfig switches on the choice of the closest robot by a declared table of costs. The
// zero value is "off": a job that names a location then prefers the robots in exactly that
// location, as it always did. Nothing in the table is built into the code: the costs (a distance,
// a travel time, any non-negative unit) come from the configuration file.
type LocationConfig struct {
	// Enabled turns the table on. Off by default.
	Enabled bool
	// Costs[from][to] is the cost of bringing work from one location to another. The table is
	// read both ways: when only Costs[b][a] is declared it also serves for (a, b). A location is
	// at cost 0 from itself. A pair that is not declared has an unknown cost, which ranks after
	// every known one.
	Costs map[string]map[string]float64
}

// Validate refuses a table with a negative, infinite or not-a-number cost.
func (c LocationConfig) Validate() error {
	froms := make([]string, 0, len(c.Costs))
	for from := range c.Costs {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	for _, from := range froms {
		tos := make([]string, 0, len(c.Costs[from]))
		for to := range c.Costs[from] {
			tos = append(tos, to)
		}
		sort.Strings(tos)
		for _, to := range tos {
			v := c.Costs[from][to]
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("location cost %q -> %q must be a finite number that is not negative, got %v", from, to, v)
			}
		}
	}
	return nil
}

// cost returns the declared cost between two locations and whether it is known.
func (c LocationConfig) cost(a, b string) (float64, bool) {
	if a == b {
		return 0, true
	}
	if v, ok := c.Costs[a][b]; ok {
		return v, true
	}
	if v, ok := c.Costs[b][a]; ok {
		return v, true
	}
	return 0, false
}

// SetLocationCosts installs the table of location costs (the zero value switches it off). A
// table with an invalid cost is refused and the previous setting is kept.
func (e *Engine) SetLocationCosts(c LocationConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// a private copy, so the caller changing its map later cannot change the engine
	copied := LocationConfig{Enabled: c.Enabled}
	if c.Costs != nil {
		copied.Costs = make(map[string]map[string]float64, len(c.Costs))
		for from, row := range c.Costs {
			r := make(map[string]float64, len(row))
			for to, v := range row {
				r[to] = v
			}
			copied.Costs[from] = r
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.location = copied
	return nil
}

// preferLocked reports whether robot r is a better choice than `than` for job j (than == nil: always).
// With the cost table on and a location named, the lower known cost wins (an unknown cost loses to
// a known one); everything else, and every tie, is decided as it always was: the lower Load, then
// the lower ID. Caller must hold e.mu.
func (e *Engine) preferLocked(j *Job, r, than *Robot) bool {
	if than == nil {
		return true
	}
	if e.location.Enabled && j.Location != "" {
		cr, okr := e.location.cost(j.Location, r.Location)
		ct, okt := e.location.cost(j.Location, than.Location)
		if okr != okt {
			return okr
		}
		if okr && cr != ct {
			return cr < ct
		}
	}
	return r.Load < than.Load || (r.Load == than.Load && r.ID < than.ID)
}
