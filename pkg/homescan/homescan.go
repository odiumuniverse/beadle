// Package homescan watches the machine's real home for writes a test suite was
// not supposed to make.
//
// A suite that resolves a host root without pinning HOME writes into the
// developer's own home, and the damage is not visible afterwards: a stray
// watch.lease, one rewritten settings.json, a file dropped inside
// ~/.config/opencode that no test ever named. Comparing the home's top-level
// names catches the first kind and is blind to the second, because the entry
// the suite created is inside a directory that was already there.
//
// Watching the whole home is not an option either. A developer's own session
// rewrites thousands of files under it while a suite runs, and a guard that
// fires on those is a guard that gets switched off. So the watch is scoped to
// the roots beadle, verger and the adapters can actually write — taken from the
// same table production reads, so a host that gains a root is covered here
// without a change to this file — plus the two tools' own homes. Everything else
// in the home is left alone.
//
// What a snapshot answers is therefore narrow on purpose: did something write
// where this tool writes? It is not a general-purpose change detector for the
// home, and it is not trying to be.
//
//	s := homescan.Take(home) // before the suite
//	// ... run ...
//	for _, change := range s.Changed(homescan.Take(home)) {
//		fmt.Fprintln(os.Stderr, change)
//	}
package homescan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/odiumuniverse/verger/pkg/hostpath"

	"github.com/odiumuniverse/beadle/pkg/vault"
)

// vergerHomeName is verger's standalone plugin home, the tree a beadle with no
// vault of its own reads and writes. beadle names it in pkg/cli
// (plugins_vergerx.go, init.go); there is no constant to import, the way
// vault.DefaultDirName is one.
const vergerHomeName = ".verger"

// OverrideEnv names the tree the guard watches INSTEAD of the machine's own
// home. It exists so that a test which deliberately writes where the guard
// watches — a mutation, a probe, a rehearsal of the leak itself — has somewhere
// harmless to do it. The default is the real home and stays that way: a guard
// with a parameter is only worth having if the parameter is not the ordinary
// setting.
//
// It is read from the environment because the suites that need it are TestMains,
// and a TestMain has no *testing.T to set a variable through.
const OverrideEnv = "BEADLE_TEST_GUARD_HOME"

// GuardHome returns the home the guard should weigh: the machine's own, unless
// a caller asked for another tree. The override says so on stderr rather than
// taking effect quietly, because a run that watches something other than the
// real home is not the run that proves the guard works.
func GuardHome() string {
	override := os.Getenv(OverrideEnv)
	if override == "" {
		return os.Getenv("HOME")
	}

	fmt.Fprintf(os.Stderr, "homescan: the real-home guard watches %s, not this machine's home\n", override)

	return override
}

// StrictEnv names the variable that holds a run to account for what it did to
// the machine's real home. The default is the opposite of strict, and that is
// the design rather than a compromise: on a developer machine the agent
// sitting next to the suite writes the very paths beadle writes — the claude
// config, the synced-skill manifests under ~/.claude/skills — and narrowing
// the watch cannot separate the two, because they are the same files.
//
// So the primary defence is not the guard. It is the HOME pin, which is
// unconditional and strict in every mode. The guard is the second line, and it
// is strict exactly where there is no neighbour to be confused with a leak.
const StrictEnv = "BEADLE_TEST_GUARD"

// Verdict is what a list of real-home changes means to the suite that found
// them.
type Verdict int

const (
	// Clean is a home that held still.
	Clean Verdict = iota
	// Warn is a home that moved, in a run not held to account for it.
	Warn
	// Fail is a home that moved, in a run that is.
	Fail
)

// String names the verdict, so a failed assertion says "Fail" and not "2".
func (v Verdict) String() string {
	switch v {
	case Clean:
		return "Clean"
	case Warn:
		return "Warn"
	case Fail:
		return "Fail"
	default:
		return "Verdict(" + strconv.Itoa(int(v)) + ")"
	}
}

// Strict reports whether a change in the machine's real home must fail the
// suite. It is strict on CI, where nothing else runs on the runner and
// anything that moved was this suite, and on demand anywhere else through
// BEADLE_TEST_GUARD.
//
// GitHub Actions exports CI=true in every job, so the signal is on by itself
// there. ci.yml sets it explicitly anyway: a rule resting on a platform default
// nobody has read is a rule that fails quietly, and the cost of setting it is
// one line.
func Strict() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(StrictEnv)), "strict") {
		return true
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv("CI"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// StrictWhy names the knob that made this run strict, for the message a failing
// run prints. It earns its place because CI is read from the environment and
// the environment belongs to the shell: a developer who exports CI=true — this
// very session did, and it cost a confused probe — has asked for a strict guard
// without ever having heard of it, and the output has to tell them where it
// came from.
func StrictWhy() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(StrictEnv)), "strict") {
		return StrictEnv + "=strict"
	}

	return "CI=" + strings.TrimSpace(os.Getenv("CI"))
}

