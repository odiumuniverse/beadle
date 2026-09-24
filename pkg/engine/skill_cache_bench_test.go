package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// benchSkillCount is the fixture size: skills per host, each five files.
const benchSkillCount = 50

// benchRef is a realistic reference file: enough bytes that reading the tree
// costs visibly more than listing it.
var benchRef = strings.Repeat("reference line for the skill tree benchmark\n", 200)

func writeBenchFile(b *testing.B, path, content string) {
	b.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		b.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

// ageBench moves every modification time under root into the past, so the
// cache trusts the listing it just computed.
func ageBench(b *testing.B, root string) {
	b.Helper()

	past := time.Now().Add(-time.Hour)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		return os.Chtimes(path, past, past) //nolint:gosec // G122: the benchmark ages its own fixture tree
	})
	if err != nil {
		b.Fatalf("age %s: %v", root, err)
	}
}

// benchSkillTree writes one skill tree with a root and four reference files.
func benchSkillTree(b *testing.B, dir, name string) {
	b.Helper()

	writeBenchFile(b, filepath.Join(dir, "SKILL.md"), "# "+name+"\n\n"+benchRef)

	for i := range 4 {
		writeBenchFile(b, filepath.Join(dir, "refs", fmt.Sprintf("ref-%d.md", i)), benchRef)
	}
}

// benchEngine builds a vault with n canon skills and one copy per host, ages
// every listing, and returns a cache-enabled engine, a cache-disabled engine
// and one skill name. Cursor writes skills, so a sync scans its read area.
func benchEngine(b *testing.B, n int) (*engine.Engine, *engine.Engine, string) {
	b.Helper()

	home := b.TempDir()
	v := vault.New(filepath.Join(b.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		b.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		b.Fatalf("load config: %v", err)
	}

	cfg.Enable(agent.CursorID)
	cfg.SetMode(agent.CursorID, kind.Skills, config.ModeSync)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		b.Fatalf("save config: %v", err)
	}

	var firstName string

	for i := range n {
		name := fmt.Sprintf("skill-%02d", i)
		if firstName == "" {
			firstName = name
		}

		benchSkillTree(b, filepath.Join(v.SkillsDir(), name), name)
		benchSkillTree(b, filepath.Join(home, ".cursor", "skills", name), name)
		benchSkillTree(b, filepath.Join(home, ".claude", "skills", name), name)
	}

	ageBench(b, home)
	ageBench(b, v.SkillsDir())

	agents := agent.All(home, b.TempDir())

	cached, err := engine.New(v, cfg, agents, engine.WithHome(home))
	if err != nil {
		b.Fatalf("new engine: %v", err)
	}

	plain, err := engine.New(v, cfg, agents, engine.WithHome(home), engine.WithSkillCacheDisabled())
	if err != nil {
		b.Fatalf("new engine: %v", err)
	}

	// Warm the cache so the cached arm measures hits, not the first read.
	if _, err := cached.Explain(b.Context(), firstName); err != nil {
		b.Fatalf("warm explain: %v", err)
	}

	return cached, plain, firstName
}

// BenchmarkSkillVisibilityScan measures the coverage scan Explain runs: every
// host's readable skill trees, digested. cached is the A-45 path; uncached is
// the behavior before it.
func BenchmarkSkillVisibilityScan(b *testing.B) {
	cached, plain, name := benchEngine(b, benchSkillCount)

	b.Run("cached", func(b *testing.B) {
		for b.Loop() {
			if _, err := cached.Explain(b.Context(), name); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("uncached", func(b *testing.B) {
		for b.Loop() {
			if _, err := plain.Explain(b.Context(), name); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkSkillSyncScan measures the dry-run sync, the pass that scans the
// skill read areas of every writing host.
func BenchmarkSkillSyncScan(b *testing.B) {
	cached, plain, _ := benchEngine(b, benchSkillCount)

	b.Run("cached", func(b *testing.B) {
		for b.Loop() {
			if _, err := cached.Sync(b.Context(), engine.SyncOptions{DryRun: true}); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("uncached", func(b *testing.B) {
		for b.Loop() {
			if _, err := plain.Sync(b.Context(), engine.SyncOptions{DryRun: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
