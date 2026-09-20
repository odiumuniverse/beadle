package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (f *fixture) enableAgent(t *testing.T, id string) {
	t.Helper()

	f.config.Enable(id)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func cursorHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers": {}}`)
}

func antigravityHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".gemini", "config", "mcp_config.json"), `{"mcpServers": {}}`)
}

func projectCanon(f *fixture, repo, rel string) string {
	return filepath.Join(f.vault.ProjectsDir(), repoID(repo), filepath.FromSlash(rel))
}

func TestProjectFourFilesRoundTrip(t *testing.T) {
	Convey("Given a repo enabling four project files across agents", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		cursorHome(t, f)
		antigravityHome(t, f)
		f.enableAgent(t, agent.CursorID)
		f.enableAgent(t, agent.AntigravityCLIID)

		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json", ".cursor/mcp.json", ".cursor/rules", ".agents/mcp_config.json")

		write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {"claude-srv": {"type": "http", "url": "https://claude.example.com"}}}`)
		write(t, filepath.Join(repo, ".cursor", "mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor.example.com"}}}`)
		write(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "# style\n")
		write(t, filepath.Join(repo, ".cursor", "rules", "notes.txt"), "foreign\n")
		write(t, filepath.Join(repo, ".agents", "mcp_config.json"), `{"mcpServers": {"agy-srv": {"serverUrl": "https://agy.example.com"}}}`)

		report := f.sync(t)

		canon := func(rel string) string { return read(t, projectCanon(f, repo, rel)) }

		Convey("When canon edits flow out and a canon deletion removes a file", func() {
			So(report.Kind(kind.Projects).VaultChanged, ShouldBeTrue)
			So(report.Errors(), ShouldBeEmpty)
			So(report.Kind(kind.Projects).Agents, ShouldHaveLength, 3)

			So(canon(".mcp.json"), ShouldContainSubstring, "claude-srv")
			So(canon(".cursor/mcp.json"), ShouldContainSubstring, "cursor-srv")
			So(canon(".cursor/rules/style.mdc"), ShouldEqual, "# style\n")
			So(canon(".agents/mcp_config.json"), ShouldContainSubstring, "agy-srv")

			So(read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")), ShouldEqual, "# style\n")
			So(read(t, filepath.Join(repo, ".cursor", "rules", "notes.txt")), ShouldEqual, "foreign\n")

			write(t, projectCanon(f, repo, ".cursor/rules/style.mdc"), "# style v2\n")

			f.sync(t)
			So(read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")), ShouldEqual, "# style v2\n")

			So(os.Remove(projectCanon(f, repo, ".cursor/rules/style.mdc")), ShouldBeNil)

			f.sync(t)

			_, styleErr := os.Stat(filepath.Join(repo, ".cursor", "rules", "style.mdc"))

			report = f.sync(t)

			So(errors.Is(styleErr, fs.ErrNotExist), ShouldBeTrue)
			So(read(t, filepath.Join(repo, ".cursor", "rules", "notes.txt")), ShouldEqual, "foreign\n")
			So(report.Action(kind.Projects, agent.CursorID), ShouldEqual, engine.ActionNoop)
			So(report.Action(kind.Projects, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
		})
	})
}

func TestProjectEnableCreatesSkeleton(t *testing.T) {
	Convey("Given a project whose file is missing", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		f.enableProject(t, ".mcp.json")

		Convey("When enabled and synced", func() {
			So(read(t, filepath.Join(repo, ".mcp.json")), ShouldEqualJSON, `{"mcpServers":{}}`)

			f.sync(t)
			So(read(t, projectCanon(f, repo, ".mcp.json")), ShouldEqualJSON, `{"mcpServers":{}}`)

			report := f.sync(t)
			So(report.Action(kind.Projects, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)

			status, err := f.engine.ProjectDisable(t.Context(), ".mcp.json")
			So(err, ShouldBeNil)

			for _, file := range status.Files {
				if file.Rel == ".mcp.json" {
					So(file.Enabled, ShouldBeFalse)
				}
			}

			Convey("Then a disabled surface is not synced and the file is kept", func() {
				report = f.sync(t)
				So(report.Kind(kind.Projects).Agents, ShouldBeEmpty)

				_, err := os.Stat(filepath.Join(repo, ".mcp.json"))
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestProjectLeakGateRendersEnvForm(t *testing.T) {
	Convey("Given a tracked project file with a secret ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		f.seedProjectCanon(t, repo, ".mcp.json",
			`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

		report := f.sync(t)

		data := read(t, filepath.Join(repo, ".mcp.json"))

		Convey("When the leak gate renders", func() {
			Convey("Then the tracked file gets the env form", func() {
				So(data, ShouldContainSubstring, "${AUTHORIZATION}")
				So(data, ShouldContainSubstring, "ctx7")
				So(data, ShouldNotContainSubstring, "Bearer abc123")
				So(strings.Join(report.Kind(kind.Projects).Warnings, " "), ShouldContainSubstring, "rendered as ${NAME}")
			})
		})
	})
}

func TestProjectIgnoredFileGetsValues(t *testing.T) {
	Convey("Given a gitignored project file with a secret ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")
		ignoreInRepo(t, repo, ".mcp.json")

		f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		f.seedProjectCanon(t, repo, ".mcp.json",
			`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

		report := f.sync(t)

		data := read(t, filepath.Join(repo, ".mcp.json"))

		Convey("When the file is ignored", func() {
			Convey("Then the literal value materializes", func() {
				So(strings.Join(report.Kind(kind.Projects).Warnings, " "), ShouldBeEmpty)
				So(data, ShouldContainSubstring, "Bearer abc123")
				So(data, ShouldNotContainSubstring, "${AUTHORIZATION}")
			})
		})
	})
}

func TestProjectWithoutClaudeCode(t *testing.T) {
	Convey("Given an OpenCode-only project setup", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		repo := newRepo(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "AGENTS.md"), "# global\n")

		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)

		Convey("When it syncs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			report = f.sync(t)

			Convey("Then it works without Claude Code and produces no errors", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# repo rules\n")
				So(strings.HasPrefix(read(t, repoFile(repo)), "<!-- beadle:memory:"), ShouldBeTrue)
				So(read(t, repoFile(repo)), ShouldContainSubstring, "hook")

				for _, issue := range issues {
					So(issue.Severity, ShouldNotEqual, engine.SeverityError)
				}

				So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Kind(kind.Memory).Agents, ShouldBeEmpty)
			})
		})
	})
}

func TestProjectLeakGateFollowsSymlinkedCwd(t *testing.T) {
	Convey("Given an ignored project file reached through a symlinked cwd", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		So(os.MkdirAll(filepath.Join(repo, "sub"), 0o750), ShouldBeNil)
		write(t, filepath.Join(repo, ".gitignore"), "/sub/.mcp.json\n")

		link := filepath.Join(t.TempDir(), "alias")
		So(os.Symlink(repo, link), ShouldBeNil)

		cwd := filepath.Join(link, "sub")

		f.useCwd(t, cwd)
		f.enableProject(t, ".mcp.json")

		f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".mcp.json"),
			`{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)
		So(os.Remove(filepath.Join(cwd, ".mcp.json")), ShouldBeNil)

		f.sync(t)

		data := read(t, filepath.Join(cwd, ".mcp.json"))

		Convey("When it syncs", func() {
			Convey("Then the ignored path materializes values", func() {
				So(data, ShouldContainSubstring, "Bearer abc123")
				So(data, ShouldNotContainSubstring, "${AUTHORIZATION}")
			})
		})
	})
}

func TestProjectAllowSecretsOverride(t *testing.T) {
	Convey("Given a project enabled with AllowSecrets", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, ".mcp.json")

		f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		f.seedProjectCanon(t, repo, ".mcp.json",
			`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

		f.sync(t)

		Convey("When it syncs", func() {
			Convey("Then a tracked file materializes values", func() {
				So(read(t, filepath.Join(repo, ".mcp.json")), ShouldContainSubstring, "Bearer abc123")
			})
		})
	})
}

