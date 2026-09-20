package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd(Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SilenceUsage = true

	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFirstRunFlow(t *testing.T) {
	Convey("Given a home with two detected agents", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude rules\n")
		writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)
		writeFile(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# opencode rules\n")

		Convey("When init runs", func() {
			out, err := runCLI(t, "init")

			Convey("Then it enables the detected agents", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "[x] Claude Code")
				So(out, ShouldContainSubstring, "[x] OpenCode")
				So(out, ShouldContainSubstring, "[ ] Gemini CLI")
				So(out, ShouldContainSubstring, "beadle sync --dry-run")

				Convey("And a dry run writes nothing", func() {
					out, err := runCLI(t, "sync", "--dry-run")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "dry run: nothing was written")

					_, statErr := os.Stat(filepath.Join(home, ".beadle", "mcp", "servers.json"))
					So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

					Convey("And a real sync reports the rules conflict", func() {
						out, err := runCLI(t, "sync")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "1 open conflict(s)")

						out, err = runCLI(t, "conflicts")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "rules")
						So(out, ShouldContainSubstring, "opencode")

						out, err = runCLI(t, "resolve", "--all", "--take", "vault")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "resolved 1 conflict(s)")

						rules, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "AGENTS.md")) //nolint:gosec // G304: test reads its own temp file
						So(err, ShouldBeNil)
						So(string(rules), ShouldEqual, "# claude rules\n")

						out, err = runCLI(t, "status")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "conflicts: none")

						_, err = runCLI(t, "doctor")
						So(err, ShouldBeNil)

						out, err = runCLI(t, "agents", "mode", "opencode", "mcp", "pull")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "opencode mcp: pull")

						_, err = runCLI(t, "agents", "mode", "cursor", "rules", "sync")
						So(err, ShouldBeError)

						out, err = runCLI(t, "kinds", "disable", "skills")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "saved")

						out, err = runCLI(t, "history", "rules")
						So(err, ShouldBeNil)
						So(out, ShouldContainSubstring, "(current)")
					})
				})
			})
		})
	})
}

func TestResolveNeedsADecision(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When resolve is called without a decision", func() {
			_, err := runCLI(t, "resolve", "deadbeef")

			Convey("Then it asks for --take", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "--take")
			})
		})
	})
}

func TestReloadHintOutput(t *testing.T) {
	Convey("Given two agents with a shared MCP server", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# rules\n")
		writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)
		writeFile(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# rules\n")

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When the first sync pushes changes", func() {
			out, err := runCLI(t, "sync")

			Convey("Then reload hints are shown once", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "↻ new Claude Code sessions load MCP changes")
				So(out, ShouldContainSubstring, "↻ OpenCode reads its config at startup: restart OpenCode to load the changes")

				out, err = runCLI(t, "sync")
				So(err, ShouldBeNil)
				So(out, ShouldNotContainSubstring, "↻")
			})
		})
	})
}
