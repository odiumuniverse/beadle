package skills

import (
	_ "embed"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// The built-in skills seeded into the vault.
const (
	ConflictsName = "beadle-conflicts"
	BeadleName    = "beadle"
)

//go:embed assets/beadle-conflicts/SKILL.md
var conflictsSkill []byte

//go:embed assets/beadle/SKILL.md
var beadleSkill []byte

// Conflicts returns a copy of the embedded beadle-conflicts skill.
func Conflicts() []byte {
	return slices.Clone(conflictsSkill)
}

// Beadle returns a copy of the embedded beadle skill.
func Beadle() []byte {
	return slices.Clone(beadleSkill)
}

// builtins lists the embedded skills in seed order.
var builtins = []struct {
	name string
	body []byte
}{
	{ConflictsName, conflictsSkill},
	{BeadleName, beadleSkill},
}

// Seed writes the built-in skills into skillsDir and returns the names it
// wrote. It never clobbers an existing skill unless force is set.
func Seed(skillsDir string, force bool) ([]string, error) {
	var written []string

	for _, skill := range builtins {
		ok, err := seedOne(skillsDir, skill.name, skill.body, force)
		if err != nil {
			return written, err
		}

		if ok {
			written = append(written, skill.name)
		}
	}

	return written, nil
}

// seedOne writes one skill and reports whether it wrote the file.
func seedOne(skillsDir, name string, body []byte, force bool) (bool, error) {
	file := filepath.Join(skillsDir, name, "SKILL.md")
	if fsutil.Exists(file) && !force {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return false, err
	}

	if err := os.Chmod(filepath.Dir(file), 0o700); err != nil { //nolint:gosec // G302: a directory needs the execute bit; 0700 is owner-only
		return false, err
	}

	if err := fsutil.WriteFileAtomic(file, body, 0o600); err != nil {
		return false, err
	}

	return true, nil
}
