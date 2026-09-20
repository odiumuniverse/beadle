package engine_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

const (
	realSecretOne = "sk-" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6"
	realSecretTwo = "tok_c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R" //nolint:gosec // G101: synthetic value injected into a temp copy only
	realSecretThr = "s3cret-d4E5f6G7h8I9j0K1l2M3n4"       //nolint:gosec // G101: synthetic value injected into a temp copy only
)

var fixtureSecretPattern = regexp.MustCompile(`(?i)(sk-|ctx7|glpat-|ghp_|xoxb-|AKIA|Bearer[[:space:]]+[A-Za-z0-9]{6,}|BEGIN[A-Z ]*PRIVATE KEY)`)

type realConfigFixture struct {
	home   string
	vault  *vault.Vault
	engine *engine.Engine
}

func realConfigRoot() string {
	return filepath.Join("testdata", "reale2e")
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()

	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}

			return os.Symlink(link, target) //nolint:gosec // G122: the path is built from the test's own fixture tree
		case entry.IsDir():
			return os.MkdirAll(target, 0o750)
		default:
			data, err := os.ReadFile(path) //nolint:gosec // G304: the test copies its own fixture
			if err != nil {
				return err
			}

			return os.WriteFile(target, data, 0o600) //nolint:gosec // G703: the target lives under the test's temp home
		}
	})
	if err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}

func injectRealValues(t *testing.T, home string) {
	t.Helper()

	replacements := map[string]string{
		"__BEADLE_SECRET_1__": realSecretOne,
		"__BEADLE_SECRET_2__": realSecretTwo,
		"__BEADLE_SECRET_3__": realSecretThr,
		"__BEADLE_HOME__":     home,
	}

	err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: the test injects into its own temp home
		if err != nil {
			return err
		}

		out := string(data)
		for from, to := range replacements {
			out = strings.ReplaceAll(out, from, to)
		}

		if out == string(data) {
			return nil
		}

		return os.WriteFile(path, []byte(out), 0o600) //nolint:gosec // G703: the path lives under the test's temp home
	})
	if err != nil {
		t.Fatalf("inject values: %v", err)
	}
}

func newRealConfigFixture(t *testing.T) (*realConfigFixture, *engine.Report) {
	t.Helper()

	home := t.TempDir()
	cwd := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	copyTree(t, filepath.Join(realConfigRoot(), "home"), home)
	injectRealValues(t, home)

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for _, id := range []string{agent.ClaudeCodeID, agent.OpenCodeID, agent.GeminiCLIID, agent.CursorID} {
		cfg.Enable(id)
	}

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	e, err := engine.New(v, cfg, agent.All(home, cwd), engine.WithHome(home))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	report, err := e.Sync(t.Context(), engine.SyncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(report.Errors()) != 0 {
		t.Fatalf("sync errors: %v", report.Errors())
	}

	return &realConfigFixture{home: home, vault: v, engine: e}, report
}

func canonHash(t *testing.T, v *vault.Vault) string {
	t.Helper()

	var out strings.Builder

	for _, path := range []string{v.RulesPath(), v.SkillsDir(), v.ServersPath(), v.SecretsPath(), filepath.Dir(v.PermissionsPath())} {
		out.WriteString(hashTree(t, path))
	}

	return out.String()
}

func assertNoLiteral(t *testing.T, root string, values ...string) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own vault
		if err != nil {
			return err
		}

		for _, value := range values {
			if strings.Contains(string(data), value) {
				t.Errorf("%s leaks a secret literal", path)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

//nolint:gocyclo,cyclop // one walker reports every fixture invariant in a single pass
func TestRealConfigFixtureIsSanitized(t *testing.T) {
	Convey("Given the on-disk real-config fixture", t, func() {
		root := realConfigRoot()

		Convey("When it is walked", func() {
			var problems []string

			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}

				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}

				if strings.Contains(strings.ToLower(entry.Name()), "sk-") {
					problems = append(problems, fmt.Sprintf("name of %s contains sk-", rel))
				}

				switch {
				case entry.Type()&fs.ModeSymlink != 0:
					target, err := os.Readlink(path)
					if err != nil {
						return err
					}

					if filepath.IsAbs(target) {
						problems = append(problems, fmt.Sprintf("symlink %s must be relative", rel))
					}

					if strings.Contains(target, "/Users/") || strings.Contains(target, "/home/") {
						problems = append(problems, fmt.Sprintf("symlink %s must not be absolute-home", rel))
					}

					if fixtureSecretPattern.MatchString(target) {
						problems = append(problems, "secret-like symlink target in "+rel)
					}

					cleaned := filepath.Clean(filepath.Join(filepath.Dir(rel), target))
					if strings.HasPrefix(cleaned, "..") {
						problems = append(problems, fmt.Sprintf("symlink %s escapes the fixture root", rel))
					}
				case entry.IsDir():
					return nil
				default:
					data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own fixtures
					if err != nil {
						return err
					}

					if fixtureSecretPattern.Match(data) {
						problems = append(problems, "secret-like value in "+rel)
					}

					if strings.Contains(string(data), "/Users/") || strings.Contains(string(data), "/home/") {
						problems = append(problems, "absolute home path in "+rel)
					}
				}

				return nil
			})

			Convey("Then every entry is relative, secret-free and contained", func() {
				So(err, ShouldBeNil)
				So(problems, ShouldBeEmpty)
			})
		})
	})
}

