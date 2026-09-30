package engine_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func versionedLink(t *testing.T, dir, name, target string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		t.Fatalf("symlink %s: %v", name, err)
	}
}

func hashTree(t *testing.T, root string) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}

			fmt.Fprintf(&out, "l %s -> %s\n", rel, link)
		case entry.IsDir():
			fmt.Fprintf(&out, "d %s\n", rel)
		default:
			data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp files
			if err != nil {
				return err
			}

			fmt.Fprintf(&out, "f %s %x\n", rel, sha256.Sum256(data))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	return out.String()
}

// installSkillsDir is where a plugin's skills live on this machine: the host
// cache directory the plugin manager installed them into. The farm's pivot
// used to sit in front of it, and the migration pointed there; it points at
// the install itself now.
func installSkillsDir(t *testing.T, f *fixture, marketplace, name string) string {
	t.Helper()

	installs := readRegistry(t, f.home)[name+"@"+marketplace]
	So(installs, ShouldNotBeEmpty)

	path, isString := installs[0]["installPath"].(string)
	So(isString, ShouldBeTrue)

	return filepath.Join(path, "skills")
}

func TestMigrateVersionedLinks(t *testing.T) {
	Convey("Given versioned links and a foreign brew link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
		versionedLink(t, dir, "beta", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0", "skills", "beta")+"/")

		brewTarget := filepath.Join(f.home, "brew", "tool")
		write(t, brewTarget, "brew\n")
		versionedLink(t, dir, "brew", brewTarget)

		f.sync(t)

		brewBefore := hashTree(t, filepath.Join(f.home, "brew"))

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		skills := installSkillsDir(t, f, "acme", "tool")

		Convey("When heal migrates", func() {
			alphaLink, err := os.Readlink(filepath.Join(dir, "alpha"))
			So(err, ShouldBeNil)

			betaLink, err := os.Readlink(filepath.Join(dir, "beta"))
			So(err, ShouldBeNil)

			brewLink, err := os.Readlink(filepath.Join(dir, "brew"))
			So(err, ShouldBeNil)

			resultsAgain, err := f.engine.MigrateHostSkills(t.Context(), false)

			So(results, ShouldHaveLength, 1)
			So(results[0].Key, ShouldEqual, "acme/tool")

			// Only the link that pointed at a version the plugin no longer has
			// moved; the other already points at the install the plugin manager
			// keeps, so it is left exactly as the user had it.
			So(results[0].Migrated, ShouldEqual, 1)

			So(alphaLink, ShouldEqual, plugin+"/skills/alpha/")
			So(read(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# alpha\n")
			So(betaLink, ShouldEqual, filepath.Join(skills, "beta"))
			So(read(t, filepath.Join(dir, "beta", "SKILL.md")), ShouldEqual, "# beta\n")

			Convey("Then foreign links stay and a repeated heal is a noop", func() {
				So(err, ShouldBeNil)
				So(brewLink, ShouldEqual, brewTarget)
				So(hashTree(t, filepath.Join(f.home, "brew")), ShouldEqual, brewBefore)
				So(err, ShouldBeNil)
				So(resultsAgain, ShouldBeEmpty)
			})
		})
	})
}

func TestMigrateOwnerGate(t *testing.T) {
	Convey("Given two plugins claiming one skill name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "aaa", "first", "1.0.0")
		writeSkill(t, first, "shared", "# first\n")

		second := pluginTree(t, f.home, "bbb", "second", "1.0.0")
		writeSkill(t, second, "shared", "# second\n")

		f.sync(t)

		// Two hosts hold the same skill as a fork - a real directory with the
		// plugin's content. A link straight into the plugin's own install is
		// already where it belongs and stays untouched, so the owner gate is
		// seen on forks, one per host.
		claudeDir := claudeSkillsDir(f.home)
		write(t, filepath.Join(claudeDir, "shared", "SKILL.md"), read(t, filepath.Join(first, "skills", "shared", "SKILL.md")))

		openDir := openCodeSkillsDir(f.home)
		write(t, filepath.Join(openDir, "shared", "SKILL.md"), read(t, filepath.Join(second, "skills", "shared", "SKILL.md")))

		f.sync(t)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When the migration runs", func() {
			link, err := os.Readlink(filepath.Join(claudeDir, "shared"))
			So(err, ShouldBeNil)

			Convey("Then the fork of the owning plugin migrates", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "aaa/first")
				So(results[0].Migrated, ShouldEqual, 1)

				// One row per plugin: the second host's copy of the same skill
				// is the owner's plugin too, so its refusal is named here.
				So(results[0].Note, ShouldContainSubstring, "drifted copy of aaa/first")

				So(link, ShouldEqual, filepath.Join(installSkillsDir(t, f, "aaa", "first"), "shared"))
			})

			Convey("And the other host's diverged copy is kept where it is", func() {
				So(read(t, filepath.Join(openDir, "shared", "SKILL.md")), ShouldEqual, "# second\n")
			})
		})
	})
}

