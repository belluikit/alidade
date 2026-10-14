// Package core is the root module's package the nested module builds on.
package core

// Store is what the nested module's Mem implements.
type Store interface {
	Get(key string) string
}

// Key is a named type the nested module's signatures mention.
type Key string

// Lookup is what the nested module calls.
func Lookup(s Store, k Key) string {
	return s.Get(string(k))
}
