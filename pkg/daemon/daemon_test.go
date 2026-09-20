package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

func TestRenderLaunchd(t *testing.T) {
	Convey("Given a launchd spec", t, func() {
		spec := daemon.Spec{
			Binary:     "/usr/local/bin/beadle",
			Args:       []string{"watch"},
			Home:       "/Users/test",
			LogPath:    "/Users/test/Library/Logs/beadle.log",
			ErrLogPath: "/Users/test/Library/Logs/beadle.err.log",
		}

		Convey("When the plist is rendered", func() {
			path, content, err := daemon.RenderLaunchd(spec)

			Convey("Then it lands in LaunchAgents with the expected keys", func() {
				So(err, ShouldBeNil)
				So(path, ShouldEqual, filepath.Join("/Users/test", "Library/LaunchAgents", daemon.DefaultLabel+".plist"))
				So(content, ShouldContainSubstring, "<key>NumberOfFiles</key><integer>8192</integer>")
				So(content, ShouldContainSubstring, "<string>/usr/local/bin/beadle</string>")
				So(content, ShouldContainSubstring, "<string>watch</string>")
				So(content, ShouldContainSubstring, "<key>KeepAlive</key><true/>")
				So(content, ShouldContainSubstring, "<key>StandardOutPath</key><string>/Users/test/Library/Logs/beadle.log</string>")
			})
		})
	})
}

func TestRenderSystemd(t *testing.T) {
	Convey("Given a systemd spec", t, func() {
		spec := daemon.Spec{
			Binary: "/usr/local/bin/beadle",
			Args:   []string{"watch"},
			Home:   "/home/test",
		}

		Convey("When the unit is rendered", func() {
			path, content, err := daemon.RenderSystemd(spec)

			Convey("Then it lands in the user unit directory", func() {
				So(err, ShouldBeNil)
				So(path, ShouldEqual, filepath.Join("/home/test", ".config/systemd/user", "com-beadle-watch.service"))
				So(content, ShouldContainSubstring, "ExecStart=/usr/local/bin/beadle watch")
				So(content, ShouldContainSubstring, "Restart=on-failure")
				So(content, ShouldContainSubstring, "ProtectHome=no")
				So(content, ShouldContainSubstring, "WantedBy=default.target")
			})
		})
	})
}

func TestRenderRejectsRelativePaths(t *testing.T) {
	Convey("Given specs with relative paths", t, func() {
		Convey("When they are rendered", func() {
			Convey("Then they are rejected", func() {
				_, _, err := daemon.RenderSystemd(daemon.Spec{Binary: "beadle", Home: "/home/test"})
				So(err, ShouldBeError)

				_, _, err = daemon.RenderLaunchd(daemon.Spec{Binary: "/bin/beadle", Home: "relative"})
				So(err, ShouldBeError)
			})
		})
	})
}

func TestInstallWritesFileAndRegisters(t *testing.T) {
	Convey("Given a daemon spec and a stub runner", t, func() {
		home := t.TempDir()

		var calls [][]string

		run := func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))

			return nil
		}

		spec := daemon.Spec{Binary: "/bin/beadle", Args: []string{"watch"}, Home: home}

		Convey("When the daemon is installed", func() {
			path, err := daemon.Install(t.Context(), spec, run)

			Convey("Then the unit file exists and the runner was called", func() {
				So(err, ShouldBeNil)

				_, statErr := os.Stat(path)
				So(statErr, ShouldBeNil)
				So(calls, ShouldNotBeEmpty)
			})
		})
	})
}
