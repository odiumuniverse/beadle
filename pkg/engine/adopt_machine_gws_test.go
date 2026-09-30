package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// adoptionFields reads the two path-shaped fields of one adoption record off
// the persisted document, not through state.Load: Load rewrites an absolute
// value into the portable form as it reads, so an assertion built on it cannot
// tell "stored a machine's home" from "stored the name of a place".
func adoptionFields(t *testing.T, f *fixture, host, name string) (provider, target string) {
	t.Helper()

	for _, record := range loadState(t, f).Adoptions {
		if record.Host == host && record.Name == name {
			return record.Provider, record.Target
		}
	}

	t.Fatalf("no adoption record for %s/%s", host, name)

	return "", ""
}

// carriesHomePath reports whether the persisted state names this machine's home
// anywhere — in any spelling.
//
// The canonicalisation is the point, and it was found the hard way: a count of
// the raw temp path is 1 on macOS and 0 on Linux for the same tree, because a
// temporary home on macOS is reached through /var, a symlink to /private/var.
// The document was not lying differently on the two systems — the search was
// asking two different questions. Both spellings are checked, so the assertion
// means "this machine's home is nowhere in the record" on either OS.
func carriesHomePath(t *testing.T, f *fixture, home string) bool {
	t.Helper()

	spelling := home
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		spelling = resolved
	}

	raw := string(stateJSON(t, f))

	return strings.Contains(raw, home) || strings.Contains(raw, spelling)
}

// TestUnadoptOnTheMachineThatArrivedWithTheVault is the adoption half of the
// vault that travels.
//
// Adopting moves somebody else's copy of a skill out of the way and records
// where it came from: the slot it occupied and, for a symlink, the copy it
// pointed at. Both were absolute, and both are facts about a filesystem rather
// than about the skill — so a record read on a second machine names two paths
// that exist on the first one. `unadopt` then does the wrong thing rather than
// the cautious thing: the stashed original is a symlink into the other machine's
// home, and moving it back "restores" it at a slot that is not this machine's.
//
// With the record naming the places instead of the paths, the same command on the
// second machine puts the original back in this machine's slot, pointing at this
// machine's copy.
func TestUnadoptOnTheMachineThatArrivedWithTheVault(t *testing.T) {
	Convey("Given a skill adopted on a machine whose home is /Users/a", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		homeA := filepath.Join(t.TempDir(), "Users", "a")
		a := newFixtureAt(t, homeA, filepath.Join(homeA, ".beagle"))
		a.emptyConfigs(t)

		write(t, a.vaultSkill("alpha"), "# alpha\n")

		srcA := foreignSkill(t, a, "alpha", "# alpha\n")
		So(srcA, ShouldStartWith, homeA)

		linkA := filepath.Join(homeA, ".claude", "skills", "alpha")
		So(linkA, ShouldStartWith, homeA)

		_, err := a.engine.Adopt(t.Context(), "alpha", agent.ClaudeCodeID, false)
		So(err, ShouldBeNil)

		providerA, targetA := adoptionFields(t, a, agent.ClaudeCodeID, "alpha")
		So(providerA, ShouldNotBeEmpty)
		So(targetA, ShouldNotBeEmpty)

		Convey("Then the record names the places, not the machine's paths", func() {
			So(providerA, ShouldStartWith, state.HomePrefix)
			So(targetA, ShouldStartWith, state.HomePrefix)
			// The claim is that the record carries none of this machine's home.
			// It is NOT "one occurrence is the home field": `adopt` does not go
			// through a sync commit, so the home stamp is never written here, and
			// an assertion built on that field was asserting a coincidence of the
			// command it happened to follow.
			So(carriesHomePath(t, a, homeA), ShouldBeFalse)
		})

		Convey("When the vault is opened on a machine whose home is /home/b", func() {
			b := arrivedMachine(t, homeA)

			// This machine has the same foreign tool installed, so the copy the
			// adoption was about exists here too — under this machine's home.
			srcB := filepath.Join(b.home, "skills-src", "alpha")
			write(t, filepath.Join(srcB, "SKILL.md"), "# alpha\n")

			linkB := filepath.Join(b.home, ".claude", "skills", "alpha")
			if err := os.MkdirAll(filepath.Dir(linkB), 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			Convey("Then unadopt puts the original back in this machine's slot", func() {
				report, err := b.engine.Unadopt(t.Context(), "alpha", agent.ClaudeCodeID, false)
				So(err, ShouldBeNil)
				So(report.Adoptions, ShouldHaveLength, 1)
				So(report.Adoptions[0].Action, ShouldEqual, "restored")
				So(report.Warnings, ShouldBeEmpty)

				info, statErr := os.Lstat(linkB)
				So(statErr, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)

				Convey("And it points at this machine's copy, not the other one's", func() {
					target, readErr := os.Readlink(linkB)
					So(readErr, ShouldBeNil)
					So(target, ShouldEqual, srcB)
				})

				Convey("And the record is gone", func() {
					_, ok := loadState(t, b).AdoptionFor(agent.ClaudeCodeID, "alpha")
					So(ok, ShouldBeFalse)
				})
			})

			Convey("And the first machine's home is nowhere in the state on disk", func() {
				// After the record is gone, nothing in the document may still name
				// the machine it was written on. `unadopt` does not sync either,
				// so the home field is absent rather than stale here.
				So(carriesHomePath(t, b, homeA), ShouldBeFalse)
			})
		})
	})
}

