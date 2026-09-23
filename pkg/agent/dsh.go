package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/project"
)

const (
	// DSHID identifies the DeepSeek Harness adapter.
	DSHID = "deepseek-harness"

	dshHomeEnv     = "DSH_HOME"
	dshDirName     = ".dsh"
	dshBinary      = "dsh"
	dshSkillsDir   = "skills"
	dshProfilesDir = "profiles"
)

// DSHHomeNote explains the empty DSH_HOME case: DSH itself ignores the empty
// value instead of resolving it against the working directory.
const DSHHomeNote = "DSH_HOME is empty; DSH ignores it and falls back to ~/.dsh (it is not resolved to cwd)"

// DSHHome resolves the DeepSeek Harness home the way DSH does: a non-empty
// DSH_HOME wins, an empty value is ignored and falls back to ~/.dsh, and
// ~/.dsh is the default. The second result reports whether DSH_HOME was set to
// an empty value.
//
// The non-empty value is taken literally: it is not trimmed, a leading ~ is
// not expanded, and a relative path resolves from the process working
// directory. Detection treats any existing path — a file included — as
// present. Q-15 verifies these semantics against upstream DSH.
func DSHHome(home string) (string, bool) {
	value, ok := os.LookupEnv(dshHomeEnv)
	if ok && value != "" {
		return value, false
	}

	return filepath.Join(home, dshDirName), ok
}

// DSHDetected reports whether DSH is present: its home directory exists or the
// dsh binary is on PATH.
func DSHDetected(home string) (bool, error) {
	dir, _ := DSHHome(home)

	found, err := anyExists(dir)
	if err != nil || found {
		return found, err
	}

	if _, err := exec.LookPath(dshBinary); err == nil {
		return true, nil
	}

	return false, nil
}

// DSHSurfacePaths returns the read-only paths the adapter reports: the
// user-level instructions file and the skills directory.
func DSHSurfacePaths(home string) (rules, skills string) {
	dir, _ := DSHHome(home)

	return filepath.Join(dir, agentsMarkdown), filepath.Join(dir, dshSkillsDir)
}

// DSHProfiles lists the profile directories under <home>/profiles.
func DSHProfiles(home string) []string {
	dir, _ := DSHHome(home)

	entries, err := os.ReadDir(filepath.Join(dir, dshProfilesDir))
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)

	return names
}

// DSH builds the DeepSeek Harness adapter: the user-global instructions file
// is a write surface (kind rules, like GEMINI.md for Gemini), and the project
// instruction chain (AGENTS.md/CLAUDE.md from the nearest .git root down to
// cwd) is pulled per file with root-relative rels. The skills surface stays
// read-only by default until A-39 flips it; an explicit mode flip
// (beadle agents mode ... sync) writes every surface, like every other
// pull-default surface.
func DSH(home, cwd string) *Agent {
	rules, skills := DSHSurfacePaths(home)

	id := project.Resolve(cwd).ID

	surfaces := []Surface{
		&rulesSurface{
			path: rules,
			traits: Traits{
				DefaultMode: config.ModeSync,
				Creatable:   true,
				Note:        "user-global DSH instructions; the project chain (AGENTS.md/CLAUDE.md) is pulled per file",
			},
		},
		&skillsSurface{
			dir: skills,
			traits: Traits{
				DefaultMode: config.ModePull,
				Note:        "read-only by default: beadle reads $DSH_HOME/skills and writes only after an explicit mode flip (A-39 flips the default)",
			},
		},
	}

	return &Agent{
		ID:       DSHID,
		Name:     "DeepSeek Harness",
		Detect:   func() (bool, error) { return DSHDetected(home) },
		Surfaces: append(surfaces, DSHChain(cwd, id)...),
	}
}
