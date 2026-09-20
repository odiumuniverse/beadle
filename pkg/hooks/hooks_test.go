package hooks_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
)

func TestHooksLoadSaveRoundTrip(t *testing.T) {
	Convey("Given a hooks file that does not exist yet", t, func() {
		path := filepath.Join(t.TempDir(), "hooks", "hooks.json")

		canon, err := hooks.Load(path)
		So(err, ShouldBeNil)
		So(canon, ShouldBeEmpty)

		canon["notify"] = hooks.Hook{Event: "notification", Matcher: "Bash", Command: "echo done", Timeout: 10}
		canon["lint"] = hooks.Hook{Event: "post-tool", Command: "make lint"}

		Convey("When hooks are saved and loaded back", func() {
			So(hooks.Save(path, canon), ShouldBeNil)

			loaded, err := hooks.Load(path)

			Convey("Then the canon round-trips", func() {
				So(err, ShouldBeNil)
				So(loaded, ShouldResemble, canon)
			})
		})
	})
}

func TestHooksValidate(t *testing.T) {
	Convey("Given hook validation", t, func() {
		Convey("When a valid hook is validated", func() {
			Convey("Then it passes", func() {
				So(hooks.Validate("lint-check", hooks.Hook{Event: "pre-tool", Command: "make lint", Timeout: hooks.MaxTimeout}), ShouldBeNil)
			})
		})

		Convey("When the name is invalid", func() {
			badNames := []string{"", "Lint", "lint_check", "lint check", "../lint"}

			for _, name := range badNames {
				Convey("With name "+name, func() {
					Convey("Then it is rejected", func() {
						So(hooks.Validate(name, hooks.Hook{Event: "stop", Command: "true"}), ShouldBeError)
					})
				})
			}
		})

		Convey("When the event, command or timeout is invalid", func() {
			Convey("Then each is rejected", func() {
				So(hooks.Validate("x", hooks.Hook{Event: "before-tool", Command: "true"}), ShouldBeError)
				So(hooks.Validate("x", hooks.Hook{Event: "stop"}), ShouldBeError)
				So(hooks.Validate("x", hooks.Hook{Event: "stop", Command: "  "}), ShouldBeError)
				So(hooks.Validate("x", hooks.Hook{Event: "stop", Command: "true", Timeout: -1}), ShouldBeError)
				So(hooks.Validate("x", hooks.Hook{Event: "stop", Command: "true", Timeout: hooks.MaxTimeout + 1}), ShouldBeError)
			})
		})

		Convey("When event and name helpers are used", func() {
			Convey("Then they classify correctly", func() {
				So(hooks.ValidEvent("session-start"), ShouldBeTrue)
				So(hooks.ValidEvent("session_end"), ShouldBeFalse)
				So(hooks.ValidName("a-b-1"), ShouldBeTrue)
			})
		})
	})
}

func TestHooksApproved(t *testing.T) {
	Convey("Given a config with approved hooks", t, func() {
		cfg := config.Default()
		cfg.ApproveHook("b")
		cfg.ApproveHook("a")
		cfg.ApproveHook("a")

		Convey("When approvals are read", func() {
			Convey("Then they are sorted and deduplicated", func() {
				So(cfg.ApprovedHooks, ShouldResemble, []string{"a", "b"})
				So(cfg.HookApproved("a"), ShouldBeTrue)
				So(cfg.HookApproved("c"), ShouldBeFalse)
				So(hooks.Approved(cfg), ShouldResemble, map[string]bool{"a": true, "b": true})
			})
		})

		Convey("When approvals are revoked", func() {
			cfg.RevokeHook("a")
			So(cfg.ApprovedHooks, ShouldResemble, []string{"b"})

			cfg.RevokeHook("b")
			So(cfg.ApprovedHooks, ShouldBeNil)
			So(hooks.Approved(nil), ShouldBeEmpty)

			Convey("Then an empty approval list is not persisted", func() {
				path := filepath.Join(t.TempDir(), config.FileName)
				So(cfg.Save(path), ShouldBeNil)

				data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(err, ShouldBeNil)
				So(string(data), ShouldNotContainSubstring, "approved_hooks")
			})
		})
	})
}
