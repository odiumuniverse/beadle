package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func codexPluginTree(t *testing.T, home, marketplace, name, version string) string {
	t.Helper()

	dir := filepath.Join(home, ".codex", "plugins", "cache", marketplace, name, version)
	write(t, filepath.Join(dir, "plugin.json"), fmt.Sprintf(`{"name": %q, "version": %q}`, name, version))

	return dir
}

func geminiExtensionTree(t *testing.T, home, name string) string {
	t.Helper()

	dir := filepath.Join(home, ".gemini", "extensions", name)
	write(t, filepath.Join(dir, "gemini-extension.json"), fmt.Sprintf(`{"name": %q, "version": "1.0.0"}`, name))

	return dir
}

func TestA47SourceIDsMatchAgents(t *testing.T) {
	Convey("Given the plugin sources and the agent hosts", t, func() {
		Convey("Then every plugin source is a known agent ID", func() {
			agents := agent.All(t.TempDir(), t.TempDir())

			for _, source := range plugin.SourceHosts() {
				So(agent.ByID(agents, source), ShouldNotBeNil)
			}

			So(plugin.SourceHosts()[0], ShouldEqual, plugin.SourceClaudeCode)
		})
	})
}

func TestA47CodexPluginIsParkedAndFarmed(t *testing.T) {
	Convey("Given a Codex plugin cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the Codex plugin is parked and farmed like a Claude plugin", func() {
				result := pluginResult(t, report, "acme/tool")
				So(result.Action, ShouldEqual, engine.PluginCreated)
				So(result.Target, ShouldEqual, dir)

				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, dir)

				want := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha")
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, want)
				So(farmLink(t, openCodeSkillsDir(f.home), "alpha"), ShouldEqual, want)

				rec := ledgerRecord(t, f, "acme/tool")
				So(rec.Source, ShouldEqual, plugin.SourceCodex)
			})
		})
	})
}

func TestA47DoctorReportsPluginSources(t *testing.T) {
	Convey("Given a Codex plugin and no other host", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		codexPluginTree(t, f.home, "acme", "tool", "1.0.0")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then every source reports its state", func() {
				So(hasIssue(issues, engine.SeverityInfo, "plugin source codex: 1 plugin(s) found"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source claude-code: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source cursor: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source gemini-cli: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source antigravity-cli: not installed"), ShouldBeTrue)
			})
		})
	})
}

func TestA47DoctorShowsReaderWarnings(t *testing.T) {
	Convey("Given a broken Gemini extension", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := geminiExtensionTree(t, f.home, "broken")
		write(t, filepath.Join(dir, "gemini-extension.json"), "{oops")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then the reader warning surfaces", func() {
				So(hasIssue(issues, engine.SeverityWarn, "plugin source gemini-cli: cannot parse"), ShouldBeTrue)
			})
		})
	})
}

func TestA47PinOfNonClaudePluginWarns(t *testing.T) {
	Convey("Given a version pin for a Codex plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		codexPluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		So(f.config.SetPluginPin(agent.ClaudeCodeID, "acme/tool", "1.0.0"), ShouldBeNil)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then the pin is reported as having no effect", func() {
				So(hasIssue(issues, engine.SeverityWarn, "belongs to codex; version pins cover the Claude Code plugin cache only"), ShouldBeTrue)
			})
		})
	})
}

func TestA47SameKeyFromTwoSourcesPresentsOnce(t *testing.T) {
	Convey("Given the same plugin installed in Claude and Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		claude := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, claude, "alpha", "# alpha\n")

		codex := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, codex, "alpha", "# alpha\n")

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the first source wins with a note", func() {
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, claude)
				So(ledgerRecord(t, f, "acme/tool").Source, ShouldEqual, plugin.SourceClaudeCode)
				So(containsWarning(report.Notes, "presenting the claude-code copy"), ShouldBeTrue)
				So(containsWarning(report.Warnings, "different content"), ShouldBeFalse)
			})
		})
	})
}

func TestA47UnreadableCopyDoesNotHideTheWorkingOne(t *testing.T) {
	Convey("Given a Claude registry entry whose install is gone and a working Codex copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		claude := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, claude, "alpha", "# alpha\n")

		codex := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, codex, "alpha", "# alpha\n")

		So(os.RemoveAll(claude), ShouldBeNil)

		f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the working copy is parked and presented", func() {
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, codex)
				So(ledgerRecord(t, f, "acme/tool").Source, ShouldEqual, plugin.SourceCodex)

				want := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha")
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, want)
			})
		})
	})
}

func TestA47SameKeyDivergentCopiesWarn(t *testing.T) {
	Convey("Given divergent copies of one plugin key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		claude := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, claude, "alpha", "# alpha\n")

		codex := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, codex, "alpha", "# different\n")

		report := f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When the sync runs", func() {
			Convey("Then the divergence is reported, not hidden", func() {
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, claude)
				So(containsWarning(report.Warnings, "different content"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "different content"), ShouldBeTrue)
			})
		})
	})
}
