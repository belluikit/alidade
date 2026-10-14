package app

import "testing"

// TestAnalyze reads a constant from a test file: with tests in, the edge
// is a test one.
func TestAnalyze(t *testing.T) {
	if envName == "" {
		t.Fatal("empty")
	}
}
