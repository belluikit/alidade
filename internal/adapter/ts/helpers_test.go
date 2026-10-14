// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"os"
	"path/filepath"
)

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
