package frontmatter_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

func TestFrontmatterSplit(t *testing.T) {
	Convey("Given markdown documents", t, func() {
		Convey("Then an empty frontmatter block is recognized", func() {
			front, body, ok := frontmatter.Split([]byte("---\n---\nbody\n"))
			So(ok, ShouldBeTrue)
			So(string(front), ShouldEqual, "")
			So(body, ShouldEqual, "body\n")
		})

		Convey("Then CRLF fences are recognized", func() {
			front, body, ok := frontmatter.Split([]byte("---\r\nkey: value\r\n---\r\nbody\r\n"))
			So(ok, ShouldBeTrue)
			So(string(front), ShouldEqual, "key: value")
			So(body, ShouldEqual, "body\r\n")
		})

		Convey("Then trailing spaces on the closing fence stay out of the body", func() {
			_, body, ok := frontmatter.Split([]byte("---\nkey: value\n---   \nbody\n"))
			So(ok, ShouldBeTrue)
			So(body, ShouldEqual, "body\n")
		})

		Convey("Then a missing closing fence is not a block", func() {
			_, body, ok := frontmatter.Split([]byte("---\nkey: value\nbody\n"))
			So(ok, ShouldBeFalse)
			So(body, ShouldEqual, "---\nkey: value\nbody\n")
		})

		Convey("Then a document without fences is all body", func() {
			front, body, ok := frontmatter.Split([]byte("plain text\n"))
			So(ok, ShouldBeFalse)
			So(front, ShouldBeNil)
			So(body, ShouldEqual, "plain text\n")
		})
	})
}

func TestFrontmatterPatchAndDrop(t *testing.T) {
	Convey("Given a document with comments and foreign keys", t, func() {
		existing := []byte("---\n# keep me\ndescription: old\nx-host: 1\n---\nbody\n")

		Convey("Then Patch replaces managed keys and keeps the rest", func() {
			out, err := frontmatter.Patch(existing, []string{"description"},
				[]frontmatter.Field{{Key: "description", Value: "new"}}, "next\n")
			So(err, ShouldBeNil)
			So(string(out), ShouldContainSubstring, "# keep me")
			So(string(out), ShouldContainSubstring, "x-host: 1")
			So(string(out), ShouldContainSubstring, "description: new")
			So(string(out), ShouldContainSubstring, "next")
			So(string(out), ShouldNotContainSubstring, "old")
		})

		Convey("Then Drop removes only the given keys", func() {
			out, err := frontmatter.Drop(existing, []string{"description"})
			So(err, ShouldBeNil)
			So(string(out), ShouldContainSubstring, "x-host: 1")
			So(string(out), ShouldNotContainSubstring, "description")
		})

		Convey("Then removing every key leaves the body alone", func() {
			out, err := frontmatter.Patch(existing, []string{"description", "x-host"}, nil, "body\n")
			So(err, ShouldBeNil)
			So(string(out), ShouldEqual, "body\n")
		})
	})
}

func TestFrontmatterMergeAndSlug(t *testing.T) {
	Convey("Given the merge helpers", t, func() {
		So(frontmatter.MergeScalar("vault", "projected", "projected"), ShouldEqual, "vault")
		So(frontmatter.MergeScalar("vault", "projected", "updated"), ShouldEqual, "updated")
		So(frontmatter.MergeScalar("vault", "projected", ""), ShouldEqual, "")
		So(frontmatter.MergeList([]string{"a"}, []string{"a"}, []string{"a"}), ShouldResemble, []string{"a"})
		So(frontmatter.MergeList([]string{"a"}, []string{"a"}, nil), ShouldBeNil)

		Convey("Then the slug rules hold", func() {
			So(frontmatter.ValidSlug("review-2"), ShouldBeTrue)
			So(frontmatter.ValidSlug("Review"), ShouldBeFalse)
			So(frontmatter.ValidSlug("-lead"), ShouldBeFalse)
			So(frontmatter.ValidSlug(""), ShouldBeFalse)
		})
	})
}
