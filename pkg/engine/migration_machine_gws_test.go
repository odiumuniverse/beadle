package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

const (
	kindSkills    = kind.Skills
	kindCommands  = kind.Commands
	kindSubagents = kind.Subagents
)

// machineSpec is the shape of the machine this migration must survive: three
// Claude plugins in the ledger, a farm that linked one plugin's skills into six
// places, and a third-party manager's own lock file and links sitting in the
// same directories.
const (
	farmPlugin    = "acme/caveman"
	farmSkills    = 1
	farmCommands  = 8
	farmAgents    = 3
	ledgerEntries = 3
)

// machineBuild lays the machine out by hand rather than by running the farm:
// the point of the fixture is the state the user would have, not the state a
// beadle run produces.
type machineBuild struct {
	home       string
	vault      *vault.Vault
	config     *config.Config
	manager    *recordingManager
	skillsHost []string
	links      []string
	farmRoot   string
	lockFile   string
	lockSum    string
	foreign    map[string]string // path → symlink target, the third party's own
	agentsDir  string
}

// newMachineFixture makes the home the machine lives in, the vault beside it,
// and the config with the six hosts the farm linked into enabled.
func newMachineFixture(t *testing.T) *machineBuild {
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

	// The six hosts the farm linked into, plus the ones the shared skills
	// surface serves.
	for _, id := range []string{
		agent.ClaudeCodeID, agent.GeminiCLIID, agent.OpenCodeID,
		agent.CodexID, agent.CursorID, agent.SharedID,
	} {
		cfg.Enable(id)
	}

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return &machineBuild{home: home, vault: v, config: cfg, foreign: map[string]string{}}
}

// pluginKeys is the ledger's own key list, sorted so the cache and the ledger
// are written in one fixed order.
func pluginKeys() []string {
	keys := []string{farmPlugin, "acme/two", "acme/three"}
	slices.Sort(keys)

	return keys
}

