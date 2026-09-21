package engine

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// coveredSkill is one canon skill name a foreign copy already delivers into
// a host's read area.
type coveredSkill struct {
	Name     string
	Provider string
	Digest   cas.Hash
}

// coverage is the per-host result of the foreign copy scan.
type coverage struct {
	Skills   map[string]coveredSkill
	Warnings []string
}

// skillCopy is one readable skill copy with the traits the coverage decision
// needs; the adapter fills it so the decision itself stays pure.
type skillCopy struct {
	name     string
	provider string
	tree     skill.Tree
	symlink  bool
	owned    bool
	farm     bool
	shared   bool
}

// foreign reports whether beadle does not deliver the copy itself: a
// read-only symlink or an unmanaged directory, outside the shared surface
// and the plugin farm. A symlink stays foreign even when the base records
// the name: beadle adopted its bytes but cannot write it.
func (c skillCopy) foreign() bool {
	if c.farm || c.shared {
		return false
	}

	return c.symlink || !c.owned
}

// coverageOf compares the foreign copies against the canon: a copy covers a
// name when the tree digest matches. A mismatch is a fork: the canon copy
// stays and the caller warns.
func coverageOf(canon map[string]skill.Tree, copies []skillCopy) coverage {
	cov := coverage{Skills: map[string]coveredSkill{}}

	digests := make(map[string]cas.Hash, len(canon))
	for name, tree := range canon {
		digests[name] = skill.TreeDigest(tree)
	}

	slices.SortFunc(copies, func(a, b skillCopy) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.provider, b.provider))
	})

	for _, copy := range copies {
		if !copy.foreign() {
			continue
		}

		digest, ok := digests[copy.name]
		if !ok {
			continue
		}

		if skill.TreeDigest(copy.tree) != digest {
			cov.Warnings = append(cov.Warnings, fmt.Sprintf(
				"skills: %s differs between the canon and %s; the canon copy stays", copy.name, copy.provider))

			continue
		}

		if _, taken := cov.Skills[copy.name]; taken {
			continue
		}

		cov.Skills[copy.name] = coveredSkill{Name: copy.name, Provider: copy.provider, Digest: digest}
	}

	return cov
}

// coverageForHost scans the skill copies the bundle host can read and reports
// the canon skills a foreign copy already delivers. It reads only; a scan
// error fails open with a warning, never a silent suppression.
func (e *Engine) coverageForHost(host bundle.Host) coverage {
	cov := coverage{Skills: map[string]coveredSkill{}}

	if !slices.Contains(host.ContentKinds(), kind.Skills) {
		return cov
	}

	a := agent.ByID(e.agents, host.AgentID())
	if a == nil {
		return cov
	}

	surface := a.Surface(kind.Skills)
	if surface == nil {
		return cov
	}

	reader, ok := surface.(agent.SkillReader)
	if !ok {
		return cov
	}

	refs, err := reader.ReadableSkills()
	if err != nil {
		cov.Warnings = append(cov.Warnings, fmt.Sprintf("skills: cannot scan the %s read area: %v", host, err))

		return cov
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		cov.Warnings = append(cov.Warnings, "skills: cannot read the state for coverage: "+err.Error())

		return cov
	}

	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		cov.Warnings = append(cov.Warnings, "skills: cannot read the canon for coverage: "+err.Error())

		return cov
	}

	copies, warns := e.skillCopies(refs, st)

	result := coverageOf(skill.Group(items), copies)
	result.Warnings = append(warns, result.Warnings...)

	return result
}

// skillCopies reads every ref and classifies it for the coverage decision.
func (e *Engine) skillCopies(refs []agent.SkillRef, st *state.State) ([]skillCopy, []string) {
	bases := e.skillSurfaceBases(st)
	shared := e.sharedSkillsDir()
	plugins := filepath.Clean(e.vault.PluginsDir())

	var (
		copies []skillCopy
		warns  []string
	)

	for _, ref := range refs {
		tree, err := skill.ReadTree(ref.Root)
		if err != nil {
			warns = append(warns, fmt.Sprintf("skills: cannot read %s: %v; the canon copy stays", ref.Root, err))

			continue
		}

		link, symlink := skillLinkTarget(ref.Dir, ref.Name)

		copies = append(copies, skillCopy{
			name:     ref.Name,
			provider: filepath.Join(ref.Dir, ref.Name),
			tree:     tree,
			symlink:  symlink,
			owned:    baseOwns(bases[ref.Dir], kind.Skills, ref.Name),
			farm:     symlink && (underDir(filepath.Clean(link), plugins) || underDir(ref.Root, plugins)),
			shared:   ref.Dir == shared,
		})
	}

	return copies, warns
}

// skillLinkTarget returns the raw symlink target resolved against its
// directory, and whether the entry is a symlink at all.
func skillLinkTarget(dir, name string) (string, bool) {
	link, err := os.Readlink(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}

	if !filepath.IsAbs(link) {
		link = filepath.Join(dir, link)
	}

	return link, true
}

// skillSurfaceBases maps every configured skills directory to the base of
// the agent that owns it, so an alsoReads copy is judged by its own surface.
func (e *Engine) skillSurfaceBases(st *state.State) map[string]state.Base {
	out := make(map[string]state.Base, len(e.agents))

	for _, a := range e.agents {
		surface := a.Surface(kind.Skills)
		if surface == nil {
			continue
		}

		base, _ := st.Base(kind.Skills, a.ID)
		out[surface.Path()] = base
	}

	return out
}

// sharedSkillsDir is the shared skills surface path, empty when the surface
// is not configured.
func (e *Engine) sharedSkillsDir() string {
	a := agent.ByID(e.agents, agent.SharedID)
	if a == nil {
		return ""
	}

	surface := a.Surface(kind.Skills)
	if surface == nil {
		return ""
	}

	return surface.Path()
}

// filterCovered drops the skills a foreign copy already delivers: the bundle
// is a patch for the deficit, not a mirror of the canon.
func filterCovered(req bundle.Request, cov coverage) bundle.Request {
	if len(cov.Skills) == 0 {
		return req
	}

	req.Skills = maps.Clone(req.Skills)

	for name := range cov.Skills {
		delete(req.Skills, name)
	}

	return req
}

// withdrawalDelivered is the element set the host is considered to serve:
// the rendered elements plus the foreign copies that cover the rest.
func withdrawalDelivered(k kind.ID, req bundle.Request, cov coverage) map[string]struct{} {
	delivered := requestGroups(k, req)
	if k != kind.Skills {
		return delivered
	}

	for name := range cov.Skills {
		delivered[name] = struct{}{}
	}

	return delivered
}

// bundleRenderRequest is the render-time request: covered skills stay with
// their foreign copies. The disable path keeps the unfiltered canon, so a
// covered name can still be materialized back.
func (e *Engine) bundleRenderRequest(host bundle.Host) (bundle.Request, coverage, []string, error) {
	req, warns, err := e.bundleRequest(host)
	if err != nil {
		return req, coverage{}, warns, err
	}

	cov := e.coverageForHost(host)

	return filterCovered(req, cov), cov, warns, nil
}