func TestMigrateKeepsUnresolvableLink(t *testing.T) {
	Convey("Given an unresolvable plugin link", t, func() {
		Convey("When the skill is gone from the lot", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			dir := claudeSkillsDir(f.home)
			versionedLink(t, dir, "ghost", plugin+"/skills/ghost/")

			f.sync(t)

			results, err := f.engine.MigrateHostSkills(t.Context(), false)
			So(err, ShouldBeNil)

			link, err := os.Readlink(filepath.Join(dir, "ghost"))
			So(err, ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			So(results, ShouldHaveLength, 1)
			So(results[0].Migrated, ShouldEqual, 0)
			So(results[0].Note, ShouldContainSubstring, "no longer offers skill ghost")
			So(link, ShouldEqual, plugin+"/skills/ghost/")
			So(hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache"), ShouldBeTrue)
		})

		Convey("When the plugin is not parked", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			f.sync(t)

			removeFromRegistry(t, f.home, "acme", "tool")
			So(os.RemoveAll(plugin), ShouldBeNil)

			dir := claudeSkillsDir(f.home)
			versionedLink(t, dir, "ghost", plugin+"/skills/alpha/")

			results, err := f.engine.MigrateHostSkills(t.Context(), false)
			So(err, ShouldBeNil)

			link, err := os.Readlink(filepath.Join(dir, "ghost"))
			So(err, ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			So(results, ShouldHaveLength, 1)
			So(results[0].Migrated, ShouldEqual, 0)
			So(results[0].Note, ShouldContainSubstring, "is not parked")
			So(link, ShouldEqual, plugin+"/skills/alpha/")
			So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked"), ShouldBeTrue)
			// The plugin is gone, so the link dangles: with the farm gone there
			// is no exemption for a link into the cache - a broken link is a
			// broken link.
			So(hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "ghost")), ShouldBeTrue)
		})
	})
}

func TestMigrateIdenticalFork(t *testing.T) {
	Convey("Given an identical fork of a farmed skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		dir := openCodeSkillsDir(f.home)
		So(os.RemoveAll(filepath.Join(dir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		link, err := os.Readlink(filepath.Join(dir, "alpha"))
		So(err, ShouldBeNil)

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		links, err := filepath.Glob(filepath.Join(dir, "*.migrating"))
		So(err, ShouldBeNil)

		Convey("When heal migrates", func() {
			Convey("Then the fork is replaced by a link to the install with no aside copies", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "acme/tool")
				So(results[0].Migrated, ShouldEqual, 1)
				So(results[0].Note, ShouldBeEmpty)

				So(link, ShouldEqual, filepath.Join(installSkillsDir(t, f, "acme", "tool"), "alpha"))
				So(read(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# alpha\n")
				So(entries, ShouldBeEmpty)
				So(links, ShouldBeEmpty)
			})
		})
	})
}

func TestMigrateForkAsideConflict(t *testing.T) {
	Convey("Given an occupied aside name while a fork must migrate", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		f.sync(t)

		dir := openCodeSkillsDir(f.home)

		So(os.RemoveAll(filepath.Join(dir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		aside := filepath.Join(dir, "alpha.migrating")
		write(t, aside, "occupied\n")

		// Claude is the plugin's own host (native): it owns no farm link, so
		// seed the legacy cache link directly.
		claudeDir := claudeSkillsDir(f.home)
		versionedLink(t, claudeDir, "beta", plugin+"/skills/beta/")

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		fork := filepath.Join(dir, "alpha")
		info, err := os.Lstat(fork)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then the aside conflict is noted and the fork stays a directory", func() {
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "cannot replace "+fork)
				So(results[0].Note, ShouldNotContainSubstring, "restored")

				So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
				So(read(t, filepath.Join(fork, "SKILL.md")), ShouldEqual, "# alpha\n")
				So(read(t, aside), ShouldEqual, "occupied\n")
			})
		})
	})
}

