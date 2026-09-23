package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/skills"
)

// seedRun runs one CLI command in this test's isolated home.
func seedRun(t *testing.T, args ...string) (string, error) {
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

// isolatedVault returns the vault path the isolated suite uses.
func isolatedVault(t *testing.T) string {
	t.Helper()

	if dir := os.Getenv("BEADLE_HOME"); dir != "" {
		return dir
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}

	return filepath.Join(home, ".beadle")
}

func TestSkillsSeedBothBuiltins(t *testing.T) {
	Convey("Given a fresh vault", t, func() {
		_, err := seedRun(t, "init")
		So(err, ShouldBeNil)

		Convey("When init has run", func() {
			Convey("Then both built-in skills are seeded", func() {
				for _, name := range []string{skills.ConflictsName, skills.BeadleName} {
					_, statErr := os.Stat(filepath.Join(isolatedVault(t), "skills", name, "SKILL.md"))
					So(statErr, ShouldBeNil)
				}

				out, err := seedRun(t, "skills", "seed")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "already in the vault")
			})
		})
	})
}

func TestSkillsReachHosts(t *testing.T) {
	Convey("Given a seeded vault and an enabled Claude Code", t, func() {
		_, err := seedRun(t, "init")
		So(err, ShouldBeNil)

		home, err := os.UserHomeDir()
		So(err, ShouldBeNil)

		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600), ShouldBeNil)

		_, err = seedRun(t, "agents", "enable", agent.ClaudeCodeID)
		So(err, ShouldBeNil)

		Convey("When a sync runs", func() {
			_, err := seedRun(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then both built-ins reach the host skills directory", func() {
				for _, name := range []string{skills.ConflictsName, skills.BeadleName} {
					_, statErr := os.Stat(filepath.Join(home, ".claude", "skills", name, "SKILL.md"))
					So(statErr, ShouldBeNil)
				}
			})
		})
	})
}
