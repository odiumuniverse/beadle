package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

const (
	registryRewritePause = 2 * time.Millisecond
	pivotReadAttempts    = 5
)

type ledgerTestRecord struct {
	Version string `json:"version"`
	Target  string `json:"target"`
}

func claudePluginsDir(home string) string {
	return filepath.Join(home, ".claude", "plugins")
}

func registryPath(home string) string {
	return filepath.Join(claudePluginsDir(home), "installed_plugins.json")
}

func pluginTree(t *testing.T, home, marketplace, name, version string) string {
	t.Helper()

	dir := filepath.Join(claudePluginsDir(home), "cache", marketplace, name, version)
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), fmt.Sprintf(`{"name": %q, "version": %q}`, name, version))

	plugins := readRegistry(t, home)
	plugins[name+"@"+marketplace] = []map[string]any{{
		"scope":       "user",
		"installPath": dir,
		"version":     version,
	}}

	writeRegistry(t, home, plugins)

	return dir
}

func readRegistry(t *testing.T, home string) map[string][]map[string]any {
	t.Helper()

	data, err := os.ReadFile(registryPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]map[string]any{}
	}

	require.NoError(t, err)

	var doc struct {
		Plugins map[string][]map[string]any `json:"plugins"`
	}

	require.NoError(t, json.Unmarshal(data, &doc))

	if doc.Plugins == nil {
		doc.Plugins = map[string][]map[string]any{}
	}

	return doc.Plugins
}

func registryBytes(t *testing.T, plugins map[string][]map[string]any) []byte {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{"plugins": plugins}, "", "  ")
	require.NoError(t, err)

	return append(data, '\n')
}

func writeRegistry(t *testing.T, home string, plugins map[string][]map[string]any) {
	t.Helper()

	require.NoError(t, os.MkdirAll(claudePluginsDir(home), 0o750))
	require.NoError(t, os.WriteFile(registryPath(home), registryBytes(t, plugins), 0o600))
}

func removeFromRegistry(t *testing.T, home, marketplace, name string) {
	t.Helper()

	plugins := readRegistry(t, home)
	delete(plugins, name+"@"+marketplace)

	writeRegistry(t, home, plugins)
}

func pluginResult(t *testing.T, report *engine.Report, key string) engine.PluginResult {
	t.Helper()

	for _, result := range report.Plugins {
		if result.Key == key {
			return result
		}
	}

	require.FailNow(t, "no plugin result for "+key)

	return engine.PluginResult{}
}

func pivotLink(t *testing.T, f *fixture, marketplace, name string) string {
	t.Helper()

	link, err := readPivotLink(filepath.Join(f.vault.PluginsDir(), marketplace, name, "current"), pivotReadAttempts)
	require.NoError(t, err)

	return link
}

func readPivotLink(path string, attempts int) (string, error) {
	var err error

	for range attempts {
		var link string

		link, err = os.Readlink(path)
		if err == nil {
			return link, nil
		}
	}

	return "", err
}

func ledgerRecord(t *testing.T, f *fixture, key string) ledgerTestRecord {
	t.Helper()

	var doc struct {
		Version int                         `json:"version"`
		Plugins map[string]ledgerTestRecord `json:"plugins"`
	}

	require.NoError(t, json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc))
	require.Equal(t, 1, doc.Version)

	rec, ok := doc.Plugins[key]
	require.True(t, ok, "ledger must contain %s: %v", key, doc.Plugins)

	return rec
}

func startWorker(t *testing.T, worker func(stop <-chan struct{})) func() {
	t.Helper()

	stop := make(chan struct{})

	var (
		once sync.Once
		wg   sync.WaitGroup
	)

	stopWorker := func() {
		once.Do(func() { close(stop) })

		wg.Wait()
	}

	t.Cleanup(stopWorker)

	wg.Go(func() {
		worker(stop)
	})

	return stopWorker
}