// plantPluginCache writes the install cache of every ledger plugin and points
// the build at the farm's payload root. It returns the cache root, which is
// what the ledger records each key as having installed into.
func plantPluginCache(t *testing.T, m *machineBuild) string {
	t.Helper()

	cacheRoot := filepath.Join(m.home, ".claude", "plugins", "cache")
	m.farmRoot = filepath.Join(m.vault.PluginsDir(), "acme", "caveman", "current")

	for _, key := range pluginKeys() {
		owner, name, _ := strings.Cut(key, "/")
		install := filepath.Join(cacheRoot, owner, name, "0.1.0")
		_ = os.MkdirAll(filepath.Join(install, "skills", "caveman"), 0o750)
		_ = os.WriteFile(filepath.Join(install, "skills", "caveman", "SKILL.md"), []byte("# caveman\n"), 0o600)
		_ = os.WriteFile(filepath.Join(install, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o600)
	}

	return cacheRoot
}

// plantFarmPayload writes what the farm rendered from its own plugin — one
// skill, eight commands, three agents — and returns the skill directory the
// host links point at.
func plantFarmPayload(t *testing.T, m *machineBuild) string {
	t.Helper()

	skill := filepath.Join(m.farmRoot, "skills", "caveman")
	_ = os.MkdirAll(skill, 0o700)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: caveman\ndescription: d\n---\n\nb\n"), 0o600)

	for i := range farmCommands {
		dir := filepath.Join(m.farmRoot, "commands")
		_ = os.MkdirAll(dir, 0o700)

		if err := os.WriteFile(filepath.Join(dir, cmdName(i)), []byte("run "+cmdName(i)), 0o600); err != nil {
			t.Fatalf("write command: %v", err)
		}
	}

	for i := range farmAgents {
		dir := filepath.Join(m.farmRoot, "agents")
		_ = os.MkdirAll(dir, 0o700)

		if err := os.WriteFile(filepath.Join(dir, agentName(i)), []byte("agent "+agentName(i)), 0o600); err != nil {
			t.Fatalf("write agent: %v", err)
		}
	}

	return skill
}

// writePluginLedger writes the ledger, verbatim in shape: every key with the
// versioned cache directory it installed into.
func writePluginLedger(t *testing.T, m *machineBuild, cacheRoot string) {
	t.Helper()

	ledger := map[string]any{"version": 1, "plugins": map[string]any{}}

	for _, key := range pluginKeys() {
		owner, name, _ := strings.Cut(key, "/")
		plugins, _ := ledger["plugins"].(map[string]any)
		plugins[key] = map[string]any{
			"version": "0.1.0",
			"target":  filepath.Join(cacheRoot, owner, name, "0.1.0"),
			"source":  "claude",
		}
	}

	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}

	_ = os.MkdirAll(filepath.Dir(m.vault.PluginsLedgerPath()), 0o700)
	if err := os.WriteFile(m.vault.PluginsLedgerPath(), data, 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
}

// plantSkillLinks links the farm's skill into every skills surface the
// enabled hosts serve, and returns those directories sorted.
func plantSkillLinks(t *testing.T, m *machineBuild, agents []*agent.Agent, skill string) []string {
	t.Helper()

	var skillDirs []string

	for _, a := range agents {
		if !m.config.Agents[a.ID].Enabled {
			continue
		}

		if s := a.Surface(kindSkills); s != nil {
			skillDirs = append(skillDirs, s.Path())
		}
	}

	slices.Sort(skillDirs)

	for _, dir := range skillDirs {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Symlink(skill, filepath.Join(dir, "caveman"))
		m.links = append(m.links, filepath.Join(dir, "caveman"))
	}

	return skillDirs
}

// plantCommandLinks puts the farm's commands and agents where the real host
// surfaces put them, and returns those two directories.
func plantCommandLinks(t *testing.T, m *machineBuild, agents []*agent.Agent) (commandDir, agentDir string) {
	t.Helper()

	// Commands and agents, where the real surfaces put them.
	for _, a := range agents {
		if a.ID != agent.ClaudeCodeID {
			continue
		}

		if s := a.Surface(kindCommands); s != nil {
			commandDir = s.Path()
		}

		if s := a.Surface(kindSubagents); s != nil {
			agentDir = s.Path()
		}
	}

	for _, dir := range []string{commandDir, agentDir} {
		if dir != "" {
			_ = os.MkdirAll(dir, 0o700)
		}
	}

	for i := range farmCommands {
		link := filepath.Join(commandDir, "caveman--"+cmdName(i))
		_ = os.Symlink(filepath.Join(m.farmRoot, "commands", cmdName(i)), link)
		m.links = append(m.links, link)
	}

	for i := range farmAgents {
		link := filepath.Join(agentDir, "caveman--"+agentName(i))
		_ = os.Symlink(filepath.Join(m.farmRoot, "agents", agentName(i)), link)
		m.links = append(m.links, link)
	}

	return commandDir, agentDir
}

// plantThirdParty plants what another manager owns in the same directories
// the farm linked into: its own links, and the lock file that describes them.
func plantThirdParty(t *testing.T, m *machineBuild, skillDirs []string) {
	t.Helper()

	// A third-party manager shares the shared skills surface with the farm.
	// Its lock file and its own links must come out of this byte-identical.
	agentsDir := filepath.Join(m.home, ".agents", "skills")

	for _, dir := range skillDirs {
		if filepath.Base(dir) == "skills" && strings.Contains(dir, ".agents") {
			agentsDir = dir
		}
	}

	m.agentsDir = agentsDir

	lock := filepath.Join(m.home, ".agents", ".skill-lock.json")
	payload := `{"packages":["mattpocock/skills","vercel-labs/skills"]}` + "\n"
	_ = os.WriteFile(lock, []byte(payload), 0o600)
	m.lockFile = lock
	m.lockSum = fileSum(t, lock)

	for _, foreign := range []string{"herdrdev-herdr", "UditAkhourii-adhd"} {
		dir := filepath.Join(m.home, "foreign", foreign)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+foreign+"\n"), 0o600)

		link := filepath.Join(agentsDir, foreign)
		_ = os.Symlink(dir, link)
		m.foreign[link] = dir
	}
}