// Judge turns what the snapshot saw into what the suite does about it. It is a
// function rather than an inline decision in each TestMain so that both halves
// are testable without running one, and so that the four suites that call it
// cannot drift apart on what "strict" means.
func Judge(changes []string) Verdict {
	switch {
	case len(changes) == 0:
		return Clean
	case Strict():
		return Fail
	default:
		return Warn
	}
}

// WarnLines is what a developer is told when the real home moved and this run
// is not held to account for it.
func WarnLines(changes []string) []string {
	lines := make([]string, 0, len(changes)+5)
	for _, change := range changes {
		lines = append(lines, "WARNING real home: "+change)
	}

	return append(lines,
		"WARNING: the machine's home moved while this suite ran, and this run",
		"WARNING: is not held to account for it. A live agent on this machine writes",
		"WARNING: the same paths beadle writes, so this is not evidence of a leak.",
		"WARNING: set "+StrictEnv+"=strict, or run with CI=true, to hold it to account.",
	)
}

// WarnAbout prints the warning where a developer is looking, including on the
// run that passed — which is the only run it is ever for.
//
// go test prints a passing package's output only under -v, and make test does
// not ask for it. So a warning written to stderr alone is swallowed by precisely
// the case that needs it: the home moved, the suite was not blamed, nothing
// happened, and the developer is left to believe the guard is clean. Hence the
// second channel. It is best effort, and it costs nothing when there is no
// terminal to write to — a CI job or a pipe has none, and there the run is
// strict, so the same lines land in a failing package's output where they
// cannot be missed.
func WarnAbout(changes []string) {
	lines := WarnLines(changes)
	for _, line := range lines {
		fmt.Fprintln(os.Stderr, line)
	}

	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}

	defer func() { _ = tty.Close() }()

	for _, line := range lines {
		fmt.Fprintln(tty, line)
	}
}

// FailAbout prints a strict run's changes and says which knob made it strict.
// One channel is enough here: a failing package's output goes to the terminal
// and to the CI log without being asked for.
func FailAbout(changes []string) {
	for _, change := range changes {
		fmt.Fprintln(os.Stderr, "real home:", change)
	}

	fmt.Fprintln(os.Stderr, "real home: this run is strict ("+StrictWhy()+
		"), so it is held to account for those. Unset it for a machine with a")
	fmt.Fprintln(os.Stderr, "real home: live agent on it, which writes the same paths beadle does.")
}

// Snapshot is the state of the watched roots at one moment. The zero Snapshot
// watches nothing and reports no change, which is what a run with no HOME
// deserves.
//
// It remembers WHICH roots it walked, and Recheck walks those again rather than
// resolving them afresh. A suite that pins XDG_CONFIG_HOME and the other
// root-moving variables between the two snapshots would otherwise be weighed
// against a different set of directories than the one it started with, and
// every file under the developer's real ~/.config would read as "removed" — a
// guard that cries wolf on the very trees it exists to protect.
type Snapshot struct {
	roots   []string
	entries map[string]entry
}

// entry is one watched path. A directory is recorded so that a root the suite
// CREATES is caught, but its mtime is not compared: a directory's mtime moves
// whenever anything touches it, including the developer's own session, and the
// files inside it are the real signal.
type entry struct {
	size  int64
	mtime time.Time
	dir   bool
}

// Take walks the watched roots under home and records what is there. A root
// that does not exist is not recorded and not an error: the suite may still
// create it, and then it appears in the later snapshot as something new.
func Take(home string) Snapshot {
	s := Snapshot{entries: map[string]entry{}}
	if home == "" {
		return s
	}

	s.roots = Roots(home)
	for _, root := range s.roots {
		record(root, s.entries)
	}

	return s
}

// Recheck walks the same roots as the snapshot it is called on and returns what
// is there now, for handing to Changed.
func (s Snapshot) Recheck() Snapshot {
	now := Snapshot{roots: s.roots, entries: make(map[string]entry, len(s.entries))}

	for _, root := range s.roots {
		record(root, now.entries)
	}

	return now
}