func TestPluginReconcileCreatesAndRepointsPivot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	report := f.sync(t)
	result := pluginResult(t, report, "acme/tool")
	require.Equal(t, engine.PluginCreated, result.Action)
	require.Equal(t, "1.0.0", result.Version)
	require.Equal(t, target, result.Target)
	require.Equal(t, target, pivotLink(t, f, "acme", "tool"))

	info, err := os.Stat(f.vault.PluginsLedgerPath())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Equal(t, "1.0.0", ledgerRecord(t, f, "acme/tool").Version)

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")

	report = f.sync(t)
	result = pluginResult(t, report, "acme/tool")
	require.Equal(t, engine.PluginRepointed, result.Action)
	require.Equal(t, "2.0.0", result.Version)
	require.Equal(t, upgraded, pivotLink(t, f, "acme", "tool"))
	require.DirExists(t, target, "the old target is left in place")
	require.Equal(t, "2.0.0", ledgerRecord(t, f, "acme/tool").Version)

	before := read(t, f.vault.PluginsLedgerPath())

	report = f.sync(t)
	result = pluginResult(t, report, "acme/tool")
	require.Equal(t, engine.PluginNoop, result.Action)
	require.Equal(t, before, read(t, f.vault.PluginsLedgerPath()), "a noop sync must not touch the ledger")
	require.Equal(t, upgraded, pivotLink(t, f, "acme", "tool"))
}

func TestPluginReconcileReadersSeeOldOrNew(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	f.sync(t)

	pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

	var (
		mu       sync.Mutex
		seen     []string
		failures []string
	)

	fail := func(message string) {
		mu.Lock()

		failures = append(failures, message)

		mu.Unlock()
	}

	record := func(link string) {
		mu.Lock()

		seen = append(seen, link)

		mu.Unlock()
	}

	stopReader := startWorker(t, func(stop <-chan struct{}) {
		for {
			select {
			case <-stop:
				return
			default:
			}

			link, err := readPivotLink(pivot, pivotReadAttempts)
			if err != nil {
				fail("readlink: " + err.Error())

				return
			}

			if !fsutil.Exists(link) {
				fail("target does not exist: " + link)

				return
			}

			record(link)
		}
	})

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")

	f.sync(t)
	stopReader()

	require.Empty(t, failures)
	require.NotEmpty(t, seen)

	for _, link := range seen {
		require.Contains(t, []string{v1, v2}, link, "readers must only see the old or the new target")
	}

	require.Equal(t, v2, pivotLink(t, f, "acme", "tool"))
	require.DirExists(t, v1)

	report := f.sync(t)
	require.Equal(t, engine.PluginNoop, pluginResult(t, report, "acme/tool").Action)
}

func TestPluginReconcileRegistryRace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	v2 := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0")
	write(t, filepath.Join(v2, ".claude-plugin", "plugin.json"), `{"name": "tool", "version": "2.0.0"}`)

	regV1 := registryBytes(t, map[string][]map[string]any{"tool@acme": {{
		"scope": "user", "installPath": v1, "version": "1.0.0",
	}}})

	regV2 := registryBytes(t, map[string][]map[string]any{"tool@acme": {{
		"scope": "user", "installPath": v2, "version": "2.0.0",
	}}})

	var (
		mu       sync.Mutex
		failures []string
	)

	fail := func(message string) {
		mu.Lock()

		failures = append(failures, message)

		mu.Unlock()
	}

	stopWriter := startWorker(t, func(stop <-chan struct{}) {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}

			blob := regV1
			if i%2 == 1 {
				blob = regV2
			}

			if err := fsutil.WriteFileAtomic(registryPath(f.home), blob, 0o600); err != nil {
				fail("rewrite registry: " + err.Error())

				return
			}

			time.Sleep(registryRewritePause)
		}
	})

	for range 5 {
		f.sync(t)
	}

	stopWriter()

	f.sync(t)

	require.Empty(t, failures)

	link := pivotLink(t, f, "acme", "tool")
	require.True(t, fsutil.Exists(link), "pivot target must exist: %s", link)

	rec := ledgerRecord(t, f, "acme/tool")
	require.Contains(t, []string{"1.0.0", "2.0.0"}, rec.Version)
	require.Equal(t, rec.Target, link)
}

func TestPluginReconcileSkipsMissingInstallPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	missing := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "ghost", "1.0.0")
	writeRegistry(t, f.home, map[string][]map[string]any{"ghost@acme": {{
		"scope": "user", "installPath": missing, "version": "1.0.0",
	}}})

	report := f.sync(t)
	result := pluginResult(t, report, "acme/ghost")
	require.Equal(t, engine.PluginSkipped, result.Action)
	require.Equal(t, "install path is missing", result.Note)
	require.NoFileExists(t, f.vault.PluginsLedgerPath())
}

func TestPluginReconcileDryRunMakesNoChanges(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	pluginTree(t, f.home, "acme", "tool", "1.0.0")

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.Empty(t, report.Plugins)
	require.DirExists(t, f.vault.PluginsDir())
	require.NoFileExists(t, f.vault.PluginsLedgerPath())
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "acme"))
}

func TestDoctorPluginPivotIssues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked yet"), "issues: %v", issues)

	f.sync(t)

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool changed 1.0.0 → 2.0.0"), "issues: %v", issues)

	f.sync(t)

	require.NoError(t, os.RemoveAll(v2))

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "plugin acme/tool pivot target is missing"), "issues: %v", issues)

	require.NoError(t, os.MkdirAll(v2, 0o700))
	f.sync(t)

	require.NoError(t, fsutil.ReplaceSymlink(filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current"), v1))

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool pivot is stale: "+v1), "issues: %v", issues)

	pivotPath := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
	require.NoError(t, os.Remove(pivotPath))
	write(t, pivotPath, "not a symlink")

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool pivot cannot be read"), "issues: %v", issues)

	removeFromRegistry(t, f.home, "acme", "tool")
	require.NoError(t, os.RemoveAll(v1))
	require.NoError(t, os.RemoveAll(v2))

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is no longer installed"), "issues: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityError, "acme/tool"), "a removed plugin must not produce a pivot error: %v", issues)
}

func TestWatchPathsIncludePluginRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	head := filepath.Join(claudePluginsDir(f.home), "marketplaces", "acme", ".git", "HEAD")
	write(t, head, "ref: refs/heads/main\n")

	paths, err := f.engine.WatchPaths(t.Context())
	require.NoError(t, err)

	registry := registryPath(f.home)
	require.Equal(t, 1, countPath(paths, registry), "the registry must be watched exactly once")
	require.Equal(t, 1, countPath(paths, head), "the marketplace HEAD must be watched exactly once")
}

func countPath(paths []string, path string) int {
	count := 0

	for _, candidate := range paths {
		if candidate == path {
			count++
		}
	}

	return count
}

func TestPluginReconcileMalformedLedger(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	require.NoError(t, os.MkdirAll(f.vault.PluginsDir(), 0o700))
	require.NoError(t, os.WriteFile(f.vault.PluginsLedgerPath(), []byte("{ not json"), 0o600))

	report := f.sync(t)
	require.Equal(t, engine.PluginCreated, pluginResult(t, report, "acme/tool").Action)
	require.Equal(t, target, pivotLink(t, f, "acme", "tool"))

	warned := slices.ContainsFunc(report.Warnings, func(warning string) bool {
		return strings.Contains(warning, "ledger")
	})
	require.True(t, warned, "warnings: %v", report.Warnings)

	var doc struct {
		Version int                         `json:"version"`
		Plugins map[string]ledgerTestRecord `json:"plugins"`
	}

	require.NoError(t, json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc))
	require.Equal(t, 1, doc.Version)
	require.Contains(t, doc.Plugins, "acme/tool")
}

func TestPluginReconcileKeepsLedgerForMissingPlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

	f.sync(t)
	removeFromRegistry(t, f.home, "acme", "tool")

	report := f.sync(t)
	result := pluginResult(t, report, "acme/tool")
	require.Equal(t, engine.PluginSkipped, result.Action)
	require.Equal(t, "plugin is no longer installed", result.Note)
	require.Equal(t, "1.0.0", ledgerRecord(t, f, "acme/tool").Version)
	require.Equal(t, target, pivotLink(t, f, "acme", "tool"))
}

func TestPluginReconcileRejectsInvalidKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	pluginTree(t, f.home, "acme", "tool", "1.0.0")

	evil := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "evil", "0.0.1")
	write(t, filepath.Join(evil, ".claude-plugin", "plugin.json"), `{"name": "evil", "version": "0.0.1"}`)

	plugins := readRegistry(t, f.home)
	plugins["../evil@acme"] = []map[string]any{{
		"scope": "user", "installPath": evil, "version": "0.0.1",
	}}

	writeRegistry(t, f.home, plugins)

	report := f.sync(t)

	bad := pluginResult(t, report, "acme/../evil")
	require.Equal(t, engine.PluginSkipped, bad.Action)
	require.Equal(t, "invalid plugin key", bad.Note)

	warned := slices.ContainsFunc(report.Warnings, func(warning string) bool {
		return strings.Contains(warning, "invalid plugin key")
	})
	require.True(t, warned, "warnings: %v", report.Warnings)

	require.Equal(t, engine.PluginCreated, pluginResult(t, report, "acme/tool").Action)
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "evil"))
	require.NotContains(t, read(t, f.vault.PluginsLedgerPath()), "evil")
	require.Equal(t, "acme/../evil", report.Plugins[0].Key, "the invalid key is reported first, sorted by key")
}

func TestPluginReconcileStopsWhenGitIgnoreFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	pluginTree(t, f.home, "acme", "tool", "1.0.0")

	ignorePath := filepath.Join(f.vault.Root(), ".gitignore")
	require.NoError(t, os.Chmod(ignorePath, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(ignorePath, 0o600)
	})

	report := f.sync(t)
	require.Empty(t, report.Plugins)

	warned := slices.ContainsFunc(report.Warnings, func(warning string) bool {
		return strings.Contains(warning, ".gitignore")
	})
	require.True(t, warned, "warnings: %v", report.Warnings)

	require.NoFileExists(t, f.vault.PluginsLedgerPath())
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "acme"))
}

func TestEnsureGitIgnoreMerges(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	t.Setenv("GIT_AUTHOR_NAME", "agent-sync test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "agent-sync test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	f := newFixture(t)
	f.config.History = config.HistoryGit
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.claudeRules(), "# r\n")
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.openCodeRules(), "# r\n")

	old := "# AgentSync: credentials and machine-local state stay on this machine.\nmcp/secrets.json\nstate/\nstate.json\nobjects/\nconflicts/"
	ignorePath := filepath.Join(f.vault.Root(), ".gitignore")
	write(t, ignorePath, old)

	pluginTree(t, f.home, "acme", "tool", "1.0.0")

	f.sync(t)

	merged := read(t, ignorePath)
	require.True(t, strings.HasPrefix(merged, old), "existing lines must be kept: %q", merged)
	require.Contains(t, merged, "plugins/\n")
	require.True(t, strings.HasSuffix(merged, "\n"), "the file must end with a newline")

	info, err := os.Stat(ignorePath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	status, err := exec.CommandContext(t.Context(), "git", "-C", f.vault.Root(), "status", "--porcelain").Output() //nolint:gosec // G204: fixed git subcommand in a test
	require.NoError(t, err)
	require.NotContains(t, string(status), "plugins")

	tracked, err := exec.CommandContext(t.Context(), "git", "-C", f.vault.Root(), "ls-files").Output() //nolint:gosec // G204: fixed git subcommand in a test
	require.NoError(t, err)
	require.NotContains(t, string(tracked), "plugins", "the machine-local ledger must never be committed")

	f.sync(t)
	require.Equal(t, merged, read(t, ignorePath), "a second sync must not duplicate lines")

	require.NoError(t, f.vault.EnsureGitIgnore())
	require.Equal(t, merged, read(t, ignorePath))
}

func TestPluginReconcileEmptyHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	pluginTree(t, f.home, "acme", "tool", "1.0.0")

	e, err := engine.New(f.vault, f.config, agent.All(f.home))
	require.NoError(t, err)

	report, err := e.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Empty(t, report.Errors())
	require.Empty(t, report.Plugins)
	require.NoFileExists(t, f.vault.PluginsLedgerPath())
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "acme"))

	issues, err := e.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "acme/tool")
	}
}

func TestDoctorPluginPivotIssuesNoRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "pivot")
		require.NotContains(t, issue.Message, "plugin")
	}
}
