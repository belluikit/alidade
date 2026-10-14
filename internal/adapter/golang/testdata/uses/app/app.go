// Package app holds one use of a package-level constant or variable per
// shape the references edge must get right, and the near misses it must
// not count (#180).
package app

import (
	"os"

	"example.com/uses/cfg"
)

// envName is read by Analyze: the case that opened #180.
const envName = "APP_BINARY"

// limit is a package variable a field of the same name must not be
// mistaken for.
var limit = 10

// derived is initialised from envName: a package-level initializer, which
// the edge does not cover.
var derived = envName

// counter is written, not read: a use all the same.
var counter int

// Analyze reads a constant of its own package.
func Analyze() string { return os.Getenv(envName) }

// Shadow declares a local named like the constant and reads only that.
func Shadow() string {
	envName := "local"
	return envName
}

// Server has a field named like the package variable.
type Server struct{ limit int }

// Limit reads the field, not the package variable.
func (s *Server) Limit() int { return s.limit }

// Default reads the package variable from a method.
func (s *Server) Default() int { return limit }

// Later reads a package variable inside a closure: the use is Later's.
func Later() func() int { return func() int { return limit } }

// Remote reads another package's variable and constant.
func Remote() (int, string) { return cfg.Timeout, cfg.Mode }

// Args reads a dependency's variable, which has no node.
func Args() []string { return os.Args }

// Count writes a package variable.
func Count() { counter++ }

// Box is generic; its method reads a constant.
type Box[T any] struct{ v T }

// Name reads a constant from a generic type's method.
func (b Box[T]) Name() string { return envName }

func init() { counter = limit }
