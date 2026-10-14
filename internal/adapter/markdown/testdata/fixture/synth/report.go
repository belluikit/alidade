// Package synth is referenced by the docs.
package synth

// Report is unique in the module: `synth.Report` and `Report` both name it.
type Report struct{ Lines int }

// Config exists here and in package other, so a bare `Config` is ambiguous.
type Config struct{ Name string }

// Render is a function the docs mention by its bare name.
func Render(r Report) string { return "" }
