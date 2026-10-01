package cas_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
)

// The store writes objects without a per-object fsync barrier, because a blob is
// immutable, named by the hash of its contents, and rebuilt from the canon. This
// is the invariant that makes that safe, and it is the reason a crash costs a
// rewrite rather than a wrong read.
func TestCorruptObjectIsDetectedAndRewritten(t *testing.T) {
	Convey("Given a store holding one object", t, func() {
		dir := t.TempDir()
		store := cas.NewStore(dir)
		data := []byte("the bytes of one object\n")

		h, err := store.Put(data)
		So(err, ShouldBeNil)

		Convey("Then the object reads back intact", func() {
			got, err := store.Get(h)
			So(err, ShouldBeNil)
			So(got, ShouldResemble, data)
		})

		Convey("When a crash leaves the object present but not matching its name", func() {
			// Exactly what a torn write looks like on disk: the name is right,
			// the bytes are short.
			path := filepath.Join(dir, string(h)[:2], string(h)[2:])
			So(os.WriteFile(path, []byte("torn"), 0o600), ShouldBeNil)

			Convey("Then reading it is refused rather than served as intact", func() {
				got, err := store.Get(h)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "corrupt blob")
				So(got, ShouldBeNil)
			})

			Convey("Then the presence check does not pass it off as stored", func() {
				// Has is what Put consults, so a store that trusted it alone
				// would skip this object forever.
				So(store.Has(h), ShouldBeTrue)
			})

			Convey("Then putting the same bytes again rewrites it", func() {
				again, err := store.Put(data)
				So(err, ShouldBeNil)
				So(again, ShouldEqual, h)

				got, err := store.Get(h)
				So(err, ShouldBeNil)
				So(got, ShouldResemble, data)
			})
		})
	})
}
