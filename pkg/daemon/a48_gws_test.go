package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

func TestUnitEnvPinsIdentity(t *testing.T) {
	Convey("Given an invoking environment with XDG, DSH and PATH set", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		t.Setenv("DSH_HOME", "/dsh")
		t.Setenv("PATH", "/usr/bin:/bin")

		Convey("When the unit environment is built for a custom vault", func() {
			env := daemon.UnitEnv("/home/u", "/vault/custom")

			Convey("Then every identity key is pinned explicitly", func() {
				So(env["HOME"], ShouldEqual, "/home/u")
				So(env["BEADLE_HOME"], ShouldEqual, "/vault/custom")
				So(env["XDG_CONFIG_HOME"], ShouldEqual, "/xdg")
				So(env["DSH_HOME"], ShouldEqual, "/dsh")
				So(env["PATH"], ShouldEqual, "/usr/bin:/bin")
			})
		})

		Convey("When the vault is the default one", func() {
			env := daemon.UnitEnv("/home/u", filepath.Join("/home/u", ".beadle"))

			Convey("Then BEADLE_HOME stays implicit", func() {
				So(env, ShouldNotContainKey, "BEADLE_HOME")
				So(env["HOME"], ShouldEqual, "/home/u")
			})
		})

		Convey("When XDG and DSH are unset", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("DSH_HOME", "")

			env := daemon.UnitEnv("/home/u", "")

			Convey("Then the unit does not carry empty values", func() {
				So(env, ShouldNotContainKey, "XDG_CONFIG_HOME")
				So(env, ShouldNotContainKey, "DSH_HOME")
				So(env, ShouldNotContainKey, "BEADLE_HOME")
				So(env["HOME"], ShouldEqual, "/home/u")
			})
		})
	})
}

func TestRenderCarriesEnv(t *testing.T) {
	spec := daemon.Spec{
		Binary: "/usr/local/bin/beadle",
		Args:   []string{"watch", "--vault", "/vault"},
		Home:   "/Users/test",
		Env: map[string]string{
			"HOME":        "/Users/test",
			"BEADLE_HOME": "/vault",
			"PATH":        "/usr/bin:/bin",
		},
	}

	Convey("Given a launchd spec with a pinned environment", t, func() {
		Convey("When the plist is rendered", func() {
			_, content, err := daemon.RenderLaunchd(spec)

			Convey("Then it carries EnvironmentVariables in a deterministic order", func() {
				So(err, ShouldBeNil)
				So(content, ShouldContainSubstring, "<key>EnvironmentVariables</key>")
				So(content, ShouldContainSubstring, "<key>HOME</key><string>/Users/test</string>")
				So(content, ShouldContainSubstring, "<key>BEADLE_HOME</key><string>/vault</string>")

				So(strings.Index(content, "BEADLE_HOME"), ShouldBeLessThan, strings.Index(content, "HOME</key>"))
			})
		})
	})

	Convey("Given a systemd spec with a pinned environment", t, func() {
		Convey("When the unit is rendered", func() {
			_, content, err := daemon.RenderSystemd(spec)

			Convey("Then it carries quoted Environment assignments", func() {
				So(err, ShouldBeNil)
				So(content, ShouldContainSubstring, `Environment="HOME=/Users/test"`)
				So(content, ShouldContainSubstring, `Environment="BEADLE_HOME=/vault"`)
				So(content, ShouldContainSubstring, `ExecStart="/usr/local/bin/beadle" "watch" "--vault" "/vault"`)
			})
		})
	})
}

func TestTemporaryHome(t *testing.T) {
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}

	Convey("Given temporary and permanent paths", t, func() {
		Convey("Then the temporary roots are recognized", func() {
			So(daemon.TemporaryHome(filepath.Join(tmp, "home")), ShouldBeTrue)
			So(daemon.TemporaryHome("/tmp/home"), ShouldBeTrue)
			So(daemon.TemporaryHome("/private/var/folders/xx/home"), ShouldBeTrue)
			So(daemon.TemporaryHome("/var/folders/xx/home"), ShouldBeTrue)
		})

		Convey("Then permanent and empty paths are not", func() {
			So(daemon.TemporaryHome("/Users/test"), ShouldBeFalse)
			So(daemon.TemporaryHome("/home/test"), ShouldBeFalse)
			So(daemon.TemporaryHome(""), ShouldBeFalse)
		})
	})
}

func TestSystemdCommandQuotesPaths(t *testing.T) {
	Convey("Given a binary and a vault with spaces in their paths", t, func() {
		spec := daemon.Spec{
			Binary: "/opt/beadle app/bin/beadle",
			Args:   []string{"watch", "--vault", "/home/u/my vault"},
			Home:   "/home/u",
		}

		Convey("When the unit is rendered", func() {
			_, content, err := daemon.RenderSystemd(spec)

			Convey("Then every argv word is quoted, so systemd keeps it whole", func() {
				So(err, ShouldBeNil)
				So(content, ShouldContainSubstring,
					`ExecStart="/opt/beadle app/bin/beadle" "watch" "--vault" "/home/u/my vault"`)
			})
		})
	})
}

func TestRegisterSystemdRestartsOnReinstall(t *testing.T) {
	Convey("Given a fake systemctl runner", t, func() {
		spec := daemon.Spec{Binary: "/usr/local/bin/beadle", Args: []string{"watch"}, Home: "/home/u"}

		var calls []string

		run := func(_ context.Context, name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))

			return nil
		}

		Convey("When the unit is registered", func() {
			So(daemon.RegisterSystemdForTest(t.Context(), spec, run), ShouldBeNil)

			Convey("Then reload, enable and restart run in order", func() {
				// enable --now is a no-op on an active unit, so the restart is
				// what applies a rewritten environment.
				So(calls, ShouldResemble, []string{
					"systemctl --user daemon-reload",
					"systemctl --user enable --now com-beadle-watch.service",
					"systemctl --user restart com-beadle-watch.service",
				})
			})
		})
	})
}

func TestUnitEnvFromFile(t *testing.T) {
	spec := daemon.Spec{
		Binary: "/usr/local/bin/beadle",
		Args:   []string{"watch"},
		Home:   t.TempDir(),
		Env: map[string]string{
			"HOME":        "/Users/test",
			"BEADLE_HOME": "/vault",
			"PATH":        "/usr/bin:/bin",
		},
	}

	Convey("Given a rendered launchd unit", t, func() {
		path, content, err := daemon.RenderLaunchd(spec)
		So(err, ShouldBeNil)
		So(os.MkdirAll(filepath.Dir(path), 0o750), ShouldBeNil)
		So(os.WriteFile(path, []byte(content), 0o600), ShouldBeNil)

		Convey("Then the pinned environment round-trips", func() {
			env, err := daemon.UnitEnvFromFile(path)
			So(err, ShouldBeNil)
			So(env, ShouldResemble, spec.Env)
		})
	})

	Convey("Given a unit without pinned values", t, func() {
		path := filepath.Join(t.TempDir(), "unit")
		So(os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\n"), 0o600), ShouldBeNil)

		Convey("Then the environment is empty", func() {
			env, err := daemon.UnitEnvFromFile(path)
			So(err, ShouldBeNil)
			So(env, ShouldBeEmpty)
		})
	})
}