// newManager is the manager the migration will meet. A real manager claims
// the cell paths it will write in each host and the farm tree it takes over.
// The claims are built from the *resolved* surfaces, because a host's
// configuration root is not always under the process home — an adapter may
// resolve elsewhere, and a claim list built from the home would stop the
// migration for a cell the manager will in fact write.
func (m *machineBuild) newManager(skillDirs []string, commandDir, agentDir string) *recordingManager {
	claims := []string{filepath.Join(m.vault.Root(), "plugins")}

	for _, dir := range skillDirs {
		claims = append(claims, dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}

	for _, dir := range []string{commandDir, agentDir} {
		if dir != "" {
			claims = append(claims, dir, filepath.Dir(dir))
		}
	}

	return &recordingManager{
		claims: claims,
		home:   filepath.Join(m.vault.Root(), "verger"),
	}
}

func buildMachine(t *testing.T) *machineBuild {
	t.Helper()

	m := newMachineFixture(t)
	cacheRoot := plantPluginCache(t, m)
	skill := plantFarmPayload(t, m)
	writePluginLedger(t, m, cacheRoot)

	// The links the farm wrote, in each host — read from the real surfaces
	// rather than assumed, because a fixture that writes where the product
	// does not is a fixture that proves nothing. (The opencode surface
	// resolves through the engine's test home, and an assumed
	// ~/.config/opencode/skills is exactly the kind of fixture bug that
	// makes a green test a lie.)
	agents := agent.All(m.home, t.TempDir())
	skillDirs := plantSkillLinks(t, m, agents, skill)
	commandDir, agentDir := plantCommandLinks(t, m, agents)
	plantThirdParty(t, m, skillDirs)
	m.skillsHost = skillDirs
	m.manager = m.newManager(skillDirs, commandDir, agentDir)

	return m
}

// cmdName and agentName are the fixture's file names: a fixed small index, so
// the rune conversion is a formatting convenience and not arithmetic.
func cmdName(i int) string   { return fmt.Sprintf("command-%c.md", 'a'+i) }  //nolint:gosec // G115
func agentName(i int) string { return fmt.Sprintf("subagent-%c.md", 'a'+i) } //nolint:gosec // G115

func fileSum(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: the test's own tree
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func (m *machineBuild) engine(t *testing.T, withManager bool) *engine.Engine {
	t.Helper()

	opts := []engine.Option{engine.WithHome(m.home)}
	if withManager {
		opts = append(opts, engine.WithPluginManager(m.manager), engine.WithVergerOwns(m.manager.Owns))
	}

	e, err := engine.New(m.vault, m.config, agent.All(m.home, t.TempDir()), opts...)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return e
}

// untouchedThirdParty asserts the bytes and the links another manager owns did
// not move. It is the assertion the whole design turns on: a glob over
// ~/.agents/skills would take two of them with it.
func (m *machineBuild) untouchedThirdParty(t *testing.T) {
	t.Helper()

	So(fileSum(t, m.lockFile), ShouldEqual, m.lockSum)

	for link, want := range m.foreign {
		got, err := os.Readlink(link)
		So(err, ShouldBeNil)
		So(got, ShouldEqual, want)
	}
}

// TestTheRealMachineShapeMigratesEndToEnd is the §4 fixture: a vault with three
// Claude plugins in its ledger, a farm that linked one plugin's skill into six
// places and its commands and agents into a host, and a third-party manager
// sharing one of those directories.
func TestTheRealMachineShapeMigratesEndToEnd(t *testing.T) {
	Convey("Given a machine with three ledger plugins and a live farm", t, func() {
		m := buildMachine(t)
		e := m.engine(t, true)

		Convey("When the first sync runs", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then every farmed plugin is handed over", func() {
				So(m.manager.adopted, ShouldContain, farmPlugin)

				// Distinct keys, not calls: the deliver stage re-adopts a key
				// on purpose, so the file is written after the claim, and a
				// count of calls would say nothing about coverage.
				distinct := map[string]bool{}
				for _, key := range m.manager.adopted {
					distinct[key] = true
				}

				So(len(distinct), ShouldEqual, ledgerEntries)
			})

			Convey("Then the third party's lock and links are byte-identical", func() {
				m.untouchedThirdParty(t)
			})

			Convey("Then a backup exists before anything was removed", func() {
				backups, readErr := os.ReadDir(filepath.Join(m.vault.Root(), "state", "backups"))
				So(readErr, ShouldBeNil)
				So(backups, ShouldNotBeEmpty)
			})

			Convey("Then the farm's own links are gone, because the manager delivers files now", func() {
				for _, link := range m.links {
					_, statErr := os.Lstat(link)
					So(os.IsNotExist(statErr), ShouldBeTrue)
				}
			})

			Convey("And the third party's links are untouched in the same directories", func() {
				m.untouchedThirdParty(t)

				// The two managers shared one directory; one set of links went
				// and the other stayed. That is the whole claim.
				So(len(m.foreign), ShouldEqual, 2)

				// The third party's links sat in the very directory the farm
				// linked into, which is why a glob there would have taken
				// them.
				for link := range m.foreign {
					So(filepath.Dir(link), ShouldEqual, m.agentsDir)
				}
			})

			Convey("Then a second sync does not migrate again", func() {
				// The record is the no-op: a state file also carries the
				// skill bases the engine reads on every run, so byte
				// equality of the whole document is not the claim. What must
				// not change is what the migration recorded — including the
				// list of paths it removed, which would grow if it ran
				// twice.
				//
				// The first sync in this block is not the one the parent
				// made: the migration is what is under test, so this block
				// runs it to completion itself and only then reads.
				_, settleErr := e.Sync(t.Context(), engine.SyncOptions{})
				So(settleErr, ShouldBeNil)

				before, loadErr := state.Load(m.vault.StatePath())
				So(loadErr, ShouldBeNil)
				So(before.FarmMigration, ShouldNotBeNil)

				_, syncErr := e.Sync(t.Context(), engine.SyncOptions{})
				So(syncErr, ShouldBeNil)

				after, loadErr := state.Load(m.vault.StatePath())
				So(loadErr, ShouldBeNil)
				So(after.FarmMigration.Stage, ShouldEqual, before.FarmMigration.Stage)
				So(after.FarmMigration.UpdatedAt, ShouldEqual, before.FarmMigration.UpdatedAt)
				So(after.FarmMigration.Removed, ShouldResemble, before.FarmMigration.Removed)
				So(after.FarmMigration.Adopted, ShouldResemble, before.FarmMigration.Adopted)

				m.untouchedThirdParty(t)
			})
		})
	})
}

