package engine_test

import (
	"fmt"
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
				So(hasIssue(issues, engine.SeverityInfo, "plugin source claude: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source cursor: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source gemini: not installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source agy: not installed"), ShouldBeTrue)
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
				So(hasIssue(issues, engine.SeverityWarn, "plugin source gemini: cannot parse"), ShouldBeTrue)
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

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When the doctor runs", func() {
			Convey("Then the divergence is reported, not hidden", func() {
				So(hasIssue(issues, engine.SeverityWarn, "different content"), ShouldBeTrue)
			})
		})
	})
}
