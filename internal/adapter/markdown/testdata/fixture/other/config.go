// Package other declares a second Config.
package other

// Config collides with synth.Config by bare name.
type Config struct{ Path string }
