package agent

import (
	"os"
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/verger/pkg/hostpath"
)

// hostEnv is the resolver environment: the home the caller resolved and the
// process environment, injected so the package stays pure and the host × env
// matrix is assertable in-process.
func hostEnv(home string) hostpath.Env {
	// The home is cleaned here, once, because this is where every host root is
	// derived: a home spelled with a doubled separator from a mktemp template
	// would otherwise be the uncleaned half of every comparison made downstream.
	return hostpath.Env{Home: fsutil.Root(home), GOOS: osGOOS, Lookup: os.LookupEnv}
}

// roots resolves one host's roots through verger's shared resolver (D-C). It
// is beadle's only path source: no other file in this package may join a home
// directory with a host subdirectory.
func roots(id, home string) hostpath.HostRoots {
	resolved, err := hostpath.Roots(id, hostEnv(home))
	if err != nil {
		// Roots fails only on an unknown id or an empty home, and every caller
		// in this package passes one of the ten canonical ids with the home the
		// caller resolved. A panic beats a silent wrong path.
		panic("agent: " + id + ": " + err.Error())
	}

	return resolved
}

// surfaces resolves one host's user-scope surfaces through the shared resolver.
// The same invariants hold as for roots: an error here means the caller passed
// something the package cannot be asked about, and guessing would write to the
// wrong tree.
func surfaces(id, home string) hostpath.HostSurfaces {
	resolved, err := hostpath.Surfaces(id, hostEnv(home))
	if err != nil {
		panic("agent: " + id + ": " + err.Error())
	}

	return resolved
}

// osGOOS is the operating system the resolver is asked about. It is a variable
// so a test can ask the matrix for another OS without a cross-compiled build.
var osGOOS = ""

// sharedAgentsDir is the cross-vendor shared root (~/.agents). Every
// user-scope surface carries it, and DSH may relocate it, so it is read from a
// surface rather than joined from the home.
func sharedAgentsDir(id, home string) string {
	return surfaces(id, home).SharedAgents
}

// sharedHub is the hub pseudo-agent's own surface. `shared` is beadle's
// pseudo-agent, not one of the resolver's ten host ids, so the hub has its own
// entry point rather than an eleventh id: giving it one would let a caller ask
// for a config root, a rules file and a hooks document it does not have.
func sharedHub(home string) hostpath.HostSurfaces {
	return hostpath.Shared(hostEnv(home))
}

// projectSurfaces resolves one host's project-scope surfaces. It cannot fail:
// the shared resolver is a pure switch with no environment and no validation.
func projectSurfaces(id, project string) hostpath.HostSurfaces {
	return hostpath.ProjectSurfaces(id, project)
}

// withoutDir drops one directory from a resolver read list. The resolver's
// write target is not always the list's first entry — gemini's is not, because
// the host ranks the shared hub above its own directory — so the "reads
// besides my own" set is a subtraction, never a `[1:]` slice.
func withoutDir(list []string, own string) []string {
	out := make([]string, 0, len(list))

	for _, dir := range list {
		if dir != own {
			out = append(out, dir)
		}
	}

	return out
}

// projectRel expresses a project-scope path the resolver names as the relative
// rel beadle's project surfaces take. The resolver works in absolute paths
// (it is handed a project root); beadle's surfaces are parameterised by that
// root so a test can point one at a temp dir, so the two shapes are bridged
// here rather than in every surface.
func projectRel(project, resolved string) string {
	if project == "" || resolved == "" {
		return ""
	}

	rel, err := filepath.Rel(project, resolved)
	if err != nil {
		return ""
	}

	return filepath.ToSlash(rel)
}