func TestProjectSecretsExtractionAndRefs(t *testing.T) {
	Convey("Given a repo .mcp.json carrying a bearer literal", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		write(t, filepath.Join(repo, ".mcp.json"),
			`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"Bearer abc123"}}}}`)

		report := f.sync(t)

		canon := read(t, projectCanon(f, repo, ".mcp.json"))

		Convey("When the secret is extracted and pruned", func() {
			removed, err := f.engine.PruneSecrets(t.Context())
			So(err, ShouldBeNil)

			value, ok := f.engine.Secrets().Get("AUTHORIZATION")

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the ref is kept in use and resolves in doctor", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(canon, ShouldContainSubstring, "{secret:AUTHORIZATION}")
				So(canon, ShouldNotContainSubstring, "abc123")

				So(ok, ShouldBeTrue)
				So(value, ShouldEqual, "Bearer abc123")
				So(removed, ShouldBeEmpty)
				So(hasIssue(issues, engine.SeverityError, "has no value"), ShouldBeFalse)
			})
		})
	})
}

func TestProjectNoImplicitDelete(t *testing.T) {
	Convey("Given a tracked project file dropped by a checkout", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		gitIn(t, repo, "config", "user.email", "test@example.com")
		gitIn(t, repo, "config", "user.name", "Test")

		write(t, repoFile(repo), "# repo rules\n")
		gitIn(t, repo, "add", "AGENTS.md")
		gitIn(t, repo, "commit", "-q", "-m", "rules")

		f.sync(t)

		gitIn(t, repo, "checkout", "-q", "-b", "without")
		gitIn(t, repo, "rm", "-q", "AGENTS.md")
		gitIn(t, repo, "commit", "-q", "-m", "drop rules")

		report := f.sync(t)

		Convey("When the file is absent and returns then is forgotten", func() {
			_, repoErr := os.Stat(repoFile(repo))

			So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# repo rules\n")
			So(errors.Is(repoErr, fs.ErrNotExist), ShouldBeTrue)

			So(report.Kind(kind.Projects).Kept, ShouldNotBeEmpty)
			So(report.ConflictsOf(kind.Projects), ShouldBeEmpty)

			report = f.sync(t)

			So(errors.Is(repoErr, fs.ErrNotExist), ShouldBeTrue)
			So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)

			gitIn(t, repo, "checkout", "-q", "-")

			report = f.sync(t)
			So(read(t, repoFile(repo)), ShouldEqual, "# repo rules\n")
			So(report.ConflictsOf(kind.Projects), ShouldBeEmpty)

			_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
			So(err, ShouldBeNil)

			_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))
			_, fileErr := os.Stat(repoFile(repo))

			st, err := state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)

			policy, err := os.ReadFile(filepath.Join(f.vault.ProjectsDir(), repoID(repo), "policy.json"))
			So(err, ShouldBeNil)

			report = f.sync(t)

			Convey("Then forget clears canon, base and policy with no dangling sync", func() {
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)

				for _, a := range f.engine.Agents() {
					base, ok := st.Base(kind.Projects, a.ID)
					if !ok {
						continue
					}

					So(base, ShouldNotContainKey, repoID(repo)+"/AGENTS.md")
				}

				So(string(policy), ShouldNotContainSubstring, "AGENTS.md")
				So(report.Kind(kind.Projects).Agents, ShouldBeEmpty)
			})
		})
	})
}

