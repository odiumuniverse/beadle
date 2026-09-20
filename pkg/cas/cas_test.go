package cas_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
)

func TestHashOf(t *testing.T) {
	Convey("Given the content hash", t, func() {
		h := cas.HashOf([]byte("hello"))

		Convey("When the same and different content are hashed", func() {
			Convey("Then the hash is stable and parseable", func() {
				So(string(h), ShouldHaveLength, 64)
				So(cas.HashOf([]byte("hello")), ShouldEqual, h)
				So(cas.HashOf([]byte("world")), ShouldNotEqual, h)

				_, err := cas.ParseHash(string(h))
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestStorePutGet(t *testing.T) {
	Convey("Given a content-addressed store", t, func() {
		store := cas.NewStore(filepath.Join(t.TempDir(), "objects"))

		Convey("When content is put and fetched", func() {
			h, err := store.Put([]byte("content"))
			So(err, ShouldBeNil)

			got, err := store.Get(h)

			Convey("Then it round-trips", func() {
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, "content")
				So(store.Has(h), ShouldBeTrue)
			})
		})
	})
}

func TestStorePutIdempotent(t *testing.T) {
	Convey("Given a content-addressed store", t, func() {
		dir := t.TempDir()
		store := cas.NewStore(dir)

		h1, err := store.Put([]byte("same"))
		So(err, ShouldBeNil)

		Convey("When the same content is put again", func() {
			h2, err := store.Put([]byte("same"))

			Convey("Then it deduplicates to one object", func() {
				So(err, ShouldBeNil)
				So(h2, ShouldEqual, h1)

				count := 0

				walkErr := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}

					if !d.IsDir() {
						count++
					}

					return nil
				})

				So(walkErr, ShouldBeNil)
				So(count, ShouldEqual, 1)
			})
		})
	})
}

func TestStoreGetMissing(t *testing.T) {
	Convey("Given a store without the requested object", t, func() {
		store := cas.NewStore(t.TempDir())
		h := cas.HashOf([]byte("absent"))

		Convey("When it is fetched", func() {
			_, err := store.Get(h)

			Convey("Then a not-exist error is returned", func() {
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestStoreRejectsInvalidHash(t *testing.T) {
	Convey("Given a store and a table of invalid hashes", t, func() {
		store := cas.NewStore(t.TempDir())

		shortHash := cas.Hash(string(cas.HashOf([]byte("x")))[:63])

		for _, bad := range []cas.Hash{"", "zz", "../../etc/passwd", shortHash} {
			Convey("When fetching "+string(bad), func() {
				_, err := store.Get(bad)

				Convey("Then the hash is rejected", func() {
					So(errors.Is(err, cas.ErrInvalidHash), ShouldBeTrue)
					So(store.Has(bad), ShouldBeFalse)
				})
			})
		}

		Convey("When an empty hash is checked", func() {
			Convey("Then it is absent", func() {
				So(store.Has(cas.Hash("")), ShouldBeFalse)
			})
		})
	})
}
