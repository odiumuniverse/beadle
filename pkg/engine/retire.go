package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// retireInvalidSkills removes canon skill directories that have no root
// SKILL.md and that beadle owns on some surface. The ordinary deletion path
// cannot reach them: once the surfaces stop reading the directory, the pull
// sees nothing to delete and the write paths never enumerate it. The open
// conflicts about a retired name are dropped with it.
func (e *Engine) retireInvalidSkills(st *state.State, report *KindReport) bool {
	dir := e.vault.SkillsDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			report.Warnings = append(report.Warnings, "skills: cannot scan the canon: "+err.Error())
		}

		return false
	}

	retired := false

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		if !skill.ValidName(name) || entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() || skill.HasRoot(path) {
			continue
		}

		if !skillNameOwned(st, name) {
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("skills: cannot retire the invalid canon skill %s: %v", name, err))

			continue
		}

		dropSkillConflicts(st, name)

		report.Warnings = append(report.Warnings, fmt.Sprintf("skills: retired the invalid canon skill %s (no root SKILL.md)", name))

		retired = true
	}

	return retired
}

// skillNameOwned reports whether any agent's skills base records the name:
// beadle adopted or delivered the element, so its canon copy is beadle's to
// retire.
func skillNameOwned(st *state.State, name string) bool {
	for _, base := range st.Bases[kind.Skills] {
		if baseOwns(base, kind.Skills, name) {
			return true
		}
	}

	return false
}

// dropSkillConflicts removes the open conflicts about one skill name: the
// canon element is gone, so the divergence no longer has a subject.
func dropSkillConflicts(st *state.State, name string) {
	st.Conflicts = slices.DeleteFunc(st.Conflicts, func(c state.Conflict) bool {
		return c.Kind == kind.Skills && groupName(kind.Skills, c.Key) == name
	})
}

// pruneInvalidSkills removes beadle-owned skill directories without a root
// SKILL.md from a writable file surface: the surface no longer reads them,
// so its write path cannot clean them up.
func pruneInvalidSkills(dir string, base kind.Items, report *KindReport) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			report.Warnings = append(report.Warnings, "skills: cannot scan "+dir+": "+err.Error())
		}

		return
	}

	owned := itemGroups(base, kind.Skills)

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		if !skill.ValidName(name) || entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() || skill.HasRoot(path) {
			continue
		}

		if _, ours := owned[name]; !ours {
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("skills: cannot remove the invalid skill %s from %s: %v", name, dir, err))

			continue
		}

		report.Warnings = append(report.Warnings, fmt.Sprintf("skills: removed the invalid skill %s from %s (no root SKILL.md)", name, dir))
	}
}