// TestAnInterruptedMigrationResumes is the crash case: a migration that stopped
// after the claim stage must finish on the next run rather than start over, and
// must not touch a third party's files on the way.
func TestAnInterruptedMigrationResumes(t *testing.T) {
	Convey("Given a migration that stopped after the claim stage", t, func() {
		m := buildMachine(t)

		// A manager that claims the farm tree but nothing in the hosts: the
		// claim stage fails, exactly as it would if the library were not
		// ready, and the migration stops with its record intact.
		partial := &recordingManager{
			claims: []string{filepath.Join(m.vault.Root(), "plugins")},
			home:   m.manager.home,
		}

		stopped := m.engineWithManager(t, partial)

		report, err := stopped.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)
		So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "does not claim")

		st, loadErr := state.Load(m.vault.StatePath())
		So(loadErr, ShouldBeNil)
		So(st.FarmMigration, ShouldNotBeNil)
		So(st.FarmMigration.Done, ShouldBeFalse)
		So(st.FarmMigration.Stage, ShouldEqual, state.FarmStageAdopt)
		So(st.FarmMigration.Backup, ShouldNotBeEmpty)

		Convey("And the migration removed nothing", func() {
			// The migration's own record, not the directory: the sync's farm
			// pass runs after the migration and prunes what it can no longer
			// find, which is its business and not this assertion's. What the
			// migration guarantees is that it recorded no removal.
			after, loadErr := state.Load(m.vault.StatePath())
			So(loadErr, ShouldBeNil)
			So(after.FarmMigration.Removed, ShouldBeEmpty)

			m.untouchedThirdParty(t)
		})

		Convey("When a ready manager takes over and the vault syncs again", func() {
			resumed := m.engine(t, true)

			_, syncErr := resumed.Sync(t.Context(), engine.SyncOptions{})
			So(syncErr, ShouldBeNil)

			Convey("Then the migration finishes", func() {
				done, doneErr := state.Load(m.vault.StatePath())
				So(doneErr, ShouldBeNil)
				So(done.FarmMigration, ShouldNotBeNil)
				So(done.FarmMigration.Done, ShouldBeTrue)
				So(done.FarmMigration.Stage, ShouldEqual, state.FarmStageVerify)
			})

			Convey("And the third party's files are still untouched", func() {
				m.untouchedThirdParty(t)
			})
		})
	})
}

// engineWithManager builds an engine around a chosen manager, so a test can run
// a migration twice with two different answers from the library.
func (m *machineBuild) engineWithManager(t *testing.T, manager *recordingManager) *engine.Engine {
	t.Helper()

	e, err := engine.New(m.vault, m.config, agent.All(m.home, t.TempDir()),
		engine.WithHome(m.home),
		engine.WithPluginManager(manager),
		engine.WithVergerOwns(manager.Owns))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return e
}
