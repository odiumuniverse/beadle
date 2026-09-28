package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestOmpMCPWriteKeepsForeignKeys covers the unlocked path of the shared omp
// MCP lock: a normal sync still delivers the canon server into the file
// verger also writes, and a key beadle's canon does not declare survives.
func TestOmpMCPWriteKeepsForeignKeys(t *testing.T) {
	Convey("Given an omp home whose mcp.json holds a foreign server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := ompFixture(t)

		path := filepath.Join(f.home, ".omp", "agent", "mcp.json")
		write(t, path, `{"mcpServers":{"foreign":{"type":"stdio","command":"their-own"}}}`)

		write(t, f.vault.ServersPath(), `{"demo":{"transport":"stdio","command":["canon-mcp"]}}`)

		f.sync(t)

		Convey("Then the canon server lands and the foreign entry is untouched", func() {
			out := read(t, path)

			So(out, ShouldContainSubstring, "canon-mcp")
			So(out, ShouldContainSubstring, "their-own")

			// No lock warning on the ordinary path.
			report := f.sync(t)
			So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "held by another writer")
		})

		Convey("And the lock file lives next to the plugin state", func() {
			_, err := os.Stat(filepath.Join(f.home, ".omp", ".omp-plugin.verger.lock"))
			So(err, ShouldBeNil)
		})
	})
}
