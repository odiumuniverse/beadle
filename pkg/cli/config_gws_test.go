package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func configFilePath(home string) string {
	return filepath.Join(home, ".beadle", "config.json")
}

// gwsRunSplit runs the CLI with stdout and stderr captured separately, so a
// test can pin which stream a line belongs to.
func gwsRunSplit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	root := newRootCmd(Options{Version: "test"})

	var stdout, stderr bytes.Buffer

	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	root.SilenceUsage = true

	err := root.ExecuteContext(t.Context())

	return stdout.String(), stderr.String(), err
}

const legacyV2Config = `{
  "version": 2,
  "permissions": "off",
  "history": "git",
  "secrets": "literal",
  "agents": {"claude-code": {"enabled": true}}
}
`

func fileStamp(t *testing.T, path string) (string, time.Time) {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), info.ModTime()
}

func TestInitWritesExplicitConfig(t *testing.T) {
	Convey("Given a fresh HOME", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# r\n", "# r\n")

		Convey("When init runs", func() {
			_, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then config.json carries every scalar and the agents", func() {
				data, err := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(err, ShouldBeNil)

				text := string(data)
				So(text, ShouldContainSubstring, `"permissions": "sync"`)
				So(text, ShouldContainSubstring, `"history": "git"`)
				So(text, ShouldContainSubstring, `"secrets": "literal"`)
				So(text, ShouldContainSubstring, `"claude-code"`)
			})
		})
	})
}

func TestInitEnablesSharedByDefault(t *testing.T) {
	Convey("Given a fresh HOME", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# r\n", "# r\n")

		Convey("When init runs", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then the shared skills surface is enabled without opt-in", func() {
				So(out, ShouldContainSubstring, "[x] Shared skills")
				So(out, ShouldNotContainSubstring, "(opt-in:")

				data, readErr := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"shared": {`)
				So(string(data), ShouldContainSubstring, `"enabled": true`)
			})
		})
	})
}

func TestStatusMigrationStaysInMemory(t *testing.T) {
	Convey("Given an initialized vault with a v2 config", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# r\n", "# r\n")

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		So(os.WriteFile(configFilePath(home), []byte(legacyV2Config), 0o600), ShouldBeNil)

		before, beforeTime := fileStamp(t, configFilePath(home))

		Convey("When status runs", func() {
			out, err := gwsRun(t, "status")
			So(err, ShouldBeNil)

			Convey("Then the flips are rendered once and the file stays as loaded", func() {
				So(out, ShouldContainSubstring, "config: permissions are synchronized by default now")
				So(out, ShouldContainSubstring, "config: the shared skills surface")

				after, afterTime := fileStamp(t, configFilePath(home))
				So(after, ShouldEqual, before)
				So(afterTime.Equal(beforeTime), ShouldBeTrue)
			})

			Convey("And --log-json still renders them", func() {
				jsonOut, err := gwsRun(t, "--log-json", "status")
				So(err, ShouldBeNil)
				So(jsonOut, ShouldContainSubstring, "config: permissions are synchronized by default now")
				So(jsonOut, ShouldContainSubstring, "config: the shared skills surface")
			})

			Convey("And status --check reports each flip exactly once", func() {
				checkOut, err := gwsRun(t, "status", "--check")
				So(err, ShouldBeNil)
				So(strings.Count(checkOut, "config: permissions are synchronized by default now"), ShouldEqual, 1)
				So(strings.Count(checkOut, "config: the shared skills surface"), ShouldEqual, 1)
			})

			Convey("When a real sync runs", func() {
				syncOut, err := gwsRun(t, "sync")
				So(err, ShouldBeNil)
				So(syncOut, ShouldContainSubstring, "config: permissions are synchronized by default now")

				data, readErr := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"version": 3`)
			})
		})
	})
}

