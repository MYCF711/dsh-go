//go:build windows

package native

import (
	"os"
	"path/filepath"
)

func isAbs(path string) bool { return filepath.IsAbs(path) }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
