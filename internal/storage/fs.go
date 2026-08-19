package storage

import (
	"fmt"
	"os"
)

// ensureDir creates the directory holding the database file if it is missing.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}
