package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const homePrefix = "~/"

// Under reports whether path is root itself or lies inside it, comparing whole
// path components and nothing shorter.
//
// The prefix form this replaces — strings.HasPrefix(path, root) — answers a
// question nobody asked. With home /Users/bob it calls /Users/bobby/x "inside
// /Users/bob", and with a vault root of /x/vw-a it calls /x/vw-ab a child. The
// error is not cosmetic: a containment check that is too generous reads files
// and prunes directories that belong to a neighbour of the root, and the
// neighbour's name is exactly what the shorter prefix cannot see.
//
// Both sides are cleaned first, so a root spelled with a trailing separator or a
// "." segment compares the same as the canonical spelling of itself.
func Under(root, path string) bool {
	r, p := filepath.Clean(root), filepath.Clean(path)

	return p == r || strings.HasPrefix(p, r+string(filepath.Separator))
}

// Root is the one way beadle turns a directory root into a comparable path.
//
// Every root beadle uses arrives from outside the program: an environment
// variable, os.UserHomeDir, os.TempDir. macOS hands TMPDIR over with a trailing
// separator and mktemp doubles it, so "/var/…/T//t-XXXX" is a perfectly ordinary
// spelling of the same directory as "/var/…/T/t-XXXX". Nothing cleans those — not
// os.MkdirTemp, not t.TempDir, not filepath.Join on the way down — so a cleaned
// path built from the root and the uncleaned root itself end up side by side and
// every comparison between them is false. That is not cosmetic: the doctor's
// shortening and the home-isolation checks both decide on the comparison.
//
// Clean at the read, once, here: a root is cleaned when beadle takes it in, not
// at each of the dozen places that later compare against it.
func Root(path string) string {
	if path == "" {
		return ""
	}

	return filepath.Clean(path)
}

// RootEnv is Root applied to an environment variable, and the only way beadle
// reads one of these. A variable that is unset or blank is not a root, and an
// empty Clean would turn "" into "." — a real, walkable, wrong path.
func RootEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return ""
	}

	return Root(value)
}

// UserHome is os.UserHomeDir cleaned, for the same reason.
func UserHome() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return Root(dir), nil
}

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
