package engine_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDoctorReadOnlyOnRealFilesystem(t *testing.T) {
	Convey("Given a synced fixture with a plugin skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		before := hashTree(t, f.home)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())

			Convey("Then it reports no symlink warning and leaves the tree unchanged", func() {
				So(err, ShouldBeNil)

				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "symlinks are not supported")
				}

				So(hashTree(t, f.home), ShouldEqual, before)
			})
		})
	})
}
