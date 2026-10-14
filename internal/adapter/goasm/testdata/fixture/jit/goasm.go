// Package jit is the fixture's consumer of goasm: the two directives
// below are what the adapter reads to learn which package the templates
// generate into. Only the arm64 emitter is committed; the amd64 template
// file has a directive and no generated file, so its Breakpoint edge
// drops at merge while AddLocalsInt resolves to the arm64 method of the
// same name (one declaration, one node — ADR 002).
package jit

//go:generate go run github.com/trendvidia/goasm/cmd/goasm@v1.1.0 -arch arm64 -in templates/arm64.goasm -out emit_arm64.go
//go:generate go run github.com/trendvidia/goasm/cmd/goasm@v1.1.0 -arch amd64 -in templates/amd64.goasm -out emit_amd64.go