// watchedFields are the HostSurfaces entries that name something beadle or
// verger WRITES inside a host's home. The paths come from the adapters' own
// table — this list names the fields, never a path — so a host that relocates
// its config is covered without an edit here.
//
// What is deliberately absent is the rest of a host's home. logs/, backups/,
// sessions/ and cache/ are the host writing its own runtime state, and a guard
// that watches them fails for anyone running the suite with a live agent on the
// same machine — measured, before this was narrowed:
//
//	created  /Users/u/.omp/logs/.omp.50624-audit.json
//	removed  /Users/u/.claude/backups/…
//
// That is not a suite leaking; it is the agent next to the suite. The other
// fields HostSurfaces carries — StateDir, ProjectsDir, Inbox, ProfilesDir,
// Markers, IgnoreRoots, and the read-only discovery lists — are the same kind of
// thing and are left out for the same reason.
var watchedFields = [...]string{
	"Rules",        // AGENTS.md, CLAUDE.md, GEMINI.md
	"RulesPerFile", // a directory of per-file rule documents
	"MCPDoc",       // settings.json, mcp.json, opencode.jsonc, kilo.jsonc
	"MCPPointer",
	"MCPTable", // config.toml
	"Skills",
	"Agents",
	"Commands",
	"Hooks",
	"HookModules",
	"Plugins",
	"PluginModulesWrite",
	"SharedSkillsWrite",
	"Settings",
}

// Roots returns, in home's own spelling, every path a beadle or verger run can
// write there: the two tools' own homes whole, and inside each host's home only
// the surfaces the adapters declare.
func Roots(home string) []string {
	env := hostpath.Env{Home: home, GOOS: runtime.GOOS, Lookup: os.LookupEnv}

	// The two tools' own homes, whole. Nothing but us runs in them, and a leak
	// there is the whole point of the guard.
	roots := []string{
		filepath.Join(home, vault.DefaultDirName),
		filepath.Join(home, vergerHomeName),
	}

	for _, id := range hostpath.All() {
		surfaces, err := hostpath.Surfaces(id, env)
		if err != nil {
			// A host whose surfaces cannot be resolved cannot be written to
			// through them either. Skipping cannot mask a write: the path would
			// have to be known first.
			continue
		}

		roots = append(roots, watched(surfaces, home)...)
	}

	return slices.Compact(slices.Sorted(slices.Values(dedupe(roots))))
}

// watched returns the listed fields of surfaces that name something under home.
// The fields are read reflectively so that a host which gains one is covered by
// adding its name to watchedFields, not by a second traversal of the table.
func watched(surfaces hostpath.HostSurfaces, home string) []string {
	value := reflect.ValueOf(surfaces)

	var found []string

	for _, name := range watchedFields {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.String {
			continue
		}

		path := field.String()
		if path == "" || !underHome(home, path) {
			continue
		}

		found = append(found, path)
	}

	return found
}

// underHome reports whether path names something inside home. The prefix test
// matters and was wrong the first time round: `rel != ".."` accepts "../../mcp",
// which is how a JSON pointer like /mcpServers — a fragment, not a path — came
// out of the roots list as a root of its own.
func underHome(home, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(path))
	if err != nil {
		return false
	}

	return rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..")
}

// Changed returns every way after differs from s, as "created"/"changed"/
// "removed" lines, sorted so a failure names its entries in the same order
// twice.
//
// Removals count: a suite that deletes the developer's settings.json is doing
// something no top-level name check would ever see, and it is exactly as
// damaging as a write.
func (s Snapshot) Changed(after Snapshot) []string {
	var changed []string

	for path, was := range s.entries {
		now, ok := after.entries[path]
		switch {
		case !ok:
			changed = append(changed, "removed  "+path)
		case was.dir || now.dir:
			// Presence already settled above; a directory's mtime is not a
			// change in its own right.
		case was.size != now.size || !was.mtime.Equal(now.mtime):
			changed = append(changed, "changed  "+path)
		}
	}

	for path := range after.entries {
		if _, ok := s.entries[path]; !ok {
			changed = append(changed, "created  "+path)
		}
	}

	slices.Sort(changed)

	return changed
}

// record walks root and files every path under it into into.
func record(root string, into map[string]entry) {
	info, err := os.Lstat(root)
	if err != nil {
		return
	}

	into[root] = describe(info)
	if !info.IsDir() {
		return
	}

	//nolint:errcheck // WalkDir's error is reported per entry, below.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// An entry that cannot be read is not a change the suite made, and
		// refusing to walk the rest of the tree over it would blind the guard
		// to everything below it.
		if err == nil {
			if fileInfo, infoErr := d.Info(); infoErr == nil {
				into[path] = describe(fileInfo)
			}
		}

		return nil
	})
}

func describe(info fs.FileInfo) entry {
	return entry{size: info.Size(), mtime: info.ModTime(), dir: info.IsDir()}
}

// dedupe drops empty paths and repeats, so Roots is a set the caller can walk
// without walking anything twice.
func dedupe(paths []string) []string {
	kept := make([]string, 0, len(paths))

	for _, path := range paths {
		if path == "" {
			continue
		}

		if slices.Contains(kept, path) {
			continue
		}

		kept = append(kept, path)
	}

	return kept
}
