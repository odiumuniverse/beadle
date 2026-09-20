package cli

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestHooksCommands(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		out, err := runCLI(t, "hooks", "list")
		So(err, ShouldBeNil)
		So(out, ShouldContainSubstring, "no hooks")

		Convey("When a hook is added", func() {
			out, err := runCLI(t, "hooks", "add", "notify", "--event", "notification", "--command", "echo done", "--timeout", "5")

			Convey("Then it is pending and its command is hidden by default", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "hook notify saved")

				out, err := runCLI(t, "hooks", "list")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "notify")
				So(out, ShouldContainSubstring, "notification")
				So(out, ShouldContainSubstring, "pending")
				So(out, ShouldNotContainSubstring, "echo done")

				out, err = runCLI(t, "hooks", "list", "--commands")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "echo done")

				Convey("And approving an unknown hook fails", func() {
					_, err := runCLI(t, "hooks", "approve", "ghost")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "unknown hook")

					out, err = runCLI(t, "hooks", "approve", "notify")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "hook notify approved")

					out, err = runCLI(t, "hooks", "list")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "approved")

					_, err = runCLI(t, "hooks", "add", "bad", "--event", "nope", "--command", "true")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "unknown event")

					_, err = runCLI(t, "hooks", "add", "BadName", "--event", "stop", "--command", "true")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "invalid hook name")

					out, err = runCLI(t, "hooks", "revoke", "notify")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "hook notify revoked")

					out, err = runCLI(t, "hooks", "list")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "pending")

					out, err = runCLI(t, "hooks", "rm", "notify")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "hook notify removed")

					_, err = runCLI(t, "hooks", "rm", "notify")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "unknown hook")

					Convey("And re-adding a removed hook drops its approval", func() {
						out, err := runCLI(t, "hooks", "add", "notify", "--event", "notification", "--command", "echo again")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "hook notify saved")

						out, err = runCLI(t, "hooks", "list")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "pending")
					})
				})
			})
		})
	})
}
