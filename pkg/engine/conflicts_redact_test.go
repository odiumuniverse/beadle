package engine

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func newRedactionEngine(t *testing.T) *Engine {
	t.Helper()

	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatal(err)
	}

	store, err := secret.Load(v.SecretsPath())
	if err != nil {
		t.Fatal(err)
	}

	store.Set("API_TOKEN", "s3cr3t-value")

	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}

	cfg.Enable(agent.ClaudeCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatal(err)
	}

	e, err := New(v, cfg, agent.All(t.TempDir(), t.TempDir()), WithHome(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func TestConflictsRedaction(t *testing.T) {
	Convey("Given a vault with a stored secret", t, func() {
		e := newRedactionEngine(t)

		Convey("When a text carrying the value is redacted", func() {
			out := e.redact("env = s3cr3t-value\n")

			Convey("Then the value is replaced by a reference and the secret never leaks", func() {
				So(out, ShouldEqual, "env = ⟨secret:API_TOKEN⟩\n")
			})
		})

		Convey("When a text has no secret", func() {
			Convey("Then it is unchanged", func() {
				So(e.redact("command = \"npx\"\n"), ShouldEqual, "command = \"npx\"\n")
			})
		})
	})
}
