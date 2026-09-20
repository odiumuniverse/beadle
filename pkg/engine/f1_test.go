package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func stubReplaceSymlink(t *testing.T, link func(path, target string) error) {
	t.Helper()

	previous := replaceSymlink
	replaceSymlink = link

	t.Cleanup(func() { replaceSymlink = previous })
}

func stubHostFSType(t *testing.T, fsType func(dir string) (string, error)) {
	t.Helper()

	previous := hostFSType
	hostFSType = fsType

	t.Cleanup(func() { hostFSType = previous })
}

func bareFarmEngine(t *testing.T) *Engine {
	t.Helper()

	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	require.NoError(t, os.MkdirAll(v.SkillsDir(), 0o700))
	require.NoError(t, os.MkdirAll(v.PluginsDir(), 0o700))

	return &Engine{vault: v, config: config.Default()}
}

func farmPlanFor(skills ...string) farmPlan {
	desired := map[string]struct{}{}
	owner := map[string]string{}

	for _, name := range skills {
		desired[name] = struct{}{}
		owner[name] = "acme/tool"
	}

	return farmPlan{
		Parked:      map[string][]string{"acme/tool": skills},
		Quarantined: map[string]pluginLedgerRec{},
		Owner:       owner,
		Desired:     desired,
		Canon:       map[string]struct{}{},
	}
}

