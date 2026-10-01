package fsutil_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// The boundary is the whole point. A containment check written as a bare prefix
// matches a neighbour of the root whose name simply starts with the root's name,
// and the neighbour is the shape that actually exists on disk: /Users/bob and
// /Users/bobby, a vault root of /x/vw-a and the sibling fixture of /x/vw-ab.
//
// So the case that matters is not "a child is inside" — every prefix check gets
// that right — but "the sibling whose name shares a prefix is not".
func TestUnderCountsWholePathComponents(t *testing.T) {
	Convey("Given a root and paths around it", t, func() {
		root := filepath.Join("/x", "vw-a")

		Convey("Then the root itself is inside", func() {
			So(fsutil.Under(root, root), ShouldBeTrue)
		})

		Convey("And a child is", func() {
			So(fsutil.Under(root, filepath.Join(root, "state", "state.json")), ShouldBeTrue)
		})

		Convey("And a sibling sharing the prefix is not", func() {
			So(fsutil.Under(root, filepath.Join("/x", "vw-ab", "state.json")), ShouldBeFalse)
			So(fsutil.Under(root, filepath.Join("/x", "vw-abc", "deep", "file")), ShouldBeFalse)
		})

		Convey("And a name that continues the root's own last segment is not", func() {
			So(fsutil.Under(filepath.Join("/Users", "bob"), filepath.Join("/Users", "bobby", ".claude")), ShouldBeFalse)
		})

		Convey("And a path that climbs out is not", func() {
			So(fsutil.Under(root, "/x"), ShouldBeFalse)
			So(fsutil.Under(root, filepath.Join(root, "..", "vw-ab", "f")), ShouldBeFalse)
		})

		Convey("And the root spelled differently is still the root", func() {
			So(fsutil.Under(root+string(filepath.Separator), root+"/state.json"), ShouldBeTrue)
			So(fsutil.Under(filepath.Join(root, "."), root), ShouldBeTrue)
		})

		Convey("And a parent is not a child", func() {
			So(fsutil.Under(root, "/x"), ShouldBeFalse)
		})
	})
}
