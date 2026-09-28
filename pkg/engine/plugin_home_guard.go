package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/state"
)

// pluginHomeMoveEnv is the explicit opt-in for running a vault from a
// different home: the destructive plugin passes stay skipped while it is
// unset.
const pluginHomeMoveEnv = "BEADLE_ALLOW_HOME_MOVE"

// pluginHomeGuard decides whether the destructive plugin passes (plugin
// retirement, orphan pivot removal) may run in this process.
//
// The plugin readers walk the CURRENT home, so a run with a foreign home (a
// container, CI, a temp HOME, a sudo'd run) sees no plugins and the
// retirement pass would delete the pivots — and with them the links the hosts
// follow — of a vault that belongs to another machine. The vault records the
// home of its first sync; a mismatch skips the destructive passes until the
// user opts in.
type pluginHomeGuard struct {
	Recorded string
	Current  string
	Allowed  bool
}

// pluginHomeGuard reads the vault's recorded home and the current process
// home.
func (e *Engine) pluginHomeGuard() pluginHomeGuard {
	guard := pluginHomeGuard{
		Current: e.home,
		Allowed: strings.TrimSpace(os.Getenv(pluginHomeMoveEnv)) != "",
	}

	if st, err := state.Load(e.vault.StatePath()); err == nil {
		guard.Recorded = st.Home
	}

	return guard
}

// Retireable reports whether the destructive passes may run: the vault has no
// record yet (first sync), the home matches the record, or the user opted in.
func (g pluginHomeGuard) Retireable() bool {
	if g.Recorded == "" || g.Allowed {
		return true
	}

	return sameHomePath(g.Recorded, g.Current)
}

// Warning renders the skip, naming both homes and the opt-in.
func (g pluginHomeGuard) Warning() string {
	return "plugins: this vault was synced from " + g.Recorded + " but the current home is " + g.Current +
		"; plugin retirement, orphan pivot removal and stale link cleanup are skipped (set " +
		pluginHomeMoveEnv + "=1 to adopt the current home, then re-run)"
}

// recordSyncHome records the home this vault is synced from: the first sync
// sets it, and a user who explicitly allowed the move adopts the current home
// here, so the plugin guard compares against a current record afterwards and
// the warning does not repeat forever.
func (e *Engine) recordSyncHome(st *state.State) {
	if guard := e.pluginHomeGuard(); guard.Retireable() && (st.Home == "" || guard.Allowed) {
		st.Home = e.home
	}
}

// sameHomePath compares two homes the way the plugin readers would see them:
// a resolved, cleaned absolute path, so a symlinked tmp dir or a trailing
// slash does not read as a move.
func sameHomePath(a, b string) bool {
	resolve := func(path string) string {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}

		return filepath.Clean(path)
	}

	return a != "" && b != "" && resolve(a) == resolve(b)
}
