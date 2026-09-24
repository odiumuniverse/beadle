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

	dshHomeEnv       = "DSH_HOME"
	dshAgentsHomeEnv = "DSH_AGENTS_HOME"
	dshDirName       = ".dsh"
	dshAgentsDirName = ".agents"
	dshBinary        = "dsh"
	dshSkillsDir     = "skills"
	dshProfilesDir   = "profiles"
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

// DSHSurfacePaths returns the adapter's two user-level surface paths: the
// user-global instructions file and the skills directory. Both are write
// surfaces (sync + creatable); the project chain adds per-file surfaces.
func DSHSurfacePaths(home string) (rules, skills string) {
	dir, _ := DSHHome(home)

	return filepath.Join(dir, agentsMarkdown), filepath.Join(dir, dshSkillsDir)
}

// DSHAgentsHome resolves the shared agents root the way DSH does: a non-empty
// DSH_AGENTS_HOME wins, otherwise ~/.agents. An empty value falls back to
// ~/.agents, following the DSH_HOME blank rule (DSH's own nullish coalescing
// would resolve an empty value against cwd — a live probe can pin that
// divergence). Like DSH_HOME, the non-empty value is taken literally: no
// trim, no ~ expansion, and a relative path resolves from cwd.
func DSHAgentsHome(home string) string {
	if value := os.Getenv(dshAgentsHomeEnv); value != "" {
		return value
	}

	return filepath.Join(home, dshAgentsDirName)
}

// DSHSharedSkillsDir returns the shared skill root DSH reads at rank 500:
// <agentsHome>/skills, where agentsHome is $DSH_AGENTS_HOME or ~/.agents.
// With the default agents home it is the ~/.agents/skills hub beadle's
// shared surface owns.
func DSHSharedSkillsDir(home string) string {
	return filepath.Join(DSHAgentsHome(home), dshSkillsDir)
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
// is a write surface (kind rules, like GEMINI.md for Gemini), the project
// instruction chain (AGENTS.md/CLAUDE.md from the nearest .git root down to
// cwd) is pulled per file with root-relative rels, the skills surface writes
// `$DSH_HOME/skills` (kind skills) while reading the shared agents-home skills
// root (`$DSH_AGENTS_HOME` or `~/.agents`) read-only, and the MCP surface
// writes beadle's servers into the home patch layer `$DSH_HOME/cordis.patch.yml`
// (kind mcp). DSH resolves same-name skills by root rank — `$DSH_HOME/skills`
// (400) wins over the shared agents-home (500) — so the shared copy is
// shadowed by design and never removed.
func DSH(home, cwd string) *Agent {
	rules, skills := DSHSurfacePaths(home)
	shared := DSHSharedSkillsDir(home)

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
			dir:         skills,
			ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
			alsoReads:   []string{shared},
			// DSH merges same-name skills by root rank: the user-dsh root
			// (400) wins over the shared user-agents root (500).
			shadowing:  true,
			readOrder:  []string{skills, shared},
			flatSkills: true,
			codec:      dshSkillCodec{},
			traits: Traits{
				DefaultMode: config.ModeSync,
				Creatable:   true,
				Note:        "DSH reads $DSH_HOME/skills (rank 400) before the shared agents-home ($DSH_AGENTS_HOME or ~/.agents, rank 500); the shared copy is read-only and shadowed by design",
			},
		},
		&dshMCPSurface{file: DSHPatchPath(home)},
	}

	return &Agent{
		ID:       DSHID,
		Name:     "DeepSeek Harness",
		Detect:   func() (bool, error) { return DSHDetected(home) },
		Surfaces: append(surfaces, DSHChain(cwd, id)...),
	}
}
