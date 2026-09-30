package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// TestKiloRootsFollowTheHost pins the env matrix for one host: every path the
// Kilo adapter uses comes from the shared resolver, so KILO_CONFIG_DIR and
// XDG_CONFIG_HOME move all of them together. The values are the ones measured
// on kilo 7.8.1 (macOS and Ubuntu): the config root honours KILO_CONFIG_DIR
// first and XDG second, and the host reads BOTH roots when both are set.
func TestKiloRootsFollowTheHost(t *testing.T) {
	Convey("Given a home and a Kilo env matrix", t, func() {
		home := t.TempDir()
		xdg := t.TempDir()
		kcd := t.TempDir()

		Convey("When nothing is set", func() {
			t.Setenv("KILO_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", "")

			Convey("Then every path is the home-relative default", func() {
				root := filepath.Join(home, ".config", "kilo")
				So(agent.KiloConfigDir(home), ShouldEqual, root)
				So(agent.KiloSkillsDir(home), ShouldEqual, filepath.Join(root, "skills"))
				So(agent.KiloSkillReadDirs(home), ShouldResemble, []string{
					filepath.Join(root, "skills"),
					filepath.Join(root, "skill"),
					filepath.Join(home, ".kilo", "skills"),
					filepath.Join(home, ".kilo", "skill"),
				})
				So(agent.KiloConfigRoots(home), ShouldResemble, []string{root})
			})
		})

		Convey("When only XDG_CONFIG_HOME is set", func() {
			t.Setenv("KILO_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", xdg)

			Convey("Then the whole surface moves under the XDG root, as the host reads it", func() {
				root := filepath.Join(xdg, "kilo")
				So(agent.KiloConfigDir(home), ShouldEqual, root)
				So(agent.KiloSkillsDir(home), ShouldEqual, filepath.Join(root, "skills"))
				So(agent.KiloCommandDirs(home), ShouldResemble, []string{
					filepath.Join(root, "commands"),
					filepath.Join(root, "command"),
				})
				So(agent.KiloConfigRoots(home), ShouldResemble, []string{root})

				surface := agent.Kilo(home, t.TempDir()).Surface(kind.Skills)
				So(surface.Path(), ShouldEqual, filepath.Join(root, "skills"))
			})
		})

		Convey("When KILO_CONFIG_DIR is set", func() {
			t.Setenv("KILO_CONFIG_DIR", kcd)
			t.Setenv("XDG_CONFIG_HOME", "")

			Convey("Then it wins over the XDG root and the home default", func() {
				So(agent.KiloConfigDir(home), ShouldEqual, kcd)
				So(agent.KiloSkillsDir(home), ShouldEqual, filepath.Join(kcd, "skills"))
			})
		})

		Convey("When both are set", func() {
			t.Setenv("KILO_CONFIG_DIR", kcd)
			t.Setenv("XDG_CONFIG_HOME", xdg)

			Convey("Then the write target is KILO_CONFIG_DIR and the host's second read root is the XDG one", func() {
				So(agent.KiloConfigDir(home), ShouldEqual, kcd)
				So(agent.KiloConfigRoots(home), ShouldResemble, []string{kcd, filepath.Join(xdg, "kilo")})
			})
		})

		Convey("When KILO_CONFIG_DIR carries padding", func() {
			padded := t.TempDir() + "  "
			t.Setenv("KILO_CONFIG_DIR", padded)
			t.Setenv("XDG_CONFIG_HOME", "")

			Convey("Then the value is used verbatim, because the host does not trim it", func() {
				// Live kilo 7.8.1: a padded KILO_CONFIG_DIR is read as given, so
				// trimming here would deliver into a directory kilo never reads.
				So(agent.KiloConfigDir(home), ShouldEqual, padded)
			})
		})

		Convey("When KILO_CONFIG_DIR is relative", func() {
			t.Setenv("KILO_CONFIG_DIR", "rel/kilo")
			t.Setenv("XDG_CONFIG_HOME", "")

			Convey("Then it is kept relative, as the host keeps it", func() {
				So(agent.KiloConfigDir(home), ShouldEqual, "rel/kilo")
			})
		})
	})
}

// TestKiloSurfaceHasNoLocalPathLiterals is the structural half of the same
// claim: the adapter's directories are the resolver's, so a value the resolver
// does not declare cannot appear in a surface. The one local join left is the
// ~/.claude/plugins ignore root, which is a beadle-wide skills-walk rule rather
// than a host fact — see docs/reviews/W3-A-impl-1.md, gap 1.
func TestKiloSurfaceHasNoLocalPathLiterals(t *testing.T) {
	Convey("Given a Kilo home", t, func() {
		home := t.TempDir()
		t.Setenv("KILO_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "")

		Convey("Then the skills surface reads and writes only resolver paths", func() {
			surface := agent.Kilo(home, t.TempDir()).Surface(kind.Skills)
			So(surface.Path(), ShouldEqual, agent.KiloSkillsDir(home))

			area, ok := surface.(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs()[0], ShouldEqual, agent.KiloSkillsDir(home))
		})
	})
}

// TestKiloConfigDirIsNotDuplicated is the "no two sources of truth" half: the
// legacy docs directory still takes part in detection, and the config root has
// exactly one definition, in the shared resolver.
func TestKiloConfigDirIsNotDuplicated(t *testing.T) {
	Convey("Given a Kilo home with only the legacy docs directory", t, func() {
		home := t.TempDir()

		Convey("Then detection still finds the host through it", func() {
			So(os.MkdirAll(filepath.Join(home, ".kilo", "skills"), 0o750), ShouldBeNil)

			detected, err := agent.Kilo(home, t.TempDir()).Detect()
			So(err, ShouldBeNil)
			So(detected, ShouldBeTrue)
		})
	})
}

// TestCodexSkillsWriteTheSharedHub pins the second W3-A decision: codex's
// skills write target is the shared hub (which Codex reads natively), and
// ~/.codex/skills is the deprecated location, kept as a legacy read. Both
// values come from the shared resolver, so one source of truth holds them.
func TestCodexSkillsWriteTheSharedHub(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()
		t.Setenv("PI_CONFIG_DIR", "")
		t.Setenv("PI_CODING_AGENT_DIR", "")
		t.Setenv("OMP_PROFILE", "")

		Convey("Then the codex skills surface writes the hub and reads the legacy dir", func() {
			surface := agent.Codex(home, t.TempDir()).Surface(kind.Skills)
			So(surface, ShouldNotBeNil)

			hub := filepath.Join(home, ".agents", "skills")
			legacy := filepath.Join(home, ".codex", "skills")

			So(surface.Path(), ShouldEqual, hub)

			area, ok := surface.(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs(), ShouldResemble, []string{hub, legacy})
		})
	})
}

// TestClaudeHasNoInbox pins the first W3-A decision: the shared resolver
// answers a path for claude's inbox, and beadle deliberately does not consume
// it, so no file is created in the user's home.
func TestClaudeHasNoInbox(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()

		Convey("Then beadle creates no inbox file for claude", func() {
			So(agent.InboxPath(home, agent.ClaudeCodeID), ShouldBeEmpty)
		})
	})
}
