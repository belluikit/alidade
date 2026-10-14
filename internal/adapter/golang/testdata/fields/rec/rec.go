// Package rec holds one struct field per shape the field nodes must get
// right (#193).
package rec

import "time"

// Base is embedded.
type Base struct{ ID string }

// Node is a linked node: an embedded field, a self-reference, a field of
// a type outside the graph, a blank field and a nested anonymous struct.
type Node struct {
	Base
	Next    *Node
	Created time.Time
	_       int
	Meta    struct{ Owner string }
	limit   int
}

// Alias declares no fields of its own.
type Alias = Node

// Box is generic.
type Box[T any] struct{ Item T }

// Limit reads a field; fields get no uses edges yet (#193 deferred them).
func (n *Node) Limit() int { return n.limit }

// Copy is a defined type over Base: Base's fields are Base's nodes.
type Copy Base

// Keyed is a constraint.
type Keyed interface{ Key() string }

// Holder's field uses T; T's constraint is written in Holder's
// declaration, not the field's.
type Holder[T Keyed] struct{ V T }
