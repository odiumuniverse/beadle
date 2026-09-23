package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestFlatName(t *testing.T) {
	Convey("Given file names", t, func() {
		cases := []struct {
			name string
			want string
			ok   bool
		}{
			{name: "alpha.md", want: "alpha", ok: true},
			{name: "review-2.md", want: "review-2", ok: true},
			{name: "SKILL.md"},
			{name: "Alpha.md"},
			{name: "alpha.txt"},
			{name: ".md"},
			{name: "alpha.md.bak"},
			{name: "a--b.md"},
			{name: "alpha"},
		}

		for _, tc := range cases {
			Convey("When "+tc.name+" is parsed", func() {
				got, ok := skill.FlatName(tc.name)

				So(ok, ShouldEqual, tc.ok)
				So(got, ShouldEqual, tc.want)
			})
		}
	})
}

func TestReadTreeFlat(t *testing.T) {
	Convey("Given a flat skill file", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "alpha.md")

		So(os.WriteFile(path, []byte("# alpha\n"), 0o600), ShouldBeNil)

		Convey("When the tree is read", func() {
			tree, err := skill.ReadTree(path)
			So(err, ShouldBeNil)

			Convey("Then it is the single-file tree", func() {
				So(tree, ShouldResemble, skill.Tree{"SKILL.md": []byte("# alpha\n")})
				So(skill.FlatPath(dir, "alpha"), ShouldEqual, path)
			})
		})
	})
}
