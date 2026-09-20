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
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/inbox"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
)

const (
	u12Token  = "ghp_abcdefghijklmnopqrstuvwxyz012345"
	u12Line   = "prefer short review loops"
	u12ApiKey = "abcdefgh12345678"
)

func inboxResults(report *engine.Report, k kind.ID, agentID string, action engine.InboxAction) []engine.InboxResult {
	kr := report.Kind(k)
	if kr == nil {
		return nil
	}

	var out []engine.InboxResult

	for _, result := range kr.Inbox {
		if result.Agent == agentID && result.Action == action {
			out = append(out, result)
		}
	}

	return out
}

func openCodeInbox(f *fixture) string {
	return agent.InboxPath(f.home, agent.OpenCodeID)
}

func writeInboxLines(t *testing.T, path string, lines ...string) {
	t.Helper()

	write(t, path, strings.Join(lines, "\n")+"\n")
}

func inboxHash(t *testing.T, text string) string {
	t.Helper()

	lines, err := inbox.Read(writeInboxTemp(t, text))
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}

	return lines[0].Hash
}

func writeInboxTemp(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "inbox.md")
	write(t, path, text+"\n")

	return path
}

func vaultMemoryNote(f *fixture, slug, name string) string {
	return filepath.Join(f.vault.MemoryDir(), slug, name)
}

func canonFiles(t *testing.T, f *fixture) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(f.vault.MemoryDir(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own vault
		if err != nil {
			return err
		}

		out.WriteString(path)
		out.Write(data)

		return nil
	})
	if err != nil {
		t.Fatalf("walk canon: %v", err)
	}

	return out.String()
}

func objectsText(t *testing.T, f *fixture) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(f.vault.ObjectsDir(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own vault
		if err != nil {
			return err
		}

		out.Write(data)

		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}

	return out.String()
}

func secretValue(t *testing.T, f *fixture, prefix string) string {
	t.Helper()

	for _, name := range f.engine.Secrets().Names() {
		if strings.HasPrefix(name, prefix) {
			value, ok := f.engine.Secrets().Get(name)
			if !ok {
				t.Fatalf("secret %s has no value", name)
			}

			return value
		}
	}

	t.Fatalf("no secret named %s*", prefix)

	return ""
}

func vaultLegacyMemoryLines() string {
	return "# memory notes stay out of git until the U-12 secret gate lands\nmemory/\n"
}

func noFileExists(t *testing.T, path string) {
	t.Helper()

	_, err := os.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s should not exist (err=%v)", path, err)
	}
}

func TestInboxFanIn(t *testing.T) {
	Convey("Given an OpenCode inbox with a consumable line", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		writeInboxLines(t, openCodeInbox(f), "<!-- header -->", "", u12Line)

		report := f.sync(t)

		hash := inboxHash(t, u12Line)
		note := vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md")

		Convey("When it syncs repeatedly", func() {
			So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated), ShouldNotBeEmpty)

			data := read(t, note)

			before := canonFiles(t, f)

			report = f.sync(t)

			Convey("Then the line becomes a note once and dedupe survives state loss", func() {
				So(data, ShouldContainSubstring, "name: \""+u12Line+"\"")
				So(data, ShouldContainSubstring, "origin: inbox")
				So(data, ShouldContainSubstring, "agent: opencode")
				So(data, ShouldContainSubstring, "hash: "+hash)
				So(data, ShouldContainSubstring, "<!-- transcribed: opencode -->")

				So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated), ShouldBeEmpty)
				So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxSkipped), ShouldNotBeEmpty)
				So(canonFiles(t, f), ShouldEqual, before)

				So(os.Remove(f.vault.StatePath()), ShouldBeNil)
				f.sync(t)
				So(canonFiles(t, f), ShouldEqual, before)
			})
		})
	})
}

