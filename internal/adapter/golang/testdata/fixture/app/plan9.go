//go:build plan9

package app

// Plan9Only is declared behind a constraint no host here satisfies, so it
// is what the load leaves out and the package node reports (#43).
func Plan9Only() {}
