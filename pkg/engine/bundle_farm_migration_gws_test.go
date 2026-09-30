package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// A user who ran beadle 0.4.2 has a plugin farm this build does not recognise:
// a rendered Claude marketplace under `bundles/<host>`, recorded in the state as
// a top-level `bundles` key. Nothing in the tree says "0.4.2", so the migration
// has to be told by the shape — and it has to move the farm, not leave it
// enabled, invisible and orphaned while the upgrade reports success.
//
// The fixture is a real 0.4.2 vault: the GitHub release `beadle 0.4.2`
// (darwin-universal tarball, sha256
// 2e728a5f8167e077c1a4c750625b8e0b647227689e3fcb9da5691011348d511d, the same digest the
// tap formula pins) with `init`, `agents enable claude-code` and
// `bundles enable --host claude` in an isolated HOME.
func TestAnUpgradeFrom042MovesItsBundleFarmToThePluginManager(t *testing.T) {
	Convey("Given a vault a real beadle 0.4.2 wrote", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		install042Vault(t, f.vault)

		Convey("When beadle syncs it", func() {
			report := f.sync(t)

			Convey("Then the state is a version this build owns", func() {
				st := farmState(t, f.vault)
				So(st.Version, ShouldEqual, state.CurrentVersion)
			})

			Convey("Then what the farm wrote is no longer there", func() {
				// `bundles/<host>` is a directory this build renders too, so
				// the farm's own content is replaced rather than the directory
				// vanishing: the assertion is on the bytes, not on the path.
				So(read(t, filepath.Join(f.vault.Root(), "bundles", "claude", ".claude-plugin", "marketplace.json")),
					ShouldNotEqual, read(t, filepath.Join("testdata", "beadle-0.4.2-bundle-farm", "bundles", "claude",
						".claude-plugin", "marketplace.json")))
			})

			Convey("Then what was removed is in a backup first", func() {
				backups := backupsFor(t, f.vault)
				So(backups, ShouldNotBeEmpty)
				So(containsFile(backups, filepath.Join("bundles", "claude", ".claude-plugin", "marketplace.json")),
					ShouldBeTrue)
			})

			Convey("Then the canon travels as a package of the plugin manager", func() {
				// The whole point of the upgrade: the canon the 0.4.2 farm
				// pushed into Claude is now delivered by the library, from a
				// spec that names it.
				So(f.manager.published, ShouldNotBeEmpty)
				So(f.manager.published[0].dir, ShouldEqual, f.vault.CanonPackageDir())
				So(f.manager.published[0].hosts, ShouldContain, agent.ClaudeCodeID)
			})

			Convey("Then the state no longer claims a bundle this build does not keep", func() {
				st := farmState(t, f.vault)
				So(st.Bundles[agent.ClaudeCodeID].Enabled, ShouldBeFalse)
			})

			Convey("Then the user is told, because their farm moved", func() {
				// The note names the paths and where the copy is. It does NOT
				// name a beadle release: the state carries a schema version,
				// and a migration that guessed "0.4.2" from it would be
				// printing a fact it does not have.
				notes := strings.Join(report.Notes, "\n")
				So(notes, ShouldContainSubstring, "bundles/claude")
				So(notes, ShouldContainSubstring, "moved the plugin farm")
				So(notes, ShouldContainSubstring, "backed up to")
			})
		})

		Convey("When beadle syncs it a second time", func() {
			f.sync(t)
			report := f.sync(t)

			Convey("Then it does nothing and says nothing about a farm", func() {
				So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "moved the plugin farm")
				So(f.manager.published, ShouldHaveLength, 1)
			})
		})
	})
}

// The doctor is what a user runs BEFORE syncing, so it is the only place the
// farm can still be named.
func TestDoctorNamesTheBundleFarmBeforeItIsMoved(t *testing.T) {
	Convey("Given a vault a real beadle 0.4.2 wrote", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		install042Vault(t, f.vault)

		Convey("When beadle doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the farm is named, with the command that moves it", func() {
				var found bool

				for _, issue := range issues {
					// The bundle's own doctor line also names
					// `bundles/claude` — and on this vault it advises the
					// 0.4.2 marketplace command, which is the advice the
					// upgrade exists to retire. So the finding is matched on
					// what it says about the farm, not on the path alone.
					if !strings.Contains(issue.Message, "nothing applies it") {
						continue
					}

					found = true

					So(issue.Message, ShouldContainSubstring, "bundles/claude")
					So(issue.Message, ShouldContainSubstring, "beadle sync")
				}

				So(found, ShouldBeTrue)
			})
		})
	})
}

