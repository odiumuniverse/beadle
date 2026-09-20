package engine_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func newGWSEngine(t *testing.T) (*engine.Engine, string) {
	t.Helper()

	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatal(err)
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
	if err != nil {
		t.Fatal(err)
	}

	return e, home
}

func TestConflictsJSONViews(t *testing.T) {
	Convey("Given a vault without conflicts", t, func() {
		e, _ := newGWSEngine(t)

		Convey("When the conflict views are listed", func() {
			views, err := e.ConflictViews()

			Convey("Then the list is empty", func() {
				So(err, ShouldBeNil)
				So(views, ShouldBeEmpty)
			})
		})
	})
}
