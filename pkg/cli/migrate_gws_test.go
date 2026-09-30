package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
)

// The shape the external gate uses: init, migrate, hash the vault, migrate
// again, and require the hashes to match. The part that makes it worth having
// is the v3 fixture — a vault `beadle init` writes is already current, so a test
// that only ever ran against a fresh vault would pass a command that migrates
// nothing at all.
func TestMigrateUpgradesAV3VaultAndIsIdempotent(t *testing.T) {
	Convey("Given a vault whose config is still v3", t, func() {
		home := gwsHome(t)
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		// A v3 config, keyed by the ids beadle no longer uses. Written over
		// the vault init just made, so the only thing under test is the
		// migration.
		vaultDir := filepath.Join(home, ".beadle")
		So(os.WriteFile(filepath.Join(vaultDir, config.FileName), []byte(v3Config), 0o600), ShouldBeNil)

		Convey("When migrate runs", func() {
			out, migrateErr := runCLI(t, "migrate")
			So(migrateErr, ShouldBeNil)
			So(out, ShouldNotContainSubstring, "nothing to migrate")

			// The bytes on disk, not a re-loaded config: Load migrates in
			// memory, so reading the file back through it would report the
			// current version whether or not the command ever saved.
			Convey("Then the config on disk is the current version", func() {
				onDisk := readFile(t, filepath.Join(vaultDir, config.FileName))
				So(onDisk, ShouldContainSubstring, `"version": 4`)
			})

			Convey("And every renamed agent is keyed by its canonical id on disk", func() {
				onDisk := readFile(t, filepath.Join(vaultDir, config.FileName))

				So(onDisk, ShouldContainSubstring, `"claude"`)
				So(onDisk, ShouldContainSubstring, `"gemini"`)
				So(onDisk, ShouldContainSubstring, `"dsh"`)

				for _, old := range []string{"claude-code", "gemini-cli", "deepseek-harness"} {
					So(onDisk, ShouldNotContainSubstring, `"`+old+`"`)
				}
			})

			Convey("And a second run changes nothing at all", func() {
				before := snapshotTree(t, vaultDir)

				out, rerunErr := runCLI(t, "migrate")
				So(rerunErr, ShouldBeNil)
				So(out, ShouldContainSubstring, "nothing to migrate")

				So(snapshotTree(t, vaultDir), ShouldResemble, before)
			})
		})
	})
}

const v3Config = `{
  "version": 3,
  "history": "git",
  "secrets": "literal",
  "kinds": {"permissions": "sync", "skills": "sync"},
  "agents": {
    "claude-code": {"enabled": true, "modes": {"mcp": "sync"}, "plugin_pins": {"acme/tool": "1.0.0"}},
    "gemini-cli": {"enabled": true, "modes": {"skills": "off"}},
    "deepseek-harness": {"enabled": true},
    "opencode": {"enabled": true}
  }
}
`
