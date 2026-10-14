//go:build linux && amd64

package app

import "example.com/fixture/shapes"

// LinuxAMD64Only is declared behind a constraint one of the candidate
// configurations satisfies, so Options.Configs "auto" brings it in on a
// host that is not linux/amd64, tagged with the configuration (#43).
func LinuxAMD64Only() float64 { return shapes.Total(nil) }
