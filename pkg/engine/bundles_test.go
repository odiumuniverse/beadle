package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type fakeCLI struct {
	calls  [][]string
	failOn map[string]error
	output string
}

func (f *fakeCLI) Run(name string, args []string, _ []byte) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))

	if err := f.failOn[strings.Join(args, " ")]; err != nil {
		return []byte(f.output), 1, err
	}

	return []byte(f.output), 0, nil
}

func foundCLI(t *testing.T) {
	t.Helper()

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) { return "/usr/bin/" + name, nil })
	t.Cleanup(restore)
}

func missingCLI(t *testing.T) {
	t.Helper()

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) { return "", fmt.Errorf("%s: not found", name) })
	t.Cleanup(restore)
}

func fakeRunner(t *testing.T, cli *fakeCLI) {
	t.Helper()

	restore := engine.SetBundlesRunnerForTest(cli)
	t.Cleanup(restore)
}

func bundleFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")
	write(t, f.vault.ServersPath(), `{"plug": {"transport": "stdio", "command": ["node", "srv.js"]}}`)
	write(t, f.vault.HooksPath(), `{"notify": {"event": "session-start", "command": "echo hi", "timeout": 5}}`)

	f.config.ApproveHook("notify")
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	return f
}

func TestBundlesEnableRegistersClaude(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Len(t, report.Bundles, 1)
	require.Equal(t, "enabled", report.Bundles[0].Action)
	require.True(t, report.Bundles[0].Registered)
	require.Regexp(t, `^0\.0\.0-[0-9a-f]{12}$`, report.Bundles[0].Version)

	dir := filepath.Join(f.vault.BundlesDir(), "claude")
	require.Equal(t, [][]string{
		{"claude", "plugin", "marketplace", "add", dir},
		{"claude", "plugin", "install", "beadle-canon@beadle"},
	}, cli.calls)

	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync))
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync))

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	entry := st.Bundles["claude"]
	require.True(t, entry.Enabled)
	require.True(t, entry.Registered)
	require.Equal(t, map[kind.ID]config.Mode{kind.Skills: config.ModeSync, kind.MCP: config.ModeSync}, entry.SavedModes)

	marketplace := read(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	require.Contains(t, marketplace, `"name": "beadle"`)
	require.Contains(t, marketplace, entry.Version)
	require.Contains(t, read(t, filepath.Join(dir, "plugins", "beadle-canon", "skills", "alpha", "SKILL.md")), "# alpha")
	require.Contains(t, read(t, filepath.Join(dir, "plugins", "beadle-canon", "hooks", "hooks.json")), "echo hi")
}

func TestBundlesEnableWithoutCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	missingCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Len(t, report.Bundles, 1)
	require.Equal(t, "generated", report.Bundles[0].Action)
	require.False(t, report.Bundles[0].Registered)
	require.Contains(t, report.Bundles[0].Note, "claude plugin marketplace add")
	require.Contains(t, strings.Join(report.Warnings, " "), "claude CLI not found")
	require.Empty(t, cli.calls)

	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), "file sync must stay on while the bundle is not registered")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.True(t, st.Bundles["claude"].Enabled)
	require.False(t, st.Bundles["claude"].Registered)
}

func TestBundlesEnableCLIFailureShowsOutput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{failOn: map[string]error{"plugin install beadle-canon@beadle": errors.New("boom")}, output: "raw install failure"}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "generated", report.Bundles[0].Action)
	require.False(t, report.Bundles[0].Registered)
	require.Contains(t, report.Bundles[0].Note, "raw install failure")
	require.Contains(t, strings.Join(report.Warnings, " "), "raw install failure")
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync))
}

func TestBundlesEnableIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Len(t, cli.calls, 2)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "noop", report.Bundles[0].Action)
	require.Len(t, cli.calls, 2, "a second enable with the same version must not run the CLI again")
}

func TestBundlesDisableRestoresModes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.calls = nil

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "disabled", report.Bundles[0].Action)

	require.Equal(t, [][]string{
		{"claude", "plugin", "uninstall", "beadle-canon@beadle"},
		{"claude", "plugin", "marketplace", "rm", "beadle"},
	}, cli.calls)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff))
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff))

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	_, ok := st.Bundles["claude"]
	require.False(t, ok)

	cli.calls = nil

	_, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.Empty(t, cli.calls, "a disabled bundle must not be validated")
}

func TestBundlesDisableRetriesAfterFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.failOn = map[string]error{"plugin uninstall beadle-canon@beadle": errors.New("boom")}

	_, err = f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)

	cli.failOn = nil
	cli.calls = nil

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "disabled", report.Bundles[0].Action)

	require.Equal(t, [][]string{
		{"claude", "plugin", "uninstall", "beadle-canon@beadle"},
		{"claude", "plugin", "marketplace", "rm", "beadle"},
	}, cli.calls)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff))

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	_, ok := st.Bundles["claude"]
	require.False(t, ok)
}

