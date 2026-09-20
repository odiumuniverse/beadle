package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func stubReplaceSymlink(t *testing.T, link func(path, target string) error) {
	t.Helper()

	previous := replaceSymlink
	replaceSymlink = link

	t.Cleanup(func() { replaceSymlink = previous })
}

func stubHostFSType(t *testing.T, fsType func(dir string) (string, error)) {
	t.Helper()

	previous := hostFSType
	hostFSType = fsType

	t.Cleanup(func() { hostFSType = previous })
}

func bareFarmEngine(t *testing.T) *Engine {
	t.Helper()

	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := os.MkdirAll(v.SkillsDir(), 0o700); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}

	if err := os.MkdirAll(v.PluginsDir(), 0o700); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	return &Engine{vault: v, config: config.Default()}
}

func farmPlanFor(skills ...string) farmPlan {
	desired := map[string]struct{}{}
	owner := map[string]string{}

	for _, name := range skills {
		desired[name] = struct{}{}
		owner[name] = "acme/tool"
	}

	return farmPlan{
		Parked:      map[string][]string{"acme/tool": skills},
		Quarantined: map[string]pluginLedgerRec{},
		Owner:       owner,
		Desired:     desired,
		Canon:       map[string]struct{}{},
	}
}

func issueMessages(t *testing.T, e *Engine) string {
	t.Helper()

	issues, err := e.Doctor(t.Context())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	messages := make([]string, 0, len(issues))

	for _, issue := range issues {
		messages = append(messages, issue.Message)
	}

	return strings.Join(messages, "\n")
}

//nolint:paralleltest // the test swaps a package-level seam
func TestFarmReportsUnsupportedSymlinks(t *testing.T) {
	Convey("Given a farm directory where symlinks are unsupported", t, func() {
		e := bareFarmEngine(t)

		failingDir := filepath.Join(t.TempDir(), "claude", "skills")
		otherDir := filepath.Join(t.TempDir(), "opencode", "skills")

		So(os.MkdirAll(failingDir, 0o700), ShouldBeNil)
		So(os.MkdirAll(otherDir, 0o700), ShouldBeNil)

		prunable := filepath.Join(e.vault.PluginsDir(), "acme", "tool", "current", "skills", "gone")
		So(os.Symlink(prunable, filepath.Join(failingDir, "gone")), ShouldBeNil)

		calls := map[string]int{}

		stubReplaceSymlink(t, func(path, target string) error {
			dir := filepath.Dir(path)
			calls[dir]++

			if dir == failingDir {
				return fmt.Errorf("create temp symlink: %w", fsutil.ErrSymlinksUnsupported)
			}

			return os.Symlink(target, path)
		})

		plan := farmPlanFor("alpha", "beta")

		results, warns := e.farmAgentSkills(agent.ClaudeCodeID, failingDir, plan)

		entries, err := os.ReadDir(failingDir)
		So(err, ShouldBeNil)

		link, linkErr := os.Readlink(filepath.Join(failingDir, "gone"))
		So(linkErr, ShouldBeNil)

		probes, err := filepath.Glob(filepath.Join(failingDir, ".beadle-probe*"))
		So(err, ShouldBeNil)

		otherResults, otherWarns := e.farmAgentSkills(agent.OpenCodeID, otherDir, plan)

		canon, err := os.ReadDir(e.vault.SkillsDir())
		So(err, ShouldBeNil)

		Convey("When the farm runs on both directories", func() {
			Convey("Then the unsupported dir is skipped once, prunes nothing and the other dir links", func() {
				So(warns, ShouldBeEmpty)
				So(results, ShouldResemble, []FarmResult{{
					Agent:  agent.ClaudeCodeID,
					Action: FarmSkipped,
					Note:   "symlinks are not supported in " + failingDir + "; a copy fallback is intentionally not performed",
				}})
				So(calls[failingDir], ShouldEqual, 1)

				So(entries, ShouldHaveLength, 1)
				So(entries[0].Name(), ShouldEqual, "gone")
				So(link, ShouldEqual, prunable)
				So(probes, ShouldBeEmpty)

				So(otherWarns, ShouldBeEmpty)
				So(otherResults, ShouldResemble, []FarmResult{{
					Agent:  agent.OpenCodeID,
					Plugin: "acme/tool",
					Action: FarmLinked,
					Count:  2,
				}})

				So(canon, ShouldBeEmpty)
			})
		})
	})
}

