package vault_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/vault"
)

func readIgnore(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}

	return string(data)
}

func TestResolveRoot(t *testing.T) {
	Convey("Given a table of flag/env sources for the vault root", t, func() {
		home, err := os.UserHomeDir()
		So(err, ShouldBeNil)

		tests := map[string]struct {
			flag string
			env  string
			want string
		}{
			"flag wins":             {flag: "/tmp/explicit", env: "/tmp/from-env", want: "/tmp/explicit"},
			"env wins over default": {env: "/tmp/from-env", want: "/tmp/from-env"},
			"default is home based": {want: filepath.Join(home, vault.DefaultDirName)},
		}

		for name, tt := range tests {
			Convey("When "+name, func() {
				got, err := vault.ResolveRoot(tt.flag, tt.env)

				Convey("Then the root matches", func() {
					So(err, ShouldBeNil)
					So(got, ShouldEqual, tt.want)
				})
			})
		}
	})
}

func TestInit(t *testing.T) {
	Convey("Given an uninitialized vault", t, func() {
		root := filepath.Join(t.TempDir(), "vault")
		v := vault.New(root)

		So(v.Initialized(), ShouldBeFalse)

		Convey("When it is initialized", func() {
			So(v.Init(), ShouldBeNil)

			Convey("Then the layout exists with owner-only mode", func() {
				So(v.Initialized(), ShouldBeTrue)

				for _, path := range []string{
					v.ConfigPath(), v.ObjectsDir(), v.SkillsDir(), v.MemoryDir(),
					v.ProjectsDir(), v.ConflictsDir(),
					filepath.Join(root, "rules"), filepath.Join(root, "mcp"), filepath.Join(root, "state"),
				} {
					info, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)
					So(info.IsDir() || !info.IsDir(), ShouldBeTrue)
				}

				for _, dir := range []string{v.ObjectsDir(), v.SkillsDir(), v.MemoryDir(), v.ProjectsDir(), v.ConflictsDir(), filepath.Join(root, "rules"), filepath.Join(root, "mcp"), filepath.Join(root, "state")} {
					info, statErr := os.Stat(dir)
					So(statErr, ShouldBeNil)
					So(info.IsDir(), ShouldBeTrue)
				}

				info, statErr := os.Stat(root)
				So(statErr, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o700))

				ignore, readErr := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(ignore), ShouldContainSubstring, "mcp/secrets.json")
				So(string(ignore), ShouldContainSubstring, "objects/")
				So(string(ignore), ShouldContainSubstring, "projects/")
				So(string(ignore), ShouldNotContainSubstring, "memory/")

				ignored, err := v.MemoryIgnored()
				So(err, ShouldBeNil)
				So(ignored, ShouldBeFalse)

				Convey("And a second init and gitignore pass are idempotent", func() {
					So(v.Init(), ShouldBeNil)
					So(v.EnsureGitIgnore(), ShouldBeNil)
					So(readIgnore(t, root), ShouldEqual, string(ignore))
				})
			})
		})
	})
}

func TestRemoveLegacyIgnore(t *testing.T) {
	Convey("Given a vault with a legacy gitignore that ignores memory", t, func() {
		root := filepath.Join(t.TempDir(), "vault")
		So(os.MkdirAll(root, 0o700), ShouldBeNil)

		legacy := "mcp/secrets.json\n# memory notes stay out of git until the U-12 secret gate lands\nmemory/\nplugins/\n"
		So(os.WriteFile(filepath.Join(root, ".gitignore"), []byte(legacy), 0o600), ShouldBeNil)

		v := vault.New(root)

		ignored, err := v.MemoryIgnored()
		So(err, ShouldBeNil)

		Convey("When the legacy ignore is removed twice", func() {
			So(v.RemoveLegacyIgnore(), ShouldBeNil)
			So(v.RemoveLegacyIgnore(), ShouldBeNil)

			updated := readIgnore(t, root)

			Convey("Then only the memory ignore entry is gone", func() {
				So(ignored, ShouldBeTrue)
				So(updated, ShouldNotContainSubstring, "memory/")
				So(updated, ShouldContainSubstring, "mcp/secrets.json")
				So(updated, ShouldContainSubstring, "plugins/")

				ignored, err = v.MemoryIgnored()
				So(err, ShouldBeNil)
				So(ignored, ShouldBeFalse)

				Convey("And the plain append-only pass does not bring memory back", func() {
					So(vault.New(root).EnsureGitIgnore(), ShouldBeNil)
					So(readIgnore(t, root), ShouldNotContainSubstring, "memory/")
				})
			})
		})
	})
}