// TestUnadoptStillRefusesAnOriginalFromOutsideHome is the half that must not
// move. A copy that lives outside the home directory has no portable name — it
// is not somewhere every machine has — so the record keeps its absolute path and
// the second machine has no business pretending it can restore it.
func TestUnadoptStillRefusesAnOriginalFromOutsideHome(t *testing.T) {
	Convey("Given an adoption whose original copy lives outside the home directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		homeA := filepath.Join(t.TempDir(), "Users", "a")
		a := newFixtureAt(t, homeA, filepath.Join(homeA, ".beagle"))
		a.emptyConfigs(t)

		write(t, a.vaultSkill("beta"), "# beta\n")

		outside := filepath.Join(t.TempDir(), "shared", "beta")
		write(t, filepath.Join(outside, "SKILL.md"), "# beta\n")

		linkA := filepath.Join(homeA, ".claude", "skills", "beta")
		if err := os.MkdirAll(filepath.Dir(linkA), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := os.Symlink(outside, linkA); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		_, err := a.engine.Adopt(t.Context(), "beta", agent.ClaudeCodeID, false)
		So(err, ShouldBeNil)

		_, targetA := adoptionFields(t, a, agent.ClaudeCodeID, "beta")
		So(targetA, ShouldEqual, outside)

		Convey("When the vault is opened on a machine whose home is /home/b", func() {
			// The other machine's filesystem is not this test's filesystem, so
			// the shared copy is gone the way it would be on a real second
			// machine: nothing outside the home is there unless the other machine
			// put it in the vault. Left in place it would resolve, and the test
			// would be measuring a shared mount rather than the refusal.
			if err := os.RemoveAll(filepath.Dir(outside)); err != nil {
				t.Fatalf("remove the shared copy: %v", err)
			}

			b := arrivedMachine(t, homeA)

			Convey("Then unadopt says the original is gone and keeps the record", func() {
				// The stash travelled, and the stashed entry is a symlink into the
				// first machine's /tmp. Restoring it would put the user's own
				// skill back as a link to a directory that does not exist here.
				report, err := b.engine.Unadopt(t.Context(), "beta", agent.ClaudeCodeID, false)
				So(err, ShouldBeNil)
				So(report.Adoptions, ShouldHaveLength, 1)
				So(report.Adoptions[0].Action, ShouldEqual, "kept")

				// The refusal names the link and says why, and it keeps the path
				// absolute: a place outside the home has no portable spelling, and
				// the user needs to see it to go and look at it.
				note := strings.Join(report.Warnings, "\n")
				So(note, ShouldContainSubstring, "the original of beta is a symlink to ")
				So(note, ShouldContainSubstring, "which is not on this machine")
				So(note, ShouldContainSubstring, "the record stays for manual review")
				Convey("And nothing was written into this machine's slot", func() {
					_, statErr := os.Lstat(filepath.Join(b.home, ".claude", "skills", "beta"))
					So(os.IsNotExist(statErr), ShouldBeTrue)
				})

				Convey("And the record is still there for manual review", func() {
					_, ok := loadState(t, b).AdoptionFor(agent.ClaudeCodeID, "beta")
					So(ok, ShouldBeTrue)
				})
			})
		})
	})
}

// TestUnadoptOnTheSameMachineStillMovesTheStash is the ordinary case, kept
// because a portable record must not cost the same machine anything: the stashed
// entry is moved back, not re-created, so a link somebody edited in the stash is
// still theirs.
func TestUnadoptOnTheSameMachineStillMovesTheStash(t *testing.T) {
	Convey("Given a skill adopted and unadopted on one machine", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("gamma"), "# gamma\n")
		src := foreignSkill(t, f, "gamma", "# gamma\n")
		link := filepath.Join(f.home, ".claude", "skills", "gamma")
		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "gamma")

		_, err := f.engine.Adopt(t.Context(), "gamma", agent.ClaudeCodeID, false)
		So(err, ShouldBeNil)

		Convey("When unadopt runs", func() {
			_, err := f.engine.Unadopt(t.Context(), "gamma", agent.ClaudeCodeID, false)
			So(err, ShouldBeNil)

			Convey("Then the link is back and the stash is empty", func() {
				target, readErr := os.Readlink(link)
				So(readErr, ShouldBeNil)
				So(target, ShouldEqual, src)

				_, statErr := os.Lstat(stash)
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})

			Convey("And the copy behind it is the one that was moved aside", func() {
				So(read(t, filepath.Join(src, "SKILL.md")), ShouldEqual, "# gamma\n")
			})
		})
	})
}