//nolint:paralleltest // the test swaps a package-level seam
func TestFarmOrdinaryErrorsStayWarnings(t *testing.T) {
	Convey("Given a farm directory with an ordinary link error", t, func() {
		e := bareFarmEngine(t)

		dir := filepath.Join(t.TempDir(), "claude", "skills")
		So(os.MkdirAll(dir, 0o700), ShouldBeNil)

		calls := 0

		stubReplaceSymlink(t, func(_, _ string) error {
			calls++

			return fmt.Errorf("create temp symlink: %w", fs.ErrPermission)
		})

		results, warns := e.farmAgentSkills(agent.ClaudeCodeID, dir, farmPlanFor("alpha", "beta"))

		entries, err := os.ReadDir(dir)
		So(err, ShouldBeNil)

		Convey("When the farm runs", func() {
			Convey("Then both failures are warnings and nothing is created", func() {
				So(results, ShouldBeEmpty)
				So(warns, ShouldHaveLength, 2)
				So(calls, ShouldEqual, 2)
				So(strings.Join(warns, " "), ShouldNotContainSubstring, "symlinks are not supported")
				So(warns[0], ShouldContainSubstring, "plugin farm: ")

				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestFarmQuietWithoutWork(t *testing.T) {
	Convey("Given a farm with no work", t, func() {
		e := bareFarmEngine(t)

		Convey("When it runs against a missing directory", func() {
			results, warns := e.farmAgentSkills(agent.ClaudeCodeID, filepath.Join(t.TempDir(), "missing", "skills"), farmPlanFor())

			Convey("Then it is quiet", func() {
				So(results, ShouldBeEmpty)
				So(warns, ShouldBeEmpty)
			})
		})
	})
}

//nolint:paralleltest // the test swaps a package-level seam
func TestDoctorReportsUnsupportedSymlinks(t *testing.T) {
	Convey("Given a vault with a plugin skill on a foreign filesystem", t, func() {
		home := t.TempDir()
		v := vault.New(filepath.Join(t.TempDir(), "vault"))
		So(v.Init(), ShouldBeNil)

		cfg, err := config.Load(v.ConfigPath())
		So(err, ShouldBeNil)

		cfg.Enable(agent.ClaudeCodeID)
		So(cfg.Save(v.ConfigPath()), ShouldBeNil)

		e, err := New(v, cfg, agent.All(home, t.TempDir()), WithHome(home))
		So(err, ShouldBeNil)

		cache := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
		skillDir := filepath.Join(cache, farmSkillsDir, "alpha")
		So(os.MkdirAll(skillDir, 0o700), ShouldBeNil)
		So(fsutil.WriteFileAtomic(filepath.Join(skillDir, farmSkillFile), []byte("# alpha\n"), 0o600), ShouldBeNil)

		claude := agent.ByID(e.Agents(), agent.ClaudeCodeID)
		So(claude, ShouldNotBeNil)

		skillsDir := claude.Surface(kind.Skills).Path()
		So(os.MkdirAll(skillsDir, 0o700), ShouldBeNil)

		So(os.MkdirAll(v.PluginsDir(), 0o700), ShouldBeNil)

		ledger := pluginLedger{Version: pluginLedgerVersion, Plugins: map[string]pluginLedgerRec{
			"acme/tool": {Version: "1.0.0", Target: cache},
		}}
		So(ledger.save(v.PluginsLedgerPath()), ShouldBeNil)

		stubHostFSType(t, func(string) (string, error) { return "exfat", nil })

		issues, err := e.Doctor(t.Context())
		So(err, ShouldBeNil)

		var warned []Issue

		for _, issue := range issues {
			if strings.Contains(issue.Message, "symlinks are not supported") {
				warned = append(warned, issue)
			}
		}

		Convey("When doctor runs on different filesystem types", func() {
			Convey("Then only exfat warns, and only while the skills dir exists", func() {
				So(warned, ShouldHaveLength, 1)
				So(warned[0].Severity, ShouldEqual, SeverityWarn)
				So(warned[0].Agent, ShouldEqual, agent.ClaudeCodeID)
				So(warned[0].Kind, ShouldEqual, kind.Skills)
				So(warned[0].Message, ShouldContainSubstring, "symlinks are not supported in ~/.claude/skills; 1 plugin skill(s) are not presented")

				stubHostFSType(t, func(string) (string, error) { return "smb", nil })
				So(issueMessages(t, e), ShouldNotContainSubstring, "symlinks are not supported")

				stubHostFSType(t, func(string) (string, error) { return "", fs.ErrPermission })
				So(issueMessages(t, e), ShouldNotContainSubstring, "symlinks are not supported")

				So(os.RemoveAll(skillsDir), ShouldBeNil)

				stubHostFSType(t, func(string) (string, error) { return "exfat", nil })
				So(issueMessages(t, e), ShouldNotContainSubstring, "symlinks are not supported")
			})
		})
	})
}
