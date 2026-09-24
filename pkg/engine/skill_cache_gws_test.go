package engine_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// ageFiles moves every modification time under root into the past, so a fresh
// write does not fall inside the skill cache's racy window.
func ageFiles(t *testing.T, root string) {
	t.Helper()

	past := time.Now().Add(-time.Hour)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		return os.Chtimes(path, past, past) //nolint:gosec // G122: the test ages its own fixture tree
	})
	if err != nil {
		t.Fatalf("age %s: %v", root, err)
	}
}

// poisonSameSize rewrites a file with new content of the same length and
// restores its modification time and permission bits, so only a content read
// can notice.
func poisonSameSize(t *testing.T, path, content string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	old, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own fixture file
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	if len(old) != len(content) {
		t.Fatalf("poison %s: content size %d does not match %d", path, len(content), len(old))
	}

	write(t, path, content)

	if err := os.Chmod(path, info.Mode().Perm()); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}

	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// cacheEngine builds a second engine over the same fixture with an empty
// in-process cache; pass engine.WithSkillCacheDisabled() to compare against a
// scan that reads every tree.
func cacheEngine(t *testing.T, f *fixture, opts ...engine.Option) *engine.Engine {
	t.Helper()

	options := append([]engine.Option{engine.WithHome(f.home)}, opts...)

	e, err := engine.New(f.vault, f.config, agent.All(f.home, t.TempDir()), options...)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return e
}

// cachedSkillFixture syncs a canon skill to Cursor and ages the delivered
// copy, so its cache entry is stable and the next scan can hit it.
func cachedSkillFixture(t *testing.T) (*fixture, string) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)
	cursorWritesSkills(t, f)

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")

	f.sync(t)

	root := filepath.Join(f.home, ".cursor", "skills", "alpha")
	ageFiles(t, root)

	f.sync(t)

	return f, root
}