func TestBundlesEnableUpdateFailureKeepsRegisteredVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	first, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.failOn = map[string]error{"plugin update beadle-canon@beadle": errors.New("boom")}
	cli.output = "raw update failure"

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "enabled", report.Bundles[0].Action)
	require.Equal(t, first.Bundles[0].Version, report.Bundles[0].Version, "a failed update must report the registered version")
	require.Contains(t, report.Bundles[0].Note, "raw update failure")
	require.Contains(t, strings.Join(report.Warnings, " "), "raw update failure")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Equal(t, first.Bundles[0].Version, st.Bundles["claude"].Version)
}

func TestBundleIssuesAntigravityLinkStates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	missingCLI(t)
	fakeRunner(t, &fakeCLI{})

	_, err := f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "is not linked yet"), "issues: %v", issues)

	linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
	require.NoError(t, os.MkdirAll(filepath.Dir(linked), 0o750))
	require.NoError(t, os.MkdirAll(linked, 0o750))

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "is linked but not registered"), "issues: %v", issues)

	_, err = f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "is not linked yet")
		require.NotContains(t, issue.Message, "is linked but not registered")
	}
}

func TestBundlesDisableWithoutCLIKeepsRegistrationState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)
	fakeRunner(t, &fakeCLI{})

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	missingCLI(t)

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "failed", report.Bundles[0].Action)
	require.Contains(t, report.Bundles[0].Note, "claude plugin uninstall beadle-canon@beadle")
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync))

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.False(t, st.Bundles["claude"].Enabled)
	require.True(t, st.Bundles["claude"].Registered)
}

func TestBundlesDisableAntigravityAfterUnlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	missingCLI(t)
	fakeRunner(t, &fakeCLI{})

	linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
	require.NoError(t, os.MkdirAll(filepath.Dir(linked), 0o750))
	require.NoError(t, os.MkdirAll(linked, 0o750))

	_, err := f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync))

	report, err := f.engine.BundlesDisable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.Equal(t, "failed", report.Bundles[0].Action)
	require.Contains(t, report.Bundles[0].Note, "remove the plugin link first")
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync))

	require.NoError(t, os.RemoveAll(linked))

	report, err = f.engine.BundlesDisable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.Equal(t, "disabled", report.Bundles[0].Action)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeOff))

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	_, ok := st.Bundles["antigravity"]
	require.False(t, ok)
}

func TestBundlesDisableWithoutEnable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	missingCLI(t)
	fakeRunner(t, &fakeCLI{})

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "noop", report.Bundles[0].Action)
	require.Contains(t, report.Bundles[0].Note, "no enabled bundle")
}

func TestBundlesSyncRerendersAndDoctorWarnsStale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	registered := report.Bundles[0].Version
	cli.calls = nil

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

	f.sync(t)

	require.Empty(t, cli.calls, "sync must not register anything")
	manifest := read(t, filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json"))
	require.NotContains(t, manifest, registered)
	require.Contains(t, manifest, "0.0.0-")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "is stale"), "issues: %v", issues)
}

func TestBundleIssuesDedupConflict(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)
	fakeRunner(t, &fakeCLI{})

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeSync)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "still synced as files"), "issues: %v", issues)
}

func TestBundleIssuesApprovalInfo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	missingCLI(t)
	fakeRunner(t, &fakeCLI{})

	write(t, f.vault.HooksPath(), `{
		"notify": {"event": "session-start", "command": "echo hi"},
		"secret": {"event": "stop", "command": "rm -rf /"}
	}`)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "1 hook(s) await approval"), "issues: %v", issues)
}

func TestBundleIssuesValidateFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.failOn = map[string]error{"plugin validate " + filepath.Join(f.vault.BundlesDir(), "claude"): errors.New("bad manifest")}
	cli.output = "manifest error"

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "claude plugin validate failed"), "issues: %v", issues)
}

func TestBundlesEnableGeminiOnlyDedupsMCP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "gemini")
	require.NoError(t, err)
	require.True(t, report.Bundles[0].Registered)

	require.Equal(t, [][]string{{"gemini", "extensions", "link", filepath.Join(f.vault.BundlesDir(), "gemini")}}, cli.calls)
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeSync))
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.GeminiCLIID, kind.Skills, config.ModeSync), "gemini skills stay a file surface")

	require.FileExists(t, filepath.Join(f.vault.BundlesDir(), "gemini", "gemini-extension.json"))
	require.NoFileExists(t, filepath.Join(f.vault.BundlesDir(), "gemini", "skills", "alpha", "SKILL.md"))
}

