package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func TestSkillTreesOptional(t *testing.T) {
	Convey("Given a state file without the skill tree section", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(`{"version":2}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			st, err := state.Load(path)

			Convey("Then it loads and the cache starts empty", func() {
				So(err, ShouldBeNil)

				_, ok := st.SkillTreeFor("/tmp/skill")
				So(ok, ShouldBeFalse)

				st.SetSkillTree("/tmp/skill", state.SkillTree{
					Digest:      cas.Hash("digest"),
					Fingerprint: cas.Hash("fingerprint"),
					Latest:      time.Unix(1, 0).UTC(),
					Stamp:       time.Unix(2, 0).UTC(),
				})

				entry, ok := st.SkillTreeFor("/tmp/skill")
				So(ok, ShouldBeTrue)
				So(entry.Digest, ShouldEqual, cas.Hash("digest"))
				So(entry.Fingerprint, ShouldEqual, cas.Hash("fingerprint"))

				Convey("And the entry round-trips through the file", func() {
					So(st.Save(path), ShouldBeNil)

					again, err := state.Load(path)
					So(err, ShouldBeNil)

					reloaded, ok := again.SkillTreeFor("/tmp/skill")
					So(ok, ShouldBeTrue)
					So(reloaded, ShouldResemble, entry)

					Convey("And an empty cache is omitted from the file", func() {
						So(again.DropSkillTrees(func(string) bool { return true }), ShouldEqual, 1)
						So(again.Save(path), ShouldBeNil)

						data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own state file
						So(err, ShouldBeNil)
						So(string(data), ShouldNotContainSubstring, "skill_trees")
					})
				})
			})
		})
	})
}
