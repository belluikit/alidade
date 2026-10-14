// Package mem is in the nested module, which reaches the root module by a
// replace directive and no go.work.
package mem

import "example.com/multi/core"

// Mem implements core.Store.
type Mem map[string]string

// Get satisfies core.Store.
func (m Mem) Get(key string) string { return m[key] }

// Find calls across the module seam.
func Find(m Mem, k core.Key) string {
	return core.Lookup(m, k)
}
