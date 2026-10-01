package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// The containment bug this pins: strings.HasPrefix(path, root) has no separator,
// so a root of /x/vw-a accepts /x/vw-ab. That is not a spelling quibble — it is
// another vault's directory, and two of the three places below delete or claim
// what they find there.
//
// Only two of the five sites the batch touched were actually broken. The table
// further down says which were not and why, because "we changed it" and "it
// changed" are different claims and only the second one needs a test.

// pruneEmptyDirs takes `removed` from state, so a stored path is not
// guaranteed to be inside the farm. One with ".." in it lands the cursor in the
// neighbour, and the climb used to accept it and remove the directory.
func TestPruneEmptyDirsStopsAtTheFarmAndNotAtANeighbour(t *testing.T) {
	Convey("Given a farm whose name is a prefix of its neighbour's", t, func() {
		base := t.TempDir()
		root := filepath.Join(base, "vw-a")
		neighbour := filepath.Join(base, "vw-ab")

		So(os.MkdirAll(filepath.Join(root, "skills"), 0o750), ShouldBeNil)
		So(os.MkdirAll(neighbour, 0o750), ShouldBeNil)

		// Empty, because os.Remove on a non-empty directory fails and the error
		// is dropped on purpose. An emptied neighbour is exactly the case.
		m := &bundleFarm{}

		Convey("When the stored removal climbs out into the neighbour", func() {
			m.pruneEmptyDirs(root, []string{filepath.Join("..", "vw-ab", "skills")})

			Convey("Then the neighbour's directory is still there", func() {
				// The old prefix test accepted /…/vw-ab under /…/vw-a, put it
				// in the removal set, and took the directory with it.
				So(fileExists(neighbour), ShouldBeTrue)
			})

			Convey("And the farm's own emptied directory is still pruned", func() {
				// Otherwise "stopped at the farm" would be satisfied by never
				// removing anything, and that is not what was asked for.
				empty := filepath.Join(root, "skills", "gone")
				So(os.MkdirAll(empty, 0o750), ShouldBeNil)

				m.pruneEmptyDirs(root, []string{filepath.Join("skills", "gone", "SKILL.md")})

				So(fileExists(empty), ShouldBeFalse)
			})
		})
	})
}

// vergerOwns walks from the key up to the surface root, asking the manager about
// each ancestor. With the bare prefix a key that climbed out was accepted as
// still-inside, so the walk continued into the neighbour and handed the predicate
// paths belonging to somebody else's vault.
func TestVergerOwnsStopsAtTheSurfaceAndNotAtANeighbour(t *testing.T) {
	Convey("Given a surface whose path is a prefix of a neighbour's vault", t, func() {
		base := t.TempDir()
		root := filepath.Join(base, "vw-a")

		var asked []string

		e := &Engine{owns: func(path string) (string, bool) {
			asked = append(asked, path)

			if strings.Contains(path, "vw-ab") {
				return "verger", true
			}

			return "", false
		}}

		v := &view{surface: stubSurface{path: root}}

		Convey("When the key climbs out into the neighbour", func() {
			owner, ok := e.vergerOwns(v, filepath.Join("..", "vw-ab", "skills", "gate"))

			Convey("Then nothing there is claimed as verger's", func() {
				// The old test accepted the candidate, kept walking, and came
				// back with the neighbour's owner.
				So(ok, ShouldBeFalse)
				So(owner, ShouldBeEmpty)
			})

			Convey("And the neighbour was never asked about above the surface", func() {
				for _, path := range asked {
					So(strings.Contains(path, "vw-ab"), ShouldBeFalse)
				}
			})
		})

		Convey("When the key is inside the surface", func() {
			owner, ok := e.vergerOwns(v, filepath.Join("skills", "gate"))

			Convey("Then the manager still answers for it", func() {
				So(ok, ShouldBeFalse)
				So(owner, ShouldBeEmpty)
			})
		})
	})
}

// The three sites the batch touched that were NOT broken: their old forms
// already appended a separator, so a neighbour was excluded before the change
// and is excluded after it. What they gained is filepath.Clean on both sides,
// which is why these rows say so explicitly — "we changed it" is not the claim
// "it changed", and only the second one is worth a test.
func TestTheSitesThatOnlyGainedCleaning(t *testing.T) {
	Convey("Given a neighbour that shares a prefix", t, func() {
		const root = "/x/root"

		Convey("Then underDir and projectKeyMatches exclude it, as they always did", func() {
			So(underDir("/x/root-other", root), ShouldBeFalse)
			So(projectKeyMatches("id/rel-other", "id", "rel"), ShouldBeFalse)
		})

		Convey("And isUnder excludes it too, on both sides of the change", func() {
			So(isUnder("/x/root-other", root), ShouldBeFalse)
		})
	})

	Convey("Given a root spelled with a trailing separator", t, func() {
		Convey("Then a path inside it matches, which the cleaned form buys", func() {
			// The one behaviour change here is Clean, not the separator: the
			// old form compared "…/root/a" against "/x/root//a" and said no.
			So(isUnder("/x/root/a", "/x/root/"), ShouldBeTrue)
			So(underDir("/x/root/a", "/x/root/"), ShouldBeTrue)
		})
	})

	Convey("Given the root itself", t, func() {
		Convey("Then it is inside, on every site that says so", func() {
			So(isUnder("/x/root", "/x/root"), ShouldBeTrue)
			So(underDir("/x/root", "/x/root"), ShouldBeTrue)
			So(projectKeyMatches("id/rel", "id", "rel"), ShouldBeTrue)
		})
	})
}

// stubSurface is the least a view needs for vergerOwns, which asks one question
// of it: where does the surface live.
type stubSurface struct{ path string }

func (s stubSurface) Kind() kind.ID { return kind.Skills }
func (s stubSurface) Path() string  { return s.path }
func (s stubSurface) WatchPaths() []string {
	return nil
}

func (s stubSurface) Read(context.Context) (agent.Snapshot, error) {
	return agent.Snapshot{}, nil
}
func (s stubSurface) Write(context.Context, kind.Items) error { return nil }
func (s stubSurface) Traits() agent.Traits                    { return agent.Traits{} }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
