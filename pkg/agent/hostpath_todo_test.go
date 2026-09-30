package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/verger/pkg/hostpath"
)

func TestOmpMarkerUsesHostpath(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()

		Convey("When ompMarker is called", func() {
			got := agent.OmpMarker(home)
			want := filepath.Join(home, ".omp", "agent", "config.yml")

			Convey("Then it returns the hostpath-resolved path", func() {
				So(got, ShouldEqual, want)
			})
		})

		Convey("When the hostpath resolver is consulted directly", func() {
			env := hostpath.Env{Home: home, GOOS: "darwin", Lookup: os.LookupEnv}
			s, err := hostpath.Surfaces("omp", env)
			So(err, ShouldBeNil)

			Convey("Then ompMarker matches Markers.ConfigFile", func() {
				So(agent.OmpMarker(home), ShouldEqual, s.Markers.ConfigFile)
			})
		})
	})
}

func TestOmpMarkerRejectsLocalPath(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()

		Convey("When ompMarker is called", func() {
			got := agent.OmpMarker(home)

			Convey("Then it does NOT return a doubled agent/agent path", func() {
				So(got, ShouldNotContainSubstring, "agent/agent")
			})
		})
	})
}

func TestOpenCodeInboxUsesHostpath(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()

		Convey("When InboxPath is called for opencode", func() {
			got := agent.InboxPath(home, "opencode")
			want := filepath.Join(home, ".config", "opencode", "inbox.md")

			Convey("Then it returns the hostpath-resolved path", func() {
				So(got, ShouldEqual, want)
			})
		})

		Convey("When the hostpath resolver is consulted directly", func() {
			env := hostpath.Env{Home: home, GOOS: "darwin", Lookup: os.LookupEnv}
			s, err := hostpath.Surfaces("opencode", env)
			So(err, ShouldBeNil)

			Convey("Then InboxPath matches the resolver Inbox", func() {
				So(agent.InboxPath(home, "opencode"), ShouldEqual, s.Inbox)
			})
		})
	})
}

func TestOpenCodeInboxRejectsLocalPath(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()

		Convey("When InboxPath is called for opencode", func() {
			got := agent.InboxPath(home, "opencode")

			Convey("Then it does NOT return a path with a local resolver artifact", func() {
				So(got, ShouldNotContainSubstring, "openCodeInboxRoot")
			})
		})
	})
}
