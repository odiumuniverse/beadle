package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const homePrefix = "~/"

func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, homePrefix) {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	if path == "~" {
		return home, nil
	}

	return filepath.Join(home, strings.TrimPrefix(path, homePrefix)), nil
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
