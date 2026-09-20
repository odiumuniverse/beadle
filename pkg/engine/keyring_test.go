package engine_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

type keyringFake struct {
	values  map[string]string
	sets    int
	failGet error
	failSet error
}

func (f *keyringFake) Get(account string) (string, bool, error) {
	if f.failGet != nil {
		return "", false, f.failGet
	}

	value, ok := f.values[account]

	return value, ok, nil
}

func (f *keyringFake) Set(account, value string) error {
	f.sets++

	if f.failSet != nil {
		return f.failSet
	}

	f.values[account] = value

	return nil
}

func (f *keyringFake) Delete(account string) (bool, error) {
	if _, ok := f.values[account]; !ok {
		return false, nil
	}

	delete(f.values, account)

	return true, nil
}

func secretsIndex(t *testing.T, path string, names ...string) {
	t.Helper()

	values := map[string]string{}

	for _, name := range names {
		values[name] = ""
	}

	encoded, err := json.Marshal(map[string]any{"version": 2, "backend": "keyring", "secrets": values})
	if err != nil {
		t.Fatalf("marshal secrets index: %v", err)
	}

	write(t, path, string(encoded)+"\n")
}

func newKeyringFixture(t *testing.T, keyring secret.Keyring, names ...string) *fixture {
	t.Helper()

	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	secretsIndex(t, v.SecretsPath(), names...)

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home), engine.WithKeyring(keyring))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return &fixture{home: home, vault: v, config: cfg, engine: e}
}

func TestKeyringModeExtractsAndResolves(t *testing.T) {
	Convey("Given a keyring-backed vault and a Claude MCP bearer", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{}}
		f := newKeyringFixture(t, keyring)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		report := f.sync(t)

		vaultRaw := read(t, f.vault.ServersPath())
		openCode := read(t, f.openCodeConfig())
		secretsRaw := read(t, f.vault.SecretsPath())

		Convey("When sync extracts and resolves", func() {
			Convey("Then the keyring holds the value, the canon holds the ref and the agent resolves it", func() {
				So(report.Warnings, ShouldBeEmpty)
				So(keyring.values["AUTHORIZATION"], ShouldEqual, "Bearer abc123")

				So(vaultRaw, ShouldContainSubstring, "{secret:AUTHORIZATION}")
				So(vaultRaw, ShouldNotContainSubstring, "abc123")

				So(openCode, ShouldContainSubstring, "Bearer abc123")
				So(openCode, ShouldNotContainSubstring, "{secret:")

				So(secretsRaw, ShouldContainSubstring, `"backend": "keyring"`)
				So(secretsRaw, ShouldNotContainSubstring, "abc123")
			})
		})
	})
}

func TestKeyringModeMissingValueSkips(t *testing.T) {
	Convey("Given a server whose keyring value is missing", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{}}
		f := newKeyringFixture(t, keyring, "AUTHORIZATION")

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)

		Convey("When sync and doctor run", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
			So(ok, ShouldBeTrue)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the write is skipped and the missing value surfaces in doctor", func() {
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, "AUTHORIZATION")
				So(read(t, f.openCodeConfig()), ShouldEqualJSON, `{"mcp": {}}`)

				So(hasIssue(issues, engine.SeverityError, "AUTHORIZATION has no value"), ShouldBeTrue)
			})
		})
	})
}

func TestKeyringModeDryRunAndDoctorAreReadOnly(t *testing.T) {
	Convey("Given a keyring-backed vault", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{}}
		f := newKeyringFixture(t, keyring)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		Convey("When dry-run, doctor and a real sync run", func() {
			_, err := f.engine.Sync(t.Context(), engine.SyncOptions{DryRun: true})
			So(err, ShouldBeNil)
			So(keyring.sets, ShouldEqual, 0)

			_, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(keyring.sets, ShouldEqual, 0)

			f.sync(t)

			Convey("Then only the real sync writes to the keyring", func() {
				So(keyring.sets, ShouldBeGreaterThan, 0)
			})
		})
	})
}

func TestKeyringModeSaveFailureFailsSync(t *testing.T) {
	Convey("Given a keyring whose write fails", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{}, failSet: errors.New("keyring write denied")}
		f := newKeyringFixture(t, keyring)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		before := read(t, f.vault.SecretsPath())

		Convey("When sync runs", func() {
			_, err := f.engine.Sync(t.Context(), engine.SyncOptions{})

			vaultRaw := read(t, f.vault.ServersPath())

			Convey("Then the sync fails and the canon never holds the plaintext", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "keyring")

				So(vaultRaw, ShouldContainSubstring, "{secret:AUTHORIZATION}")
				So(vaultRaw, ShouldNotContainSubstring, "abc123")
				So(read(t, f.vault.SecretsPath()), ShouldEqual, before)
			})
		})
	})
}

func TestKeyringModePrefetchFailureDegradesVisibly(t *testing.T) {
	Convey("Given a keyring whose prefetch fails", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{}, failGet: errors.New("keyring is locked")}
		f := newKeyringFixture(t, keyring, "AUTHORIZATION")

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)

		Convey("When sync and doctor run", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			warnings := 0

			for _, warning := range report.Warnings {
				if strings.Contains(warning, "keyring unavailable") {
					warnings++
				}
			}

			result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
			So(ok, ShouldBeTrue)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it degrades visibly with one warning and skipped per-ref checks", func() {
				So(warnings, ShouldEqual, 1)
				So(result.Action, ShouldEqual, engine.ActionSkipped)

				So(hasIssue(issues, engine.SeverityWarn, "keyring unavailable"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "has no value"), ShouldBeFalse)
			})
		})
	})
}

func TestKeyringModeMemoryNoteResolves(t *testing.T) {
	Convey("Given a memory note carrying a secret ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		keyring := &keyringFake{values: map[string]string{"TOKEN": "tok-12345"}}
		f := newKeyringFixture(t, keyring, "TOKEN")
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# token\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# token {secret:TOKEN}\n")
		f.sync(t)

		note := read(t, claudeMemory(f, "-Users-a", "MEMORY.md"))

		Convey("When the note is pushed", func() {
			Convey("Then the ref resolves and never reaches the agent", func() {
				So(note, ShouldContainSubstring, "tok-12345")
				So(note, ShouldNotContainSubstring, "{secret:")
			})
		})
	})
}
