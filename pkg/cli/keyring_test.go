package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func newSecretsVault(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	return home
}

func readSecrets(t *testing.T, home string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(home, ".beadle", "mcp", "secrets.json")) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}

	return string(data)
}

func TestKeyringListShowsBackend(t *testing.T) {
	Convey("Given a vault with a stored secret", t, func() {
		home := newSecretsVault(t)

		setOut, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
		So(err, ShouldBeNil)
		So(setOut, ShouldContainSubstring, "stored TOKEN")

		Convey("When secrets are listed", func() {
			out, err := runCLI(t, "secrets", "list")

			Convey("Then the backend and the secret are shown", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "secrets: 1 (backend: file, mode: literal)")
				So(out, ShouldContainSubstring, "TOKEN")
				So(readSecrets(t, home), ShouldContainSubstring, "s3cr3t")
			})
		})
	})
}

func TestKeyringMigrateRefusesWithoutTool(t *testing.T) {
	Convey("Given a vault with a secret and no keyring tool", t, func() {
		home := newSecretsVault(t)

		_, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
		So(err, ShouldBeNil)

		before := readSecrets(t, home)

		t.Setenv("PATH", t.TempDir())

		Convey("When migrating to keyring", func() {
			_, err := runCLI(t, "secrets", "migrate", "keyring")

			Convey("Then it refuses before changing anything", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "keyring unavailable")
				So(readSecrets(t, home), ShouldEqual, before)
				So(readSecrets(t, home), ShouldNotContainSubstring, `"backend"`)
			})
		})
	})
}

func TestKeyringMigrateAlreadyKeyringProbes(t *testing.T) {
	Convey("Given a keyring-backed secrets file and no tool", t, func() {
		home := newSecretsVault(t)

		writeFile(t, filepath.Join(home, ".beadle", "mcp", "secrets.json"), `{"version": 2, "backend": "keyring", "secrets": {}}`)

		t.Setenv("PATH", t.TempDir())

		Convey("When migrating to keyring", func() {
			_, err := runCLI(t, "secrets", "migrate", "keyring")

			Convey("Then it still probes and refuses", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "keyring unavailable")
			})
		})
	})
}

func TestKeyringMigrateFileIsNoop(t *testing.T) {
	Convey("Given a file-backed vault with a secret", t, func() {
		home := newSecretsVault(t)

		_, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
		So(err, ShouldBeNil)

		Convey("When migrating to file or an unknown backend", func() {
			out, err := runCLI(t, "secrets", "migrate", "file")
			So(err, ShouldBeNil)

			_, err = runCLI(t, "secrets", "migrate", "gpg")

			Convey("Then file is a no-op and the unknown backend is refused", func() {
				So(out, ShouldContainSubstring, "backend is already file")
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unknown backend")
				So(readSecrets(t, home), ShouldContainSubstring, "s3cr3t")
			})
		})
	})
}