func TestRealConfigE2E(t *testing.T) {
	Convey("Given a sanitized real-config home synced into a vault", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, report := newRealConfigFixture(t)

		homeBefore := hashTree(t, f.home)
		canonBefore := canonHash(t, f.vault)

		second, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("When it is read and synced again", func() {
			Convey("Then skills, MCP, secrets and the alias behave and the second sync is a noop", func() {
				entries, err := os.ReadDir(f.vault.SkillsDir())
				So(err, ShouldBeNil)

				servers, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
				So(err, ShouldBeNil)

				files, err := os.ReadDir(f.vault.ConflictsDir())
				So(err, ShouldBeNil)

				So(report.Kind(kind.Skills).VaultChanged, ShouldBeTrue)
				So(entries, ShouldHaveLength, 45)

				for _, entry := range entries {
					So(entry.Name(), ShouldNotContainSubstring, "plug-")
				}

				pivot := filepath.Join(f.vault.PluginsDir(), "vmkteam", "vmkteam-developer", "current", "skills", "plug-1")
				So(farmLink(t, claudeSkillsDir(f.home), "plug-1"), ShouldEqual, pivot)

				So(servers, ShouldHaveLength, 7)
				So(report.Conflicts, ShouldHaveLength, 3)

				for _, conflict := range report.Conflicts {
					So(conflict.Reason, ShouldEqual, state.ReasonAdded)
				}

				So(files, ShouldHaveLength, 3)

				assertRealConfigConflict(t, report, kind.Rules, agent.GeminiCLIID, "main")
				assertRealConfigConflict(t, report, kind.MCP, agent.OpenCodeID, "codegraph")
				assertRealConfigConflict(t, report, kind.MCP, agent.GeminiCLIID, "context7")

				info, err := os.Stat(f.vault.SecretsPath())
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, fs.FileMode(0o600))

				secretRaw := read(t, f.vault.SecretsPath())
				for _, value := range []string{realSecretOne, realSecretTwo, realSecretThr} {
					So(secretRaw, ShouldContainSubstring, value)
				}

				for _, path := range []string{f.vault.RulesPath(), f.vault.SkillsDir(), f.vault.ServersPath(), f.vault.ObjectsDir()} {
					assertNoLiteral(t, path, realSecretOne, realSecretTwo, realSecretThr)
				}

				link := filepath.Join(f.home, ".config", "opencode", "AGENTS.md")
				target, err := os.Readlink(link)
				So(err, ShouldBeNil)

				So(target, ShouldEqual, "../../.claude/CLAUDE.md")
				So(report.Action(kind.Rules, agent.OpenCodeID), ShouldEqual, engine.ActionAlias)
				So(read(t, f.vault.RulesPath()), ShouldEqual, read(t, filepath.Join(f.home, ".claude", "CLAUDE.md")))
				So(read(t, f.vault.RulesPath()), ShouldNotContainSubstring, "gemini rules")
				So(read(t, filepath.Join(f.home, ".config", "opencode", "opencode.jsonc")), ShouldContainSubstring, `"enabled": false`)

				So(second.Errors(), ShouldBeEmpty)
				So(second.VaultChanged(), ShouldBeFalse)

				for _, kr := range second.Kinds {
					So(kr.Pulled, ShouldBeEmpty)

					for _, result := range kr.Agents {
						So(result.Action, ShouldNotEqual, engine.ActionPushed)
					}
				}

				So(hashTree(t, f.home), ShouldEqual, homeBefore)
				So(canonHash(t, f.vault), ShouldEqual, canonBefore)
			})
		})
	})
}

func assertRealConfigConflict(t *testing.T, report *engine.Report, k kind.ID, agentID, key string) {
	t.Helper()

	for _, conflict := range report.ConflictsOf(k) {
		if conflict.Agent == agentID {
			if conflict.Key != key {
				t.Fatalf("conflict key %q, want %q", conflict.Key, key)
			}

			return
		}
	}

	t.Fatalf("no conflict for %s/%s", k, agentID)
}
