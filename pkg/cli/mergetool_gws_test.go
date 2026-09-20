package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

type gwsMergetool struct {
	Mergetool engine.MergetoolResult `json:"mergetool"`
}

func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()

	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(t.Context(), "git", all...).CombinedOutput() //nolint:gosec // G204: fixed git subcommands in tests
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return string(out)
}

func TestMergetoolCLIValidation(t *testing.T) {
	Convey("Given mergetool flag combinations", t, func() {
		gwsHome(t)

		cases := []struct {
			name string
			args []string
			want string
		}{
			{name: "both flags", args: []string{"resolve", "deadbeef", "--mergetool", "--mergetool-abort"}, want: "either --mergetool or --mergetool-abort"},
			{name: "all", args: []string{"resolve", "--all", "--mergetool"}, want: "cannot be combined with --mergetool"},
			{name: "two ids", args: []string{"resolve", "aa", "bb", "--mergetool"}, want: "exactly one conflict id"},
			{name: "take", args: []string{"resolve", "aa", "--mergetool", "--take", "vault"}, want: "takes no resolution flags"},
			{name: "kind", args: []string{"resolve", "aa", "--mergetool", "--kind", "mcp"}, want: "takes no resolution flags"},
			{name: "no id", args: []string{"resolve", "--mergetool-abort"}, want: "exactly one conflict id"},
		}

		for _, tc := range cases {
			Convey("When "+tc.name+" is passed", func() {
				_, err := gwsRun(t, tc.args...)

				Convey("Then it is rejected", func() {
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, tc.want)
				})
			})
		}
	})
}

func TestMergetoolCLIJSON(t *testing.T) {
	Convey("Given a rules conflict in a git vault without a git identity", t, func() {
		home := gwsHome(t)

		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

		vault := filepath.Join(home, ".beadle")

		So(os.MkdirAll(vault, 0o700), ShouldBeNil)

		gitCLI(t, vault, "init", "-q")
		gitCLI(t, vault, "config", "user.useConfigOnly", "true")

		gwsRules(t, home, "# v1\n", "# v1\n")
		gwsInitSync(t)

		gwsWrite(t, filepath.Join(vault, "rules", "base.md"), "# vault v2\n")
		gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude v2\n")

		if _, err := gwsRun(t, "sync"); err != nil {
			t.Fatal(err)
		}

		views := gwsConflictViews(t, "conflicts", "--json")
		So(views.Conflicts, ShouldHaveLength, 1)

		c := views.Conflicts[0]

		Convey("When mergetool runs with --json", func() {
			out, err := gwsRun(t, "resolve", c.ID, "--mergetool", "--json")
			So(err, ShouldBeNil)
			So(gitCLI(t, vault, "log", "-1", "--format=%ae"), ShouldContainSubstring, "beadle@localhost")

			var payload gwsMergetool
			So(json.Unmarshal([]byte(out), &payload), ShouldBeNil)

			Convey("Then the payload describes the active state", func() {
				So(payload.Mergetool.ID, ShouldEqual, c.ID)
				So(payload.Mergetool.State, ShouldEqual, engine.MergetoolActive)
				So(payload.Mergetool.Path, ShouldEqual, filepath.Join(vault, "rules", "base.md"))
				So(payload.Mergetool.Refs, ShouldHaveLength, 3)

				Convey("And abort reports the aborted state", func() {
					out, err := gwsRun(t, "resolve", c.ID, "--mergetool-abort", "--json")
					So(err, ShouldBeNil)

					var aborted gwsMergetool
					So(json.Unmarshal([]byte(out), &aborted), ShouldBeNil)
					So(aborted.Mergetool.State, ShouldEqual, engine.MergetoolAborted)
					So(aborted.Mergetool.Refs, ShouldResemble, payload.Mergetool.Refs)

					Convey("And the human path works again", func() {
						out, err := gwsRun(t, "resolve", c.ID, "--mergetool")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "mergetool active:")

						_, err = gwsRun(t, "resolve", c.ID, "--mergetool-abort")
						So(err, ShouldBeNil)
					})
				})
			})
		})
	})
}
