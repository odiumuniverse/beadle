package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestReadWriteTree(t *testing.T) {
	Convey("Given a skill tree", t, func() {
		dir := t.TempDir()

		tree := skill.Tree{
			"SKILL.md":     []byte("# hello\n"),
			"scripts/x.sh": []byte("echo hi\n"),
		}

		Convey("When it is written and read back", func() {
			So(skill.WriteTree(dir, "my-skill", tree), ShouldBeNil)

			got, err := skill.ReadTree(filepath.Join(dir, "my-skill"))
			So(err, ShouldBeNil)

			skills, err := skill.ReadDir(dir)

			Convey("Then the tree round-trips through the directory", func() {
				So(err, ShouldBeNil)
				So(got, ShouldResemble, tree)
				So(skills, ShouldHaveLength, 1)
				So(skills["my-skill"], ShouldResemble, tree)
			})
		})
	})
}

func TestReadDirSkipsSymlinksAndBadNames(t *testing.T) {
	Convey("Given a skills directory with a good skill, a bad name and a symlink", t, func() {
		dir := t.TempDir()

		So(os.MkdirAll(filepath.Join(dir, "good-skill"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "good-skill", "SKILL.md"), []byte("x"), 0o600), ShouldBeNil)

		So(os.MkdirAll(filepath.Join(dir, "Bad Name"), 0o750), ShouldBeNil)

		target := filepath.Join(t.TempDir(), "linked")
		So(os.MkdirAll(target, 0o750), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(dir, "linked-skill")), ShouldBeNil)

		Convey("When the directory is read", func() {
			skills, err := skill.ReadDir(dir)

			Convey("Then only the good skill is returned", func() {
				So(err, ShouldBeNil)
				So(skills, ShouldHaveLength, 1)
				So(skills, ShouldContainKey, "good-skill")
			})
		})
	})
}

func TestWriteTreeRejectsTraversal(t *testing.T) {
	Convey("Given a tree with a traversal path or a bad skill name", t, func() {
		Convey("When it is written", func() {
			Convey("Then it is rejected", func() {
				So(skill.WriteTree(t.TempDir(), "my-skill", skill.Tree{"../escape.md": []byte("x")}), ShouldBeError)
				So(skill.WriteTree(t.TempDir(), "Bad Name", skill.Tree{"a.md": []byte("x")}), ShouldBeError)
			})
		})
	})
}

func TestManifest(t *testing.T) {
	Convey("Given a skill tree", t, func() {
		tree := skill.Tree{"SKILL.md": []byte("# x\n")}
		manifest := skill.ManifestOf(tree)

		Convey("When the manifest is marshalled and parsed", func() {
			data, err := manifest.Marshal()
			So(err, ShouldBeNil)

			parsed, err := skill.ParseManifest(data)

			Convey("Then it round-trips with a stable hash", func() {
				So(err, ShouldBeNil)
				So(parsed, ShouldResemble, manifest)
				So(parsed.Hash(), ShouldEqual, manifest.Hash())
				So(manifest.Hash(), ShouldNotBeEmpty)
			})
		})
	})
}

func TestTree3(t *testing.T) {
	Convey("Given a table of three-way skill tree merges", t, func() {
		tests := map[string]struct {
			base, vault, agent skill.Tree
			want               skill.Tree
			wantConflicts      []string
		}{
			"agent change": {
				base:  skill.Tree{"a": []byte("1")},
				vault: skill.Tree{"a": []byte("1")},
				agent: skill.Tree{"a": []byte("2")},
				want:  skill.Tree{"a": []byte("2")},
			},
			"vault change": {
				base:  skill.Tree{"a": []byte("1")},
				vault: skill.Tree{"a": []byte("2")},
				agent: skill.Tree{"a": []byte("1")},
				want:  skill.Tree{"a": []byte("2")},
			},
			"same change both sides": {
				base:  skill.Tree{"a": []byte("1")},
				vault: skill.Tree{"a": []byte("2")},
				agent: skill.Tree{"a": []byte("2")},
				want:  skill.Tree{"a": []byte("2")},
			},
			"different change": {
				base:          skill.Tree{"a": []byte("1")},
				vault:         skill.Tree{"a": []byte("2")},
				agent:         skill.Tree{"a": []byte("3")},
				want:          skill.Tree{"a": []byte("2")},
				wantConflicts: []string{"a"},
			},
			"agent deletes, vault unchanged": {
				base:  skill.Tree{"a": []byte("1")},
				vault: skill.Tree{"a": []byte("1")},
				agent: skill.Tree{},
				want:  skill.Tree{},
			},
			"agent deletes, vault modifies": {
				base:          skill.Tree{"a": []byte("1")},
				vault:         skill.Tree{"a": []byte("2")},
				agent:         skill.Tree{},
				want:          skill.Tree{"a": []byte("2")},
				wantConflicts: []string{"a"},
			},
			"new file from agent": {
				base:  skill.Tree{},
				vault: skill.Tree{},
				agent: skill.Tree{"b": []byte("1")},
				want:  skill.Tree{"b": []byte("1")},
			},
			"both add same file": {
				base:  skill.Tree{},
				vault: skill.Tree{"b": []byte("1")},
				agent: skill.Tree{"b": []byte("1")},
				want:  skill.Tree{"b": []byte("1")},
			},
			"both add different content": {
				base:          skill.Tree{},
				vault:         skill.Tree{"b": []byte("1")},
				agent:         skill.Tree{"b": []byte("2")},
				want:          skill.Tree{"b": []byte("1")},
				wantConflicts: []string{"b"},
			},
			"vault adds, agent never had it": {
				base:  skill.Tree{},
				vault: skill.Tree{"b": []byte("1")},
				agent: skill.Tree{},
				want:  skill.Tree{"b": []byte("1")},
			},
			"independent files": {
				base:  skill.Tree{"a": []byte("1"), "b": []byte("1")},
				vault: skill.Tree{"a": []byte("2"), "b": []byte("1")},
				agent: skill.Tree{"a": []byte("1"), "b": []byte("2")},
				want:  skill.Tree{"a": []byte("2"), "b": []byte("2")},
			},
		}

		for name, tt := range tests {
			Convey("When "+name, func() {
				result := skill.Tree3("s", tt.base, tt.vault, tt.agent)

				paths := make([]string, 0, len(result.Conflicts))
				for _, c := range result.Conflicts {
					paths = append(paths, c.Path)
				}

				Convey("Then the merged tree and conflict paths match", func() {
					So(result.Tree, ShouldResemble, tt.want)

					if tt.wantConflicts == nil {
						So(paths, ShouldBeEmpty)
					} else {
						So(paths, ShouldResemble, tt.wantConflicts)
					}
				})
			})
		}
	})
}
