package mem

import (
	"testing"

	"example.com/multi/core"
)

// fake implements core.Store from a test file, so the implements edge
// across the module seam is a test one.
type fake map[string]string

// Get satisfies core.Store.
func (f fake) Get(key string) string { return f[key] }

// seed's signature names core.Key, so its references edge across the seam
// is a test one; its body calls across the seam, so is its calls edge.
func seed(k core.Key) string { return core.Lookup(fake{string(k): "x"}, k) }

func TestFind(t *testing.T) {
	if seed("a") != "x" || Find(Mem{"a": "b"}, "a") != "b" {
		t.Fatal("across the module seam")
	}
}
