package engine_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

// Two tools share one machine: the plugin manager delivers packages, beadle
// syncs configuration. What the manager delivered is not beadle's canon, and a
// sync that pulls it in makes beadle the owner of a file the other tool will
// overwrite on its next run.
//
// The library's receipt records the artifact it delivered — for a skill that is
// the skill's *directory* — while beadle's key is a file inside it. So this is
// about the shape of the question, not about a kind being missing a filter: the
// same directory answer has to hold for every kind whose files a package can
// bring.
func TestAPackageThePluginManagerDeliveredStaysOutOfTheCanon(t *testing.T) {
	Convey("Given a machine where the plugin manager delivered a package", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		// What the library records: the artifact it delivered, matched EXACTLY.
		// For a skill that is the skill's directory, while beadle's key is a
		// file inside it — which is the whole shape of this bug, so a fixture
		// that answered by prefix would hide it.
		manager := &exactManager{
			recordingManager: &recordingManager{home: filepath.Join(f.home, ".verger")},
			artifacts: []string{
				filepath.Join(f.home, ".claude", "skills", "fake"),
				filepath.Join(f.home, ".claude", "commands", "fake.md"),
			},
		}

		engine2 := f.withManager(t, manager)

		// The files the manager's install wrote, one per kind it can touch.
		write(t, filepath.Join(f.home, ".claude", "skills", "fake", "SKILL.md"),
			"---\nname: fake\n---\nfake body\n")
		write(t, filepath.Join(f.home, ".claude", "commands", "fake.md"), "do the thing\n")

		Convey("When beadle syncs", func() {
			report, err := engine2.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then nothing the manager delivered enters the canon", func() {
				So(vaultKeys(t, f.vault.SkillsDir()), ShouldBeEmpty)
				So(vaultKeys(t, f.vault.CommandsDir()), ShouldBeEmpty)
			})

			Convey("Then the sync says why it left them alone", func() {
				var said bool

				for _, kr := range report.Kinds {
					for _, warning := range kr.Warnings {
						if strings.Contains(warning, "owned by the plugin manager") {
							said = true
						}
					}
				}

				So(said, ShouldBeTrue)
			})

			Convey("Then the files are still on the host", func() {
				So(read(t, filepath.Join(f.home, ".claude", "skills", "fake", "SKILL.md")),
					ShouldEqual, "---\nname: fake\n---\nfake body\n")
			})
		})
	})
}

// The filter is about the plugin manager's files, not about the user's: an edit
// in beadle's own canon still syncs, or the fix would be a way to lose work.
func TestAnEditInBeadlesOwnCanonStillSyncs(t *testing.T) {
	Convey("Given a canon item the user edited and no plugin manager claims it", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		manager := &exactManager{
			recordingManager: &recordingManager{home: filepath.Join(f.home, ".verger")},
			artifacts:        []string{filepath.Join(f.home, ".claude", "skills", "foreign")},
		}

		engine2 := f.withManager(t, manager)

		write(t, f.vaultSkill("mine"), "# mine\n")
		write(t, filepath.Join(f.home, ".claude", "skills", "mine", "SKILL.md"), "# mine\n")

		f.sync(t)

		Convey("When the user edits it", func() {
			write(t, filepath.Join(f.home, ".claude", "skills", "mine", "SKILL.md"), "# mine, edited\n")

			Convey("Then the next sync takes the edit into the canon", func() {
				_, err := engine2.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(read(t, f.vaultSkill("mine")), ShouldEqual, "# mine, edited\n")
			})
		})
	})
}

// vaultKeys lists the canon item names one directory holds, so an assertion can
// say "this directory is empty" without naming a file that may not exist.
func vaultKeys(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// exactManager answers ownership the way the library does: the artifact paths a
// receipt recorded, matched exactly. The fixture's own manager answers by
// prefix, which is friendlier to a test and useless here — the question beadle
// has to get right is the shape of the path, not the prefix of it.
type exactManager struct {
	*recordingManager

	artifacts []string
}

func (m *exactManager) Owns(path string) (string, bool) {
	m.ownsCalls++

	if slices.Contains(m.artifacts, path) {
		return "verger", true
	}

	return "", false
}

// withManager builds a second engine over the same vault, wired to this
// fixture's plugin manager, so a test can change the manager the sync sees
// without the fixture's own one.
func (f *fixture) withManager(t *testing.T, manager *exactManager) *engine.Engine {
	t.Helper()

	e, err := engine.New(f.vault, f.config, agent.All(f.home, f.home),
		engine.WithHome(f.home), engine.WithPluginManager(manager), engine.WithVergerOwns(manager.Owns))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return e
}