func TestInboxDedupeAcrossSlugs(t *testing.T) {
	Convey("Given a line already consumed by another slug", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		hash := inboxHash(t, u12Line)
		write(t, vaultMemoryNote(f, "-Users-other", "inbox-"+hash+".md"), "elsewhere\n")
		writeInboxLines(t, openCodeInbox(f), u12Line)

		report := f.sync(t)

		Convey("When sync runs", func() {
			_, noteErr := os.Stat(vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md"))

			Convey("Then it is skipped and no note is written", func() {
				So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxSkipped), ShouldNotBeEmpty)
				So(errors.Is(noteErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestInboxOptOutAndReimport(t *testing.T) {
	Convey("Given a consumed inbox line", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		writeInboxLines(t, openCodeInbox(f), u12Line)
		f.sync(t)

		hash := inboxHash(t, u12Line)
		note := vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md")
		So(read(t, note), ShouldNotBeEmpty)

		Convey("When the note is removed and the line resurfaces", func() {
			So(os.Remove(note), ShouldBeNil)
			write(t, openCodeInbox(f), "other line\n")

			f.sync(t)
			noFileExists(t, note)

			writeInboxLines(t, openCodeInbox(f), u12Line)
			f.sync(t)

			Convey("Then it is not reimported until the line is written again", func() {
				_, err := os.Stat(note)
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestInboxDirectionsAndModes(t *testing.T) {
	Convey("Given a pending inbox line", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		writeInboxLines(t, openCodeInbox(f), u12Line)

		report := f.run(t, engine.SyncOptions{DryRun: true})

		Convey("When dry-run, push, pull and mode-off are exercised", func() {
			So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxWouldCreate), ShouldNotBeEmpty)

			_, dirErr := os.Stat(filepath.Join(f.vault.MemoryDir(), memory.Slug(repo)))
			So(errors.Is(dirErr, fs.ErrNotExist), ShouldBeTrue)

			report = f.run(t, engine.SyncOptions{Direction: config.ModePush})
			So(report.Kind(kind.Memory).Inbox, ShouldBeEmpty)

			report = f.run(t, engine.SyncOptions{Direction: config.ModePull})
			So(inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated), ShouldNotBeEmpty)

			second := "another line"
			writeInboxLines(t, openCodeInbox(f), second)

			f.config.SetMode(agent.OpenCodeID, kind.Memory, config.ModeOff)
			f.sync(t)

			Convey("Then only agent-to-vault directions consume and mode-off agents are skipped", func() {
				_, noteErr := os.Stat(vaultMemoryNote(f, memory.Slug(repo), "inbox-"+inboxHash(t, u12Line)+".md"))
				So(noteErr, ShouldBeNil)

				_, skippedErr := os.Stat(vaultMemoryNote(f, memory.Slug(repo), "inbox-"+inboxHash(t, second)+".md"))
				So(errors.Is(skippedErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestSecretGateDirectPull(t *testing.T) {
	Convey("Given an agent memory note carrying a token", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

		f.sync(t)

		canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))

		Convey("When the secret gate runs", func() {
			Convey("Then the literal is extracted and never stored raw", func() {
				So(canon, ShouldNotContainSubstring, u12Token)
				So(canon, ShouldContainSubstring, "{secret:")
				So(canonFiles(t, f), ShouldNotContainSubstring, u12Token)
				So(objectsText(t, f), ShouldNotContainSubstring, u12Token)
				So(read(t, f.vault.StatePath()), ShouldNotContainSubstring, u12Token)

				So(f.engine.Secrets().Len(), ShouldEqual, 1)
				So(secretValue(t, f, "SECRET"), ShouldEqual, u12Token)
				So(read(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md")), ShouldEqual, "api_key: "+u12Token+"\n")
			})
		})
	})
}

func TestSecretGateLaterKeyPair(t *testing.T) {
	Convey("Given a JSON note whose later key pair holds a secret", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), `{"token":"","password":"hunter2secret"}`+"\n")

		f.sync(t)

		canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))

		Convey("When the gate runs", func() {
			Convey("Then the later pair is extracted", func() {
				So(canon, ShouldNotContainSubstring, "hunter2secret")
				So(canon, ShouldContainSubstring, "{secret:PASSWORD}")
				So(objectsText(t, f), ShouldNotContainSubstring, "hunter2secret")
				So(read(t, f.vault.StatePath()), ShouldNotContainSubstring, "hunter2secret")
			})
		})
	})
}

func TestSecretGateMigrationAndIdempotency(t *testing.T) {
	Convey("Given a canon note with a plaintext token", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "notes\ntoken: "+u12Token+"\n")

		f.sync(t)

		canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))
		before := canonFiles(t, f)

		second := f.sync(t)

		Convey("When the canon is migrated", func() {
			Convey("Then the ref replaces the literal and a repeated sync is a noop", func() {
				So(canon, ShouldNotContainSubstring, u12Token)
				So(canon, ShouldContainSubstring, "{secret:SECRET}")
				So(objectsText(t, f), ShouldNotContainSubstring, u12Token)
				So(read(t, f.vault.StatePath()), ShouldNotContainSubstring, u12Token)

				So(second.Kind(kind.Memory).VaultChanged, ShouldBeFalse)
				So(canonFiles(t, f), ShouldEqual, before)
			})
		})
	})
}

func TestSecretGateConflictBlobs(t *testing.T) {
	Convey("Given a memory conflict over a secret-bearing note", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12ApiKey+"\n")
		f.sync(t)

		write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12ApiKey+"\nagent edit\n")
		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:API_KEY}\nvault edit\n")

		report := f.sync(t)

		Convey("When the conflict is recorded", func() {
			Convey("Then the blobs hold refs only", func() {
				So(report.ConflictsOf(kind.Memory), ShouldHaveLength, 1)
				So(objectsText(t, f), ShouldNotContainSubstring, u12ApiKey)
			})
		})
	})
}

func TestOutboundMissingSecretSkipsNote(t *testing.T) {
	Convey("Given a canon note with a missing secret ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:GONE_12345}\n")
		write(t, filepath.Join(claudeSlug(f, memory.Slug(repo)), "memory", "other.md"), "x\n")

		report := f.sync(t)

		result, ok := report.Kind(kind.Memory).Agent(agent.ClaudeCodeID)

		Convey("When the note is pushed", func() {
			_, noteErr := os.Stat(claudeMemory(f, memory.Slug(repo), "MEMORY.md"))

			Convey("Then the write is skipped naming the missing secret", func() {
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, "missing secrets: GONE_12345")
				So(errors.Is(noteErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestOutboundEnvRefNotExpanded(t *testing.T) {
	Convey("Given a canon note with an env ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, vaultMemoryNote(f, memory.Slug(repo), "ENV.md"), "value: {env:MY_TOKEN}\n")
		write(t, claudeMemory(f, memory.Slug(repo), "seed.md"), "seed\n")

		f.sync(t)

		Convey("When it is pushed", func() {
			Convey("Then the env ref is not expanded", func() {
				So(read(t, claudeMemory(f, memory.Slug(repo), "ENV.md")), ShouldContainSubstring, "{env:MY_TOKEN}")
			})
		})
	})
}

func TestDigestRedactsRefs(t *testing.T) {
	Convey("Given a memory note referenced by a digest", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "---\ndescription: api_key: {secret:API_KEY}\n---\nbody\n")

		f.sync(t)

		data := read(t, repoFile(repo))

		Convey("When the digest is rendered", func() {
			Convey("Then refs are redacted", func() {
				So(data, ShouldContainSubstring, "[redacted]")
				So(data, ShouldNotContainSubstring, "{secret:API_KEY}")
			})
		})
	})
}

func TestMemoryGitignoreCleanup(t *testing.T) {
	Convey("Given a vault gitignore with the legacy memory lines", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		legacy := read(t, filepath.Join(f.vault.Root(), ".gitignore")) + vaultLegacyMemoryLines()
		write(t, filepath.Join(f.vault.Root(), ".gitignore"), legacy)

		write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

		f.sync(t)

		ignore := read(t, filepath.Join(f.vault.Root(), ".gitignore"))

		Convey("When the secret gate lands", func() {
			ignored, err := f.vault.MemoryIgnored()
			So(err, ShouldBeNil)

			f.sync(t)

			Convey("Then the legacy ignore is removed idempotently", func() {
				So(ignore, ShouldNotContainSubstring, "memory/")
				So(ignore, ShouldNotContainSubstring, "U-12 secret gate lands\nmemory")
				So(ignored, ShouldBeFalse)
				So(read(t, filepath.Join(f.vault.Root(), ".gitignore")), ShouldEqual, ignore)
			})
		})
	})
}

func TestDoctorFlagsTrackedPlaintextNotes(t *testing.T) {
	Convey("Given a canon note with plaintext while git ignores are absent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it flags tracked plaintext", func() {
				So(hasIssue(issues, engine.SeverityError, "plaintext secrets are not ignored by git"), ShouldBeTrue)
			})
		})
	})
}

func TestDoctorReportsNoteRefs(t *testing.T) {
	Convey("Given a canon note referencing a stored secret", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:API_KEY}\n")
		f.engine.Secrets().Set("API_KEY", u12ApiKey)
		So(f.engine.Secrets().Save(), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When the value is present then removed", func() {
			f.engine.Secrets().Delete("API_KEY")

			issuesGone, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then doctor counts refs and later reports the missing value", func() {
				So(hasIssue(issues, engine.SeverityInfo, "1 secret reference(s) in 1 memory note(s)"), ShouldBeTrue)
				So(hasIssue(issuesGone, engine.SeverityError, "secret API_KEY has no value"), ShouldBeTrue)
			})
		})
	})
}