func TestSkillCacheHitSkipsReread(t *testing.T) {
	Convey("Given a cached Cursor copy with an aged listing", t, func() {
		f, root := cachedSkillFixture(t)

		st := loadState(t, f)

		entry, ok := st.SkillTreeFor(root)
		So(ok, ShouldBeTrue)
		So(entry.Digest, ShouldNotBeEmpty)
		So(entry.Fingerprint, ShouldNotBeEmpty)

		Convey("When the copy is edited with the same size and mtime", func() {
			poisonSameSize(t, filepath.Join(root, "SKILL.md"), "# beta!\n")

			Convey("Then a fresh engine reuses the cached digest and reports no fork", func() {
				issues, err := cacheEngine(t, f).Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeFalse)
			})

			Convey("And an engine without the cache reads the edit and reports the fork", func() {
				issues, err := cacheEngine(t, f, engine.WithSkillCacheDisabled()).Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})
	})
}

func TestSkillCacheInvalidation(t *testing.T) {
	Convey("Given a cached Cursor copy with an aged listing", t, func() {
		f, root := cachedSkillFixture(t)
		skillFile := filepath.Join(root, "SKILL.md")

		Convey("When the content changes with a new size", func() {
			write(t, skillFile, "# alpha changed\n")

			Convey("Then the next scan reads it and reports the fork", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})

		Convey("When the content changes with the same size and a new mtime", func() {
			write(t, skillFile, "# beta!\n")

			Convey("Then the next scan reads it and reports the fork", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})

		Convey("When only the mtime changes", func() {
			later := time.Now().Add(time.Minute)
			So(os.Chtimes(skillFile, later, later), ShouldBeNil)

			f.sync(t)

			Convey("Then the entry is refreshed with the new listing", func() {
				entry, ok := loadState(t, f).SkillTreeFor(root)
				So(ok, ShouldBeTrue)
				So(entry.Latest, ShouldEqual, later)
			})
		})

		Convey("When a nested file is added", func() {
			write(t, filepath.Join(root, "refs", "extra.md"), "extra\n")

			Convey("Then the next scan reads it and reports the fork", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})
	})
}

func TestSkillCachePrunesVanishedRoots(t *testing.T) {
	Convey("Given a synced fixture with a foreign copy in a read area", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		cursorWritesSkills(t, f)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")

		foreign := filepath.Join(f.home, ".claude", "skills", "foreign")
		write(t, filepath.Join(foreign, "SKILL.md"), "# foreign\n")
		ageFiles(t, foreign)

		f.sync(t)

		_, ok := loadState(t, f).SkillTreeFor(foreign)
		So(ok, ShouldBeTrue)

		Convey("When the copy is renamed", func() {
			renamed := filepath.Join(f.home, ".claude", "skills", "renamed")
			So(os.Rename(foreign, renamed), ShouldBeNil)

			f.sync(t)

			Convey("Then the old entry is gone and the new root is cached", func() {
				st := loadState(t, f)

				_, ok := st.SkillTreeFor(foreign)
				So(ok, ShouldBeFalse)

				entry, ok := st.SkillTreeFor(renamed)
				So(ok, ShouldBeTrue)
				So(entry.Digest, ShouldNotBeEmpty)
			})
		})

		Convey("When the copy is removed", func() {
			So(os.RemoveAll(foreign), ShouldBeNil)

			f.sync(t)

			Convey("Then its entry is gone from the state", func() {
				_, ok := loadState(t, f).SkillTreeFor(foreign)
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestSkillCacheRacyWindow(t *testing.T) {
	Convey("Given a cached copy whose entry was computed moments ago", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		cursorWritesSkills(t, f)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")

		f.sync(t)
		f.sync(t)

		root := filepath.Join(f.home, ".cursor", "skills", "alpha")

		st := loadState(t, f)

		entry, ok := st.SkillTreeFor(root)
		So(ok, ShouldBeTrue)

		// Re-stamp the entry as if it were computed right now: the listing
		// still matches, so only the racy window can keep the digest honest.
		stat, err := skill.StatTree(root)
		So(err, ShouldBeNil)

		entry.Fingerprint = stat.Fingerprint
		entry.Latest = stat.Latest
		entry.Stamp = time.Now()
		st.SetSkillTree(root, entry)
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		Convey("When the copy is edited with the same size and mtime right away", func() {
			poisonSameSize(t, filepath.Join(root, "SKILL.md"), "# beta!\n")

			Convey("Then a fresh engine reads again and reports the fork: a racy tree is not trusted", func() {
				issues, err := cacheEngine(t, f).Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})
	})
}

func TestSkillCacheDecisionsIdentical(t *testing.T) {
	Convey("Given a synced fixture with delivered and foreign copies", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		cursorWritesSkills(t, f)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"), "# beta\n")

		foreignSkill(t, f, "beta", "# beta foreign\n")

		f.sync(t)

		Convey("When doctor and explain run with and without the cache", func() {
			cachedIssues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			plainIssues, err := cacheEngine(t, f, engine.WithSkillCacheDisabled()).Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the doctor findings are identical", func() {
				So(cachedIssues, ShouldResemble, plainIssues)
			})

			cachedRows, err := f.engine.Explain(t.Context(), "beta")
			So(err, ShouldBeNil)

			plainRows, err := cacheEngine(t, f, engine.WithSkillCacheDisabled()).Explain(t.Context(), "beta")
			So(err, ShouldBeNil)

			Convey("Then the explain rows are identical", func() {
				So(cachedRows, ShouldResemble, plainRows)
			})
		})
	})
}

func TestSkillCacheEmptyDigestIsNotTrusted(t *testing.T) {
	Convey("Given a state entry whose listing matches but digest is empty", t, func() {
		f, root := cachedSkillFixture(t)

		st := loadState(t, f)

		entry, ok := st.SkillTreeFor(root)
		So(ok, ShouldBeTrue)

		stat, err := skill.StatTree(root)
		So(err, ShouldBeNil)

		entry.Digest = ""
		entry.Fingerprint = stat.Fingerprint
		entry.Latest = stat.Latest
		entry.Stamp = time.Now()
		st.SetSkillTree(root, entry)
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		Convey("When a fresh engine scans the copy that matches the canon", func() {
			issues, err := cacheEngine(t, f).Doctor(t.Context())

			Convey("Then it reads the tree and reports no false fork", func() {
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillCacheReadOnly(t *testing.T) {
	Convey("Given a synced fixture with cached skill trees", t, func() {
		f, _ := cachedSkillFixture(t)

		// Drop the cached entries so a scan would write them again: a read-only
		// command must still leave the state untouched.
		st := loadState(t, f)
		So(st.DropSkillTrees(func(string) bool { return true }), ShouldBeGreaterThan, 0)
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		before := hashTree(t, f.home) + hashTree(t, f.vault.Root())

		Convey("When doctor, explain and a dry-run sync run", func() {
			e := cacheEngine(t, f)

			_, err := e.Doctor(t.Context())
			So(err, ShouldBeNil)

			_, err = e.Explain(t.Context(), "alpha")
			So(err, ShouldBeNil)

			report, err := e.Sync(t.Context(), engine.SyncOptions{DryRun: true})
			So(err, ShouldBeNil)
			So(report.DryRun, ShouldBeTrue)

			Convey("Then home and vault are byte-identical: the cache is never persisted read-only", func() {
				So(hashTree(t, f.home)+hashTree(t, f.vault.Root()), ShouldEqual, before)
			})

			Convey("And the pin itself notices a state write", func() {
				// A guard only guards if the regression fails it: save one
				// entry and prove the pin would have caught it.
				probe := loadState(t, f)
				probe.SetSkillTree(filepath.Join(t.TempDir(), "pin-probe"), state.SkillTree{Digest: "probe"})
				So(probe.Save(f.vault.StatePath()), ShouldBeNil)

				So(hashTree(t, f.home)+hashTree(t, f.vault.Root()), ShouldNotEqual, before)
			})
		})
	})
}
