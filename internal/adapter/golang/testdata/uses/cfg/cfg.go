// Package cfg holds package-level values another package reads (#180).
package cfg

// Timeout is read from another package of the module.
var Timeout = 30

// Mode is a typed constant read through a type switch.
const Mode = "fast"