func TestProjectForgetRemovesLocalEdits(t *testing.T) {
	Convey("Given a project file edited after the last sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "# rules\n")
		f.sync(t)

		write(t, repoFile(repo), "# edited after the last sync\n")

		_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
		So(err, ShouldBeNil)

		report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("When forgotten", func() {
			_, fileErr := os.Stat(repoFile(repo))
			_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))

			Convey("Then the file and canon are gone with no dangling conflict", func() {
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
				So(report.ConflictsOf(kind.Projects), ShouldBeEmpty)
				So(report.Kind(kind.Projects).Kept, ShouldBeEmpty)
			})
		})
	})
}

func TestProjectForgetRemovesFenceOnlyFile(t *testing.T) {
	Convey("Given a fence-only project file created by the digest", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
		f.sync(t)

		_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
		So(err, ShouldBeNil)

		report := f.sync(t)

		Convey("When forgotten", func() {
			_, fileErr := os.Stat(repoFile(repo))

			Convey("Then the fence-only file is removed and the policy entry is gone", func() {
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)
				So(report.Kind(kind.Projects).Agents, ShouldBeEmpty)
			})
		})
	})
}

func TestDoctorDigestAdviceRespectsPublishability(t *testing.T) {
	Convey("Given project notes and a git-tracked project file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		write(t, repoFile(repo), "# rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		Convey("When the file is not publishable", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the advice explains why sync will not write it", func() {
				So(hasIssue(issues, engine.SeverityInfo, "not publishable"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "will not write it"), ShouldBeTrue)
			})
		})

		Convey("When the file is gitignored", func() {
			ignoreInRepo(t, repo, "AGENTS.md")

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the advice is a plain sync", func() {
				So(hasIssue(issues, engine.SeverityInfo, "run beadle sync"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "not publishable"), ShouldBeFalse)
			})
		})
	})
}

