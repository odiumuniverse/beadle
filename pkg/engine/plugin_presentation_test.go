package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func presentationFixture(t *testing.T, hosts ...string) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	for _, id := range hosts {
		f.config.Enable(id)
	}

	if slices.Contains(hosts, agent.GeminiCLIID) {
		write(t, f.geminiSettings(), `{"mcpServers": {}}`)
	}

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# alpha in the cache\n")

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "plugged", "# plugged\n")

	f.sync(t)

	return f
}

func cacheSkillPath(home, version, skillName string) string {
	return filepath.Join(claudePluginsDir(home), "cache", "acme", "tool", version, "skills", skillName)
}

func pivotSkillPath(f *fixture, skillName string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", skillName)
}

func requireGeminiCopy(t *testing.T, f *fixture, content string) {
	t.Helper()

	path := filepath.Join(f.home, ".gemini", "skills", "alpha", "SKILL.md")

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("Gemini holds a symlink, expected a real copy")
	}

	if got := read(t, path); got != content {
		t.Fatalf("gemini copy = %q, want %q", got, content)
	}
}

func TestPluginPresentationDirectCacheLinkIsInvisible(t *testing.T) {
	Convey("Given a foreign direct cache link in the Claude skills dir", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := presentationFixture(t)

		claudeLink := filepath.Join(claudeSkillsDir(f.home), "plugged")
		So(os.Remove(claudeLink), ShouldBeNil)
		versionedLink(t, claudeSkillsDir(f.home), "plugged", cacheSkillPath(f.home, "1.0.0", "plugged"))

		Convey("When sync and doctor run", func() {
			report := f.sync(t)

			entries, err := os.ReadDir(f.vault.SkillsDir())
			So(err, ShouldBeNil)

			link, linkErr := os.Readlink(claudeLink)
			So(linkErr, ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the cache link never reaches the canon or is re-pointed", func() {
				So(report.Kind(kind.Skills).VaultChanged, ShouldBeFalse)

				for _, change := range report.Kind(kind.Skills).Pulled {
					So(change.Key, ShouldNotContainSubstring, "plugged")
				}

				So(entries, ShouldBeEmpty)
				So(link, ShouldEqual, cacheSkillPath(f.home, "1.0.0", "plugged"))

				So(farmLink(t, openCodeSkillsDir(f.home), "plugged"), ShouldEqual, pivotSkillPath(f, "plugged"))
				So(hasIssue(issues, engine.SeverityError, "plugged"), ShouldBeFalse)
			})
		})
	})
}

func TestPluginPresentationExternalChainIsInvisible(t *testing.T) {
	Convey("Given a link chain ending in the plugin cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := presentationFixture(t)

		dock := filepath.Join(f.home, "dock", "alpha")
		So(os.MkdirAll(filepath.Dir(dock), 0o750), ShouldBeNil)
		So(os.Symlink(cacheSkillPath(f.home, "2.0.0", "alpha"), dock), ShouldBeNil)
		versionedLink(t, claudeSkillsDir(f.home), "alpha", dock)

		Convey("When sync runs", func() {
			report := f.sync(t)

			entries, err := os.ReadDir(f.vault.SkillsDir())
			So(err, ShouldBeNil)

			Convey("Then the chain never reaches the canon", func() {
				So(report.Kind(kind.Skills).VaultChanged, ShouldBeFalse)
				So(report.Kind(kind.Skills).Pulled, ShouldBeEmpty)
				So(report.Action(kind.Skills, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginPresentationDockRoundTrip(t *testing.T) {
	Convey("Given a dock link into the Claude skills dir", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := presentationFixture(t, agent.GeminiCLIID, agent.SharedID)

		dock := filepath.Join(sharedSkillsDir(f.home), "alpha")
		write(t, filepath.Join(dock, "SKILL.md"), "# alpha v1\n")
		versionedLink(t, claudeSkillsDir(f.home), "alpha", dock)

		report := f.sync(t)

		Convey("When the dock is edited and then removed", func() {
			write(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md"), "# alpha v2\n")

			f.sync(t)

			So(os.RemoveAll(dock), ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then edits pull through and removal surfaces a broken link", func() {
				So(report.Kind(kind.Skills).VaultChanged, ShouldBeTrue)
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha v2\n")

				requireGeminiCopy(t, f, "# alpha v2\n")

				So(hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeTrue)
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha v2\n")
			})
		})
	})
}
