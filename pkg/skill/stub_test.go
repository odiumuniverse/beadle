package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestIsStubDir(t *testing.T) {
	Convey("Given a directory without a stub marker", t, func() {
		dir := t.TempDir()
		marker := filepath.Join(dir, skill.StubMarkerFile)

		_, ok := skill.IsStubDir(dir)
		So(ok, ShouldBeFalse)

		Convey("When a valid stub marker is written", func() {
			So(os.WriteFile(marker, []byte("v1 acme/tool 1.0.0 2026-09-16T10:30:00Z\n"), 0o600), ShouldBeNil)

			key, ok := skill.IsStubDir(dir)

			Convey("Then it is recognized", func() {
				So(ok, ShouldBeTrue)
				So(key, ShouldEqual, "acme/tool")
			})
		})

		Convey("When the marker is malformed", func() {
			for _, broken := range []string{
				"v2 acme/tool 1.0.0 2026-09-16T10:30:00Z\n",
				"v1 acme/tool 1.0.0 not-a-date\n",
				"v1 acme/tool\n",
				"",
			} {
				Convey("With marker "+broken, func() {
					So(os.WriteFile(marker, []byte(broken), 0o600), ShouldBeNil)

					_, ok := skill.IsStubDir(dir)

					Convey("Then it is not recognized", func() {
						So(ok, ShouldBeFalse)
					})
				})
			}
		})
	})
}