func TestProjectHardeningRefusesLinks(t *testing.T) {
	Convey("Given a project file replaced by a symlink or hard link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		f.engine.Secrets().Set("API_TOKEN", "tok-123456")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		f.seedProjectCanon(t, repo, ".mcp.json", `{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:API_TOKEN}"}}}}`)

		target := filepath.Join(f.home, ".claude.json")
		before := read(t, target)

		So(os.Symlink(target, filepath.Join(repo, ".mcp.json")), ShouldBeNil)

		report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		info, err := os.Lstat(filepath.Join(repo, ".mcp.json"))
		So(err, ShouldBeNil)

		Convey("When a symlink then a hard link stands in place", func() {
			So(strings.Join(report.Errors(), " "), ShouldContainSubstring, "refusing to touch a symlink")
			So(read(t, target), ShouldEqual, before)
			So(info.Mode()&os.ModeSymlink, ShouldNotEqual, os.FileMode(0))

			So(os.Remove(filepath.Join(repo, ".mcp.json")), ShouldBeNil)

			other := filepath.Join(repo, "shared-config.json")
			write(t, other, `{"mcpServers": {}}`)
			So(os.Link(other, filepath.Join(repo, ".mcp.json")), ShouldBeNil)

			report, err = f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			So(strings.Join(report.Errors(), " "), ShouldContainSubstring, "refusing to touch a file with several hard links")
			So(read(t, other), ShouldEqualJSON, `{"mcpServers": {}}`)

			So(os.Remove(filepath.Join(repo, ".mcp.json")), ShouldBeNil)
			So(os.Remove(other), ShouldBeNil)

			f.sync(t)

			Convey("Then once the links are gone the write succeeds", func() {
				So(read(t, filepath.Join(repo, ".mcp.json")), ShouldContainSubstring, "ctx7")
			})
		})
	})
}

func TestProjectFailClosedOnGitErrors(t *testing.T) {
	Convey("Given a repo whose .git disappears", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		f.engine.Secrets().Set("API_TOKEN", "tok-123456")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		f.seedProjectCanon(t, repo, ".mcp.json", `{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:API_TOKEN}"}}}}`)

		f.sync(t)
		So(read(t, filepath.Join(repo, ".mcp.json")), ShouldContainSubstring, "${API_TOKEN}")

		So(os.RemoveAll(filepath.Join(repo, ".git")), ShouldBeNil)

		report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("When git check-ignore fails", func() {
			Convey("Then the warning is visible and the env form is kept", func() {
				So(strings.Join(report.Kind(kind.Projects).Warnings, " "), ShouldContainSubstring, "check-ignore")
				So(read(t, filepath.Join(repo, ".mcp.json")), ShouldContainSubstring, "${API_TOKEN}")
			})
		})
	})
}

func TestProjectMultiSurfaceCursor(t *testing.T) {
	Convey("Given a Cursor project with two surfaces", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		cursorHome(t, f)
		f.enableAgent(t, agent.CursorID)

		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".cursor/mcp.json", ".cursor/rules")

		write(t, filepath.Join(repo, ".cursor", "mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor.example.com"}}}`)
		write(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "# style\n")
		write(t, filepath.Join(repo, ".cursor", "rules", "notes.txt"), "foreign\n")

		report := f.sync(t)

		result, ok := report.Kind(kind.Projects).Agent(agent.CursorID)

		Convey("When both surfaces change", func() {
			So(report.Kind(kind.Projects).Agents, ShouldHaveLength, 1)
			So(ok, ShouldBeTrue)
			So(result.Action, ShouldEqual, engine.ActionNoop)
			So(report.Kind(kind.Projects).Pulled, ShouldHaveLength, 2)

			for _, change := range report.Kind(kind.Projects).Pulled {
				So(change.Agent, ShouldEqual, agent.CursorID)
			}

			So(read(t, projectCanon(f, repo, ".cursor/rules/style.mdc")), ShouldEqual, "# style\n")
			So(read(t, projectCanon(f, repo, ".cursor/mcp.json")), ShouldContainSubstring, "cursor-srv")

			write(t, projectCanon(f, repo, ".cursor/rules/style.mdc"), "# style v2\n")
			write(t, projectCanon(f, repo, ".cursor/mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor2.example.com"}}}`)

			report = f.sync(t)

			result, ok = report.Kind(kind.Projects).Agent(agent.CursorID)

			So(read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")), ShouldEqual, "# style v2\n")
			So(ok, ShouldBeTrue)
			So(result.Changes, ShouldHaveLength, 2)

			So(os.Remove(projectCanon(f, repo, ".cursor/rules/style.mdc")), ShouldBeNil)

			f.sync(t)

			_, styleErr := os.Stat(filepath.Join(repo, ".cursor", "rules", "style.mdc"))
			_, notesErr := os.Stat(filepath.Join(repo, ".cursor", "rules", "notes.txt"))

			Convey("Then both merge into one result and deletions apply", func() {
				So(errors.Is(styleErr, fs.ErrNotExist), ShouldBeTrue)
				So(notesErr, ShouldBeNil)
			})
		})
	})
}

func TestProjectDigestUsesNotesSlug(t *testing.T) {
	Convey("Given a project whose digest notes live under the path slug", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)

		fence := read(t, repoFile(repo))

		Convey("When the digest renders", func() {
			Convey("Then the canon uses the git identity and notes use the path slug", func() {
				So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)

				_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))
				_, noteErr := os.Stat(filepath.Join(f.vault.MemoryDir(), memory.Slug(repo), "MEMORY.md"))

				So(canonErr, ShouldBeNil)
				So(noteErr, ShouldBeNil)

				So(strings.HasPrefix(fence, "<!-- beadle:memory:"), ShouldBeTrue)
				So(fence, ShouldContainSubstring, "hook")
			})
		})
	})
}