func TestMigrateIdenticalForkBeforeSync(t *testing.T) {
	Convey("Given an identical fork present before the first sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		dir := openCodeSkillsDir(f.home)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		f.sync(t)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		link, err := os.Readlink(filepath.Join(dir, "alpha"))
		So(err, ShouldBeNil)

		Convey("When heal migrates", func() {
			Convey("Then the canon adopts the fork and it migrates", func() {
				_, canonErr := os.Stat(f.vaultSkill("alpha"))
				So(canonErr, ShouldBeNil)

				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "acme/tool")
				// Two migrations: OpenCode's pre-sync fork and the identical
				// canon copy Claude received (Claude is the plugin's own host,
				// so it never held a farm link there).
				So(results[0].Migrated, ShouldEqual, 2)

				So(link, ShouldEqual, filepath.Join(installSkillsDir(t, f, "acme", "tool"), "alpha"))
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha\n")
			})
		})
	})
}

func TestMigrateDriftedForkBeforeSyncKeepsAndWarns(t *testing.T) {
	Convey("Given a drifted fork before the first sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		dir := openCodeSkillsDir(f.home)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alphA\n")

		f.sync(t)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		info, err := os.Lstat(filepath.Join(dir, "alpha"))
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then a drifted fork stays and warns", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "drifted copy of acme/tool")
				So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
				So(read(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# alphA\n")
				So(hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), ShouldBeTrue)
			})
		})
	})
}

func TestMigrateIdenticalForkDivergedCanonKeepsBoth(t *testing.T) {
	Convey("Given an identical fork under a diverged canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		f.sync(t)

		write(t, f.vaultSkill("alpha"), "# canon edit\n")

		dir := openCodeSkillsDir(f.home)
		So(os.RemoveAll(filepath.Join(dir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		// Claude is the plugin's own host (native): it owns no farm link, so
		// seed the legacy cache link directly.
		claudeDir := claudeSkillsDir(f.home)
		versionedLink(t, claudeDir, "beta", plugin+"/skills/beta/")

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		fork := filepath.Join(dir, "alpha")
		info, err := os.Lstat(fork)
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then both copies are kept and it warns about the divergence", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "matches acme/tool but the vault copy differs; keeping both")

				So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
				So(read(t, filepath.Join(fork, "SKILL.md")), ShouldEqual, "# alpha\n")

				So(hasIssue(issues, engine.SeverityWarn, "skill alpha matches acme/tool but the vault copy differs; keeping both"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), ShouldBeFalse)
			})
		})
	})
}

func TestMigrateForkExtrasKeep(t *testing.T) {
	Convey("Given a fork with extra entries", t, func() {
		cases := []struct {
			desc  string
			extra func(t *testing.T, fork string)
		}{
			{desc: "junk directory", extra: func(t *testing.T, fork string) {
				t.Helper()

				write(t, filepath.Join(fork, "node_modules", "pkg", "index.js"), "x\n")
			}},
			{desc: "symlink", extra: func(t *testing.T, fork string) {
				t.Helper()

				if err := os.Symlink("/tmp", filepath.Join(fork, "inner")); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			}},
			{desc: "empty directory", extra: func(t *testing.T, fork string) {
				t.Helper()

				if err := os.MkdirAll(filepath.Join(fork, "empty"), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			}},
		}

		for _, tc := range cases {
			Convey("When the fork has an "+tc.desc, func() {
				t.Setenv("XDG_CONFIG_HOME", "")

				f := newFixture(t)
				f.emptyConfigs(t)

				plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
				writeSkill(t, plugin, "alpha", "# alpha\n")

				f.sync(t)

				dir := openCodeSkillsDir(f.home)
				fork := filepath.Join(dir, "alpha")

				So(os.RemoveAll(fork), ShouldBeNil)
				write(t, filepath.Join(fork, "SKILL.md"), "# alpha\n")
				tc.extra(t, fork)

				before := hashTree(t, fork)

				results, err := f.engine.MigrateHostSkills(t.Context(), false)
				So(err, ShouldBeNil)

				info, err := os.Lstat(fork)
				So(err, ShouldBeNil)

				Convey("Then the whole fork is kept", func() {
					So(results, ShouldHaveLength, 1)
					So(results[0].Migrated, ShouldEqual, 0)
					So(results[0].Note, ShouldContainSubstring, "drifted copy of acme/tool")
					So(hashTree(t, fork), ShouldEqual, before)
					So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
				})
			})
		}
	})
}

func TestMigrateDriftedForkKept(t *testing.T) {
	Convey("Given a drifted fork", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		dir := openCodeSkillsDir(f.home)
		fork := filepath.Join(dir, "alpha")

		So(os.RemoveAll(fork), ShouldBeNil)
		write(t, filepath.Join(fork, "SKILL.md"), "# alphA\n")

		before := hashTree(t, fork)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then the fork stays byte-identical and warns", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "drifted copy of acme/tool")
				So(hashTree(t, fork), ShouldEqual, before)
				So(hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), ShouldBeTrue)
			})
		})
	})
}