func TestBundlesEnableAntigravityNeedsLink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.Equal(t, "generated", report.Bundles[0].Action)
	require.False(t, report.Bundles[0].Registered)
	require.Empty(t, cli.calls)
	require.Contains(t, report.Bundles[0].Note, f.home)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync))

	require.FileExists(t, filepath.Join(f.vault.BundlesDir(), "antigravity", "skills", "alpha", "SKILL.md"))

	linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
	require.NoError(t, os.MkdirAll(filepath.Dir(linked), 0o750))
	require.NoError(t, os.MkdirAll(linked, 0o750))

	report, err = f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.True(t, report.Bundles[0].Registered)
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync))

	report, err = f.engine.BundlesEnable(t.Context(), "antigravity")
	require.NoError(t, err)
	require.Equal(t, "noop", report.Bundles[0].Action)
}

func TestBundlesEnableUpdatesRegisteredBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	first, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.calls = nil

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

	second, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "enabled", second.Bundles[0].Action)
	require.NotEqual(t, first.Bundles[0].Version, second.Bundles[0].Version)

	require.Equal(t, [][]string{
		{"claude", "plugin", "marketplace", "update", "beadle"},
		{"claude", "plugin", "update", "beadle-canon@beadle"},
	}, cli.calls)

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Equal(t, second.Bundles[0].Version, st.Bundles["claude"].Version)
	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync))

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "disabled", report.Bundles[0].Action)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), "an update must keep the saved modes intact")
}

func TestBundlesEnableKeepsSavedModes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)
	fakeRunner(t, &fakeCLI{})

	f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModePull)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)
	_, err = f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Equal(t, map[kind.ID]config.Mode{kind.Skills: config.ModePull, kind.MCP: config.ModeSync}, st.Bundles["claude"].SavedModes)

	_, err = f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, config.ModePull, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync))
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff))
}

func TestBundlesDisableGeminiUnlinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "gemini")
	require.NoError(t, err)

	cli.calls = nil

	report, err := f.engine.BundlesDisable(t.Context(), "gemini")
	require.NoError(t, err)
	require.Equal(t, "disabled", report.Bundles[0].Action)

	require.Equal(t, [][]string{{"gemini", "extensions", "unlink", filepath.Join(f.vault.BundlesDir(), "gemini")}}, cli.calls)
	require.Equal(t, config.ModeSync, f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeOff))
}

func TestBundlesDisableFailureKeepsRegistrationState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	cli.failOn = map[string]error{"plugin uninstall beadle-canon@beadle": errors.New("boom")}
	cli.output = "raw uninstall failure"

	report, err := f.engine.BundlesDisable(t.Context(), "claude")
	require.NoError(t, err)
	require.Equal(t, "failed", report.Bundles[0].Action)
	require.True(t, report.Bundles[0].Registered)
	require.Contains(t, strings.Join(report.Warnings, " "), "raw uninstall failure")

	require.Equal(t, config.ModeOff, f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), "a failed unregister must not restore file sync")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.False(t, st.Bundles["claude"].Enabled)
	require.True(t, st.Bundles["claude"].Registered)

	cli.calls = nil

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "is still registered"), "issues: %v", issues)
	require.Equal(t, [][]string{{"claude", "plugin", "validate", filepath.Join(f.vault.BundlesDir(), "claude")}}, cli.calls, "a still-registered bundle must be validated")
}

func TestBundlesRefreshSkipsDryRunAndPull(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)
	fakeRunner(t, &fakeCLI{})

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	manifest := filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json")
	before := read(t, manifest)

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

	f.run(t, engine.SyncOptions{DryRun: true})
	require.Equal(t, before, read(t, manifest), "dry-run must not rewrite bundles")

	f.run(t, engine.SyncOptions{Direction: config.ModePull})
	require.Equal(t, before, read(t, manifest), "pull must not rewrite bundles")

	f.sync(t)
	require.NotEqual(t, before, read(t, manifest))
	require.NotContains(t, read(t, manifest), report.Bundles[0].Version)
}

func TestBundleIssuesValidateSkippedWithoutCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := bundleFixture(t)
	foundCLI(t)

	cli := &fakeCLI{}
	fakeRunner(t, cli)

	_, err := f.engine.BundlesEnable(t.Context(), "claude")
	require.NoError(t, err)

	missingCLI(t)

	cli.calls = nil

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "claude CLI not found"), "issues: %v", issues)
	require.Empty(t, cli.calls, "validate must not run without the claude CLI")
}
