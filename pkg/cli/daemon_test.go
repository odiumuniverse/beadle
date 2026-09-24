package cli

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func TestDaemonSpecResolvesVaultRoot(t *testing.T) {
	Convey("Given a vault reached through BEADLE_HOME", t, func() {
		home := t.TempDir()
		root := filepath.Join(home, "custom-vault")

		t.Setenv("HOME", home)
		t.Setenv("BEADLE_HOME", root)

		So(vault.New(root).Init(), ShouldBeNil)

		a := &app{}

		Convey("When the daemon spec is built", func() {
			spec, vaultRoot, err := a.daemonSpec()
			So(err, ShouldBeNil)
			So(vaultRoot, ShouldEqual, root)

			Convey("Then watch always carries the resolved vault root", func() {
				So(spec.Args, ShouldResemble, []string{"watch", "--vault", root})

				_, content, err := daemon.RenderLaunchd(spec)
				So(err, ShouldBeNil)
				So(content, ShouldContainSubstring, "<string>--vault</string>")
				So(content, ShouldContainSubstring, "<string>"+root+"</string>")
			})
		})

		Convey("When the vault flag overrides the environment", func() {
			flagRoot := filepath.Join(home, "flag-vault")

			So(vault.New(flagRoot).Init(), ShouldBeNil)

			spec, vaultRoot, err := (&app{vaultPath: flagRoot}).daemonSpec()
			So(err, ShouldBeNil)

			Convey("Then the flag wins", func() {
				So(spec.Args, ShouldResemble, []string{"watch", "--vault", flagRoot})
				So(vaultRoot, ShouldEqual, flagRoot)
			})
		})
	})
}