func TestMigrateForeignDirUntouched(t *testing.T) {
	Convey("Given a foreign directory no plugin owns", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		foreign := filepath.Join(claudeSkillsDir(f.home), "notes")
		write(t, filepath.Join(foreign, "SKILL.md"), "# notes\n")

		before := hashTree(t, foreign)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then it is untouched", func() {
				So(results, ShouldBeEmpty)
				So(hashTree(t, foreign), ShouldEqual, before)
			})
		})
	})
}

func TestMigrateDryRunAndNoDirs(t *testing.T) {
	Convey("Given a fork and a missing host dir", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		dir := openCodeSkillsDir(f.home)
		So(os.RemoveAll(filepath.Join(dir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		before := hashTree(t, dir)

		results, err := f.engine.MigrateHostSkills(t.Context(), true)
		So(err, ShouldBeNil)

		Convey("When a dry run runs", func() {
			Convey("Then it predicts the migration and changes nothing", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 1)
				So(hashTree(t, dir), ShouldEqual, before)
			})
		})

		Convey("When the host directory is missing and a real heal runs", func() {
			So(os.RemoveAll(claudeSkillsDir(f.home)), ShouldBeNil)

			resultsReal, err := f.engine.MigrateHostSkills(t.Context(), false)
			So(err, ShouldBeNil)

			_, claudeErr := os.Stat(claudeSkillsDir(f.home))

			Convey("Then it still migrates and never creates the missing dir", func() {
				So(resultsReal, ShouldHaveLength, 1)
				So(resultsReal[0].Migrated, ShouldEqual, 1)
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestMigrateNoopAfterHeal(t *testing.T) {
	Convey("Given a migrated fork", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		dir := openCodeSkillsDir(f.home)
		So(os.RemoveAll(filepath.Join(dir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)
		So(results, ShouldHaveLength, 1)

		Convey("When heal and sync run again", func() {
			resultsAgain, err := f.engine.MigrateHostSkills(t.Context(), false)
			So(err, ShouldBeNil)

			report := f.sync(t)

			entries, err := os.ReadDir(f.vault.SkillsDir())
			So(err, ShouldBeNil)

			Convey("Then both are noops and the canon stays empty", func() {
				So(results[0].Migrated, ShouldEqual, 1)
				So(resultsAgain, ShouldBeEmpty)

				So(report.Action(kind.Skills, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestHealReportsMigration(t *testing.T) {
	Convey("Given a fork and a versioned link to migrate", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		f.sync(t)

		// Claude is the plugin's own host (native): it owns no farm link, so
		// seed the legacy cache link directly.
		claudeDir := claudeSkillsDir(f.home)
		versionedLink(t, claudeDir, "alpha", plugin+"/skills/alpha/")

		openCodeDir := openCodeSkillsDir(f.home)

		So(os.RemoveAll(filepath.Join(openCodeDir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(openCodeDir, "alpha", "SKILL.md"), "# alpha\n")

		So(os.RemoveAll(filepath.Join(openCodeDir, "beta")), ShouldBeNil)
		write(t, filepath.Join(openCodeDir, "beta", "SKILL.md"), "# betA\n")

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then the migration and the drift note are reported", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "acme/tool")
				So(results[0].Migrated, ShouldEqual, 1)
				So(results[0].Note, ShouldContainSubstring, "drifted copy of acme/tool; keeping the local version")
			})
		})
	})
}

func TestDoctorPluginCacheLinkModeOff(t *testing.T) {
	Convey("Given a mode-off skills surface with a plugin-cache link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "alpha", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "9.9.9", "skills", "alpha")+"/")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it errors on the broken symlink and never scans mode-off surfaces", func() {
				So(hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "alpha")), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "points into the plugin cache"), ShouldBeFalse)
			})
		})
	})
}

func TestDoctorPluginMigrationIssues(t *testing.T) {
	Convey("Given migration issues plus a foreign broken link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
		versionedLink(t, dir, "beta", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0", "skills", "beta")+"/")

		broken := filepath.Join(dir, "broken")
		So(os.Symlink(filepath.Join(f.home, "missing"), broken), ShouldBeNil)

		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(os.RemoveAll(filepath.Join(openCodeDir, "alpha")), ShouldBeNil)
		write(t, filepath.Join(openCodeDir, "alpha", "SKILL.md"), "# alphA\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			// A link that resolves into the plugin's own cache is where it
			// belongs now, so only the fork and the dangling links are named.
			So(hasIssue(issues, engine.SeverityWarn, "skill alpha points into the plugin cache"), ShouldBeFalse)
			So(hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityError, "broken symlink: "+broken), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "beta")), ShouldBeTrue)

			_, err = f.engine.MigrateHostSkills(t.Context(), false)
			So(err, ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then after heal the migration warning is gone but drift and foreign errors stay", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "points into the plugin cache")
				}

				So(hasIssue(issues, engine.SeverityWarn, "drifted copy of acme/tool"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "broken symlink: "+broken), ShouldBeTrue)
			})
		})
	})
}

func TestMigrationReasonNotParked(t *testing.T) {
	Convey("Given a link into an unparked plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		So(os.RemoveAll(plugin), ShouldBeNil)

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "ghost", plugin+"/skills/alpha/")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then doctor names the parking problem and the migration says why it did nothing", func() {
				So(hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache; plugin acme/tool is not parked"), ShouldBeTrue)
				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "is not parked")
			})
		})
	})
}

