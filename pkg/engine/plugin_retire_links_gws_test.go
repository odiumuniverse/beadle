package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

// TestPluginLinkSweepRepairsDanglingPivots covers the regression that left
// links behind when a plugin pivot was retired: the hosts that read their
// skills by pull (opencode, cursor, codex, gemini) and the shared agents home
// kept symlinks into a pivot directory that no longer exists.
func TestPluginLinkSweepRepairsDanglingPivots(t *testing.T) {
	Convey("Given a host link into a retired pivot and healthy neighbours", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.OpenCodeID)

		if err := f.config.Save(f.vault.ConfigPath()); err != nil {
			t.Fatalf("save config: %v", err)
		}

		hostSkills := filepath.Join(f.home, ".config", "opencode", "skills")
		dead := filepath.Join(f.vault.PluginsDir(), "vmkteam", "vmkteam-developer", "current")
		link := filepath.Join(hostSkills, "vmkteam-developer--api-health")

		So(os.MkdirAll(hostSkills, 0o750), ShouldBeNil)
		So(os.Symlink(filepath.Join(dead, "skills", "api-health"), link), ShouldBeNil)

		// A foreign file and a link into a live pivot must survive.
		write(t, filepath.Join(hostSkills, "foreign", "SKILL.md"), "# foreign\n")

		live := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
		So(os.MkdirAll(live, 0o750), ShouldBeNil)

		liveLink := filepath.Join(hostSkills, "acme--tool")
		So(os.Symlink(live, liveLink), ShouldBeNil)

		report := f.sync(t)

		Convey("Then the dangling link is gone and nothing else moved", func() {
			_, err := os.Lstat(link)
			So(os.IsNotExist(err), ShouldBeTrue)

			So(read(t, filepath.Join(hostSkills, "foreign", "SKILL.md")), ShouldEqual, "# foreign\n")

			info, err := os.Lstat(liveLink)
			So(err, ShouldBeNil)
			So(info.Mode()&os.ModeSymlink, ShouldNotEqual, 0)

			So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "dangling link")

			Convey("And a second sync has nothing left to repair", func() {
				again := f.sync(t)
				So(strings.Join(again.Notes, "\n"), ShouldNotContainSubstring, "dangling link")
			})
		})
	})
}