// install042Vault copies the recorded 0.4.2 vault over the fixture's own, so
// the migration sees the tree and the state exactly as 0.4.2 left them.
func install042Vault(t *testing.T, v *vault.Vault) {
	t.Helper()

	copyTree(t, filepath.Join("testdata", "beadle-0.4.2-bundle-farm"), v.Root())

	st, err := state.Load(v.StatePath())
	if err != nil {
		t.Fatalf("load the 0.4.2 state: %v", err)
	}

	// The version the FILE carried, not the one Load leaves in memory: a state
	// from 0.4.2 reads as current the moment anything loads it, which is
	// exactly why the migration asks for the loaded version.
	if st.LoadedVersion() != 2 {
		t.Fatalf("the fixture is not a 0.4.2 state: state version %d", st.LoadedVersion())
	}
}

// backupsFor lists the backup directories the migration left under the vault's
// own state directory.
func backupsFor(t *testing.T, v *vault.Vault) []string {
	t.Helper()

	root := filepath.Join(v.Root(), "state", "backups")

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var dirs []string

	for _, entry := range entries {
		dirs = append(dirs, filepath.Join(root, entry.Name()))
	}

	return dirs
}

// containsFile reports whether any of the directories holds one relative path.
func containsFile(dirs []string, rel string) bool {
	for _, dir := range dirs {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			return true
		}
	}

	return false
}

func farmState(t *testing.T, v *vault.Vault) *state.State {
	t.Helper()

	st, err := state.Load(v.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	return st
}

// A file inside the farm that beadle cannot account for is the user's, whoever
// put it there. The upgrade moves the farm and leaves that file alone, says so,
// and does not count the directory as gone.
func TestTheUpgradeLeavesAFileTheFarmDidNotWrite(t *testing.T) {
	Convey("Given a 0.4.2 farm with a file beadle did not write", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		install042Vault(t, f.vault)

		// An MCP server the user added inside the farm directory, and a skill
		// of their own: neither is in the vault canon, so neither is beadle's.
		own := filepath.Join(f.vault.Root(), "bundles", "claude", "plugins", "beadle-canon", "skills", "mine", "SKILL.md")
		mineBody := "---\nname: mine\n---\nmine\n"

		So(os.MkdirAll(filepath.Dir(own), 0o700), ShouldBeNil)
		So(os.WriteFile(own, []byte(mineBody), 0o600), ShouldBeNil)

		server := filepath.Join(f.vault.Root(), "bundles", "claude", "plugins", "beadle-canon", ".mcp.json")
		So(os.WriteFile(server, []byte(`{"mcpServers":{"mine":{"command":"mine"}}}`), 0o600), ShouldBeNil)

		Convey("When beadle syncs it", func() {
			report := f.sync(t)

			Convey("Then the canon is a package of the plugin manager anyway", func() {
				So(f.manager.published, ShouldHaveLength, 1)
				So(f.manager.publishConsents[0].Reason, ShouldNotBeEmpty)
			})

			Convey("Then the user's file is named, with the copy that keeps it", func() {
				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "not what this build would write")
				So(warnings, ShouldContainSubstring, "beadle bundles disable")
			})

			Convey("Then the backup holds the user's bytes, byte for byte", func() {
				// The one thing this may not lose: a file the farm did not
				// write. It is copied before the directory is taken, and the
				// report says where the copy is.
				So(read(t, filepath.Join(backupsFor(t, f.vault)[0], strings.TrimPrefix(own, f.vault.Root()))),
					ShouldEqual, mineBody)
				So(read(t, filepath.Join(backupsFor(t, f.vault)[0],
					"bundles", "claude", "plugins", "beadle-canon", ".mcp.json")),
					ShouldEqual, `{"mcpServers":{"mine":{"command":"mine"}}}`)
			})

			Convey("When it syncs again", func() {
				f.sync(t)
				report := f.sync(t)

				Convey("Then it is a no-op and says nothing", func() {
					So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "moved the plugin farm")
					So(f.manager.published, ShouldHaveLength, 1)
				})
			})
		})
	})
}