func TestInitRendersMigrationNotes(t *testing.T) {
	Convey("Given an initialized vault with a v2 config", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# r\n", "# r\n")

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		So(os.WriteFile(configFilePath(home), []byte(legacyV2Config), 0o600), ShouldBeNil)

		Convey("When init runs again", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then the flips are rendered and the file carries v3", func() {
				So(out, ShouldContainSubstring, "config: permissions are synchronized by default now")
				So(out, ShouldContainSubstring, "config: the shared skills surface")

				data, readErr := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"version": 3`)
			})
		})
	})
}

// TestMigrationNotesStayOnStderr pins the stream split: the one-time flips go
// to stderr, so a machine-readable stdout (--json) is never corrupted.
func TestMigrationNotesStayOnStderr(t *testing.T) {
	Convey("Given an initialized vault with a v2 config", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# r\n", "# r\n")

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		So(os.WriteFile(configFilePath(home), []byte(legacyV2Config), 0o600), ShouldBeNil)

		Convey("When a JSON command runs", func() {
			stdout, stderr, err := gwsRunSplit(t, "conflicts", "--json")
			So(err, ShouldBeNil)

			Convey("Then the flips are on stderr and stdout stays valid JSON", func() {
				So(stderr, ShouldContainSubstring, "config: permissions are synchronized by default now")
				So(stderr, ShouldContainSubstring, "config: the shared skills surface")
				So(stdout, ShouldNotContainSubstring, "config:")

				var parsed map[string]any
				So(json.Unmarshal([]byte(stdout), &parsed), ShouldBeNil)
			})
		})
	})
}

func TestStatusHealsMissingConfig(t *testing.T) {
	Convey("Given an initialized vault without config.json", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		So(os.Remove(configFilePath(home)), ShouldBeNil)

		Convey("When status runs", func() {
			logs, err := captureLogs(t, func() (string, error) { return gwsRun(t, "status") })
			So(err, ShouldBeNil)

			Convey("Then the config is recreated with defaults and an info line", func() {
				data, readErr := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"permissions": "sync"`)
				So(logs, ShouldContainSubstring, "config.json was missing; recreated with defaults")
			})
		})
	})
}

func TestSyncHealsMissingConfig(t *testing.T) {
	Convey("Given an initialized vault without config.json", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		So(os.Remove(configFilePath(home)), ShouldBeNil)

		Convey("When sync runs", func() {
			logs, err := captureLogs(t, func() (string, error) { return gwsRun(t, "sync") })
			So(err, ShouldBeNil)

			Convey("Then the command still works and recreates the config", func() {
				_, readErr := os.ReadFile(configFilePath(home)) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(logs, ShouldContainSubstring, "config.json was missing; recreated with defaults")
			})
		})
	})
}

func TestStatusKeepsConfigUntouched(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		before, beforeTime := fileStamp(t, configFilePath(home))

		Convey("When status runs", func() {
			_, err := gwsRun(t, "status")
			So(err, ShouldBeNil)

			after, afterTime := fileStamp(t, configFilePath(home))

			Convey("Then the config file is not rewritten", func() {
				So(after, ShouldEqual, before)
				So(afterTime.Equal(beforeTime), ShouldBeTrue)
			})
		})
	})
}

func TestCommandsDoNotHealForeignDirs(t *testing.T) {
	Convey("Given a directory that is not a vault", t, func() {
		home := gwsHome(t)
		foreign := filepath.Join(home, "not-a-vault")

		So(os.MkdirAll(foreign, 0o700), ShouldBeNil)
		So(os.WriteFile(filepath.Join(foreign, "keep.txt"), []byte("x\n"), 0o600), ShouldBeNil)

		Convey("When status and sync point at it", func() {
			out, err := gwsRun(t, "status", "--vault", foreign)
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "state: not initialized")

			_, err = gwsRun(t, "sync", "--vault", foreign)
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "not initialized")

			Convey("Then nothing is created there", func() {
				entries, readErr := os.ReadDir(foreign)
				So(readErr, ShouldBeNil)
				So(entries, ShouldHaveLength, 1)
				So(entries[0].Name(), ShouldEqual, "keep.txt")
			})
		})
	})
}

func TestStatusKeepsMissingVault(t *testing.T) {
	Convey("Given no vault at all", t, func() {
		home := gwsHome(t)

		Convey("When status runs", func() {
			out, err := gwsRun(t, "status")
			So(err, ShouldBeNil)

			Convey("Then it stays uninitialized and creates nothing", func() {
				So(out, ShouldContainSubstring, "state: not initialized")
				So(vaultDirExists(filepath.Join(home, ".beadle")), ShouldBeFalse)
			})
		})
	})
}
