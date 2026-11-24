// Package tools holds small filesystem and path helpers shared across the app.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileExists returns true when path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// DirExists returns true when path exists and is a directory.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// CreateDir creates a directory (and parents) with 0o750 permissions and
// returns its path.
func CreateDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return "", err
	}
	return path, nil
}

// TempFileName builds a unique-ish path under basePath using prefix/suffix.
// When basePath is empty the OS temp dir is used.
func TempFileName(basePath, prefix, suffix string) string {
	if basePath == "" {
		basePath = os.TempDir()
	}
	name := fmt.Sprintf("%s%d%s", prefix, time.Now().UnixNano(), suffix)
	return filepath.Join(basePath, name)
}

// RemoveFolder removes path and all of its contents. A missing path is not an
// error.
func RemoveFolder(path string) error {
	if path == "" {
		return nil
	}
	if !DirExists(path) {
		return nil
	}
	return os.RemoveAll(path)
}