func TestMigrationReasonOwnerTaken(t *testing.T) {
	Convey("Given an owner conflict across two plugins", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "aaa", "first", "1.0.0")
		writeSkill(t, first, "shared", "# first\n")

		second := pluginTree(t, f.home, "bbb", "second", "1.0.0")
		writeSkill(t, second, "shared", "# second\n")
		writeSkill(t, second, "omega", "# omega\n")

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "shared-a", first+"/skills/shared/")
		versionedLink(t, dir, "shared-b", second+"/skills/shared/")
		versionedLink(t, dir, "omega", second+"/skills/omega/")

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			byKey := map[string]engine.MigrateResult{}
			for _, result := range results {
				byKey[result.Key] = result
			}

			Convey("Then the owner's link stays put and the loser is left in place", func() {
				So(hasIssue(issues, engine.SeverityWarn, "skill shared is provided by aaa/first"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "plugin bbb/second is not parked"), ShouldBeFalse)

				// The link the owner wins stays exactly where it is - it
				// already points at the plugin's own install - and the loser's
				// link is refused with a reason.
				So(results, ShouldHaveLength, 1)
				So(byKey["bbb/second"].Migrated, ShouldEqual, 0)
				So(byKey["bbb/second"].Note, ShouldContainSubstring, "skill shared is provided by aaa/first; shared-b left in place")
			})
		})
	})
}

func TestMigrationReasonSkillGone(t *testing.T) {
	Convey("Given a link to a skill the plugin no longer offers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
		versionedLink(t, dir, "ghost", plugin+"/skills/ghost/")

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		results, err := f.engine.MigrateHostSkills(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then the missing skill is named and left in place", func() {
				So(hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache; plugin acme/tool no longer offers skill ghost"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked"), ShouldBeFalse)

				So(results, ShouldHaveLength, 1)
				So(results[0].Migrated, ShouldEqual, 0)
				So(results[0].Note, ShouldContainSubstring, "plugin acme/tool no longer offers skill ghost; ghost left in place")
			})
		})
	})
}