//nolint:paralleltest // the test swaps a package-level seam
func TestFarmReportsUnsupportedSymlinks(t *testing.T) {
	e := bareFarmEngine(t)

	failingDir := filepath.Join(t.TempDir(), "claude", "skills")
	otherDir := filepath.Join(t.TempDir(), "opencode", "skills")

	require.NoError(t, os.MkdirAll(failingDir, 0o700))
	require.NoError(t, os.MkdirAll(otherDir, 0o700))

	prunable := filepath.Join(e.vault.PluginsDir(), "acme", "tool", "current", "skills", "gone")
	require.NoError(t, os.Symlink(prunable, filepath.Join(failingDir, "gone")))

	calls := map[string]int{}

	stubReplaceSymlink(t, func(path, target string) error {
		dir := filepath.Dir(path)
		calls[dir]++

		if dir == failingDir {
			return fmt.Errorf("create temp symlink: %w", fsutil.ErrSymlinksUnsupported)
		}

		return os.Symlink(target, path)
	})

	plan := farmPlanFor("alpha", "beta")

	results, warns := e.farmAgentSkills(agent.ClaudeCodeID, failingDir, plan)
	require.Empty(t, warns)
	require.Equal(t, []FarmResult{{
		Agent:  agent.ClaudeCodeID,
		Action: FarmSkipped,
		Note:   "symlinks are not supported in " + failingDir + "; a copy fallback is intentionally not performed",
	}}, results)
	require.Equal(t, 1, calls[failingDir], "the farm must stop after the first unsupported link")

	entries, err := os.ReadDir(failingDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no farm entries may be created in an unsupported directory")
	require.Equal(t, "gone", entries[0].Name())

	link, err := os.Readlink(filepath.Join(failingDir, "gone"))
	require.NoError(t, err)
	require.Equal(t, prunable, link, "prune must not run in an unsupported directory")

	probes, err := filepath.Glob(filepath.Join(failingDir, ".beadle-probe*"))
	require.NoError(t, err)
	require.Empty(t, probes, "the farm must leave no temp artifacts")

	otherResults, otherWarns := e.farmAgentSkills(agent.OpenCodeID, otherDir, plan)
	require.Empty(t, otherWarns)
	require.Equal(t, []FarmResult{{
		Agent:  agent.OpenCodeID,
		Plugin: "acme/tool",
		Action: FarmLinked,
		Count:  2,
	}}, otherResults)

	canon, err := os.ReadDir(e.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, canon, "the vault canon must not be touched")
}

//nolint:paralleltest // the test swaps a package-level seam
func TestFarmOrdinaryErrorsStayWarnings(t *testing.T) {
	e := bareFarmEngine(t)

	dir := filepath.Join(t.TempDir(), "claude", "skills")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	calls := 0

	stubReplaceSymlink(t, func(_, _ string) error {
		calls++

		return fmt.Errorf("create temp symlink: %w", fs.ErrPermission)
	})

	results, warns := e.farmAgentSkills(agent.ClaudeCodeID, dir, farmPlanFor("alpha", "beta"))

	require.Empty(t, results)
	require.Len(t, warns, 2)
	require.Equal(t, 2, calls)
	require.NotContains(t, strings.Join(warns, " "), "symlinks are not supported")
	require.Contains(t, warns[0], "plugin farm: ")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestFarmQuietWithoutWork(t *testing.T) {
	t.Parallel()

	e := bareFarmEngine(t)

	results, warns := e.farmAgentSkills(agent.ClaudeCodeID, filepath.Join(t.TempDir(), "missing", "skills"), farmPlanFor())
	require.Empty(t, results)
	require.Empty(t, warns)
}

//nolint:paralleltest // the test swaps a package-level seam
func TestDoctorReportsUnsupportedSymlinks(t *testing.T) {
	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))
	require.NoError(t, v.Init())

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)
	cfg.Enable(agent.ClaudeCodeID)
	require.NoError(t, cfg.Save(v.ConfigPath()))

	e, err := New(v, cfg, agent.All(home, t.TempDir()), WithHome(home))
	require.NoError(t, err)

	cache := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
	skillDir := filepath.Join(cache, farmSkillsDir, "alpha")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, fsutil.WriteFileAtomic(filepath.Join(skillDir, farmSkillFile), []byte("# alpha\n"), 0o600))

	claude := agent.ByID(e.Agents(), agent.ClaudeCodeID)
	require.NotNil(t, claude)

	skillsDir := claude.Surface(kind.Skills).Path()
	require.NoError(t, os.MkdirAll(skillsDir, 0o700))

	require.NoError(t, os.MkdirAll(v.PluginsDir(), 0o700))

	ledger := pluginLedger{Version: pluginLedgerVersion, Plugins: map[string]pluginLedgerRec{
		"acme/tool": {Version: "1.0.0", Target: cache},
	}}
	require.NoError(t, ledger.save(v.PluginsLedgerPath()))

	stubHostFSType(t, func(string) (string, error) { return "exfat", nil })

	issues, err := e.Doctor(t.Context())
	require.NoError(t, err)

	var warned []Issue

	for _, issue := range issues {
		if strings.Contains(issue.Message, "symlinks are not supported") {
			warned = append(warned, issue)
		}
	}

	require.Len(t, warned, 1)
	require.Equal(t, SeverityWarn, warned[0].Severity)
	require.Equal(t, agent.ClaudeCodeID, warned[0].Agent)
	require.Equal(t, kind.Skills, warned[0].Kind)
	require.Contains(t, warned[0].Message, "symlinks are not supported in ~/.claude/skills; 1 plugin skill(s) are not presented")

	stubHostFSType(t, func(string) (string, error) { return "smb", nil })
	require.NotContains(t, issueMessages(t, e), "symlinks are not supported")

	stubHostFSType(t, func(string) (string, error) { return "", fs.ErrPermission })
	require.NotContains(t, issueMessages(t, e), "symlinks are not supported")

	require.NoError(t, os.RemoveAll(skillsDir))

	stubHostFSType(t, func(string) (string, error) { return "exfat", nil })
	require.NotContains(t, issueMessages(t, e), "symlinks are not supported")
}

func issueMessages(t *testing.T, e *Engine) string {
	t.Helper()

	issues, err := e.Doctor(t.Context())
	require.NoError(t, err)

	messages := make([]string, 0, len(issues))

	for _, issue := range issues {
		messages = append(messages, issue.Message)
	}

	return strings.Join(messages, "\n")
}
