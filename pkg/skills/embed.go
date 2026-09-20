package skills

import (
	_ "embed"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// ConflictsName is the canonical skill name seeded into the vault.
const ConflictsName = "beadle-conflicts"

//go:embed assets/beadle-conflicts/SKILL.md
var conflictsSkill []byte

// Conflicts returns a copy of the embedded beadle-conflicts skill.
func Conflicts() []byte {
	return slices.Clone(conflictsSkill)
}

// Seed writes the beadle-conflicts skill into skillsDir. It never clobbers an
// existing skill unless force is set, and reports whether it wrote the file.
func Seed(skillsDir string, force bool) (bool, error) {
	file := filepath.Join(skillsDir, ConflictsName, "SKILL.md")
	if fsutil.Exists(file) && !force {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return false, err
	}

	if err := fsutil.WriteFileAtomic(file, conflictsSkill, 0o644); err != nil {
		return false, err
	}

	return true, nil
}
