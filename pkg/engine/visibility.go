package engine

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// providerClass names the channel a readable skill copy belongs to.
type providerClass string

const (
	classOwn     providerClass = "own"
	classShared  providerClass = "shared"
	classFarm    providerClass = "farm"
	classForeign providerClass = "foreign"
	classBundle  providerClass = "bundle"
)

// provider is one readable skill copy with the traits the visibility decision
// needs; the adapter fills it so the decision itself stays pure.
type provider struct {
	class  providerClass
	name   string
	dir    string // the read directory the copy lives in
	path   string // the copy path shown in reports
	file   string // the flat `<name>.md` path when the copy is a flat file
	root   string // the skill tree this copy is; ownership is keyed by it
	digest cas.Hash
	// independent is the verdict every consumer reads: does this copy mean
	// somebody else delivers the element, or is it beadle's own delivery?
	// resolveVisibility sets it from the ownership it is handed, so the farm
	// render, a withdrawal and `explain` share one meaning. A root with no
	// recorded ownership is independent, which is the safe default: a copy
	// beadle never wrote really does deliver the element.
	independent bool
}

// ownCopyUnchanged reports whether the copy at root is still exactly what the
// base records: every file beadle wrote is present with its recorded hash, and
// the base knows no file the copy no longer has. It is the comparison a
// withdrawal already makes, so "beadle still owns this" cannot come to mean two
// different things in two different places.
func ownCopyUnchanged(root string, base state.Base, name string) bool {
	tree, err := skill.ReadTree(root)
	if err != nil {
		// Unreadable is not "unchanged": a copy nobody can read is no evidence
		// that beadle's delivery is still intact.
		return false
	}

	files := make([]string, 0, len(tree))
	snapshot := make(kind.Items, len(tree))

	for rel, content := range tree {
		key := name + "/" + rel
		files = append(files, key)
		snapshot[key] = content
	}

	slices.Sort(files)

	return snapshotMatchesBase(snapshot, base, kind.Skills, name, files)
}

// ownership is what beadle itself delivered: the skill tree root it wrote, and
// the digest of what it wrote there. It is the answer to "is this still the copy
// beadle delivered?", passed in rather than read, so the decision stays a pure
// function of (disk, ownership) and never touches the state itself.
//
// A root that is absent is a root beadle never wrote, and its copy is somebody
// else's. That default is the safe one in both directions: a copy beadle did
// not write really does deliver the element, and a copy beadle wrote is only
// beadle's while the bytes are still the bytes it delivered.
type ownership map[string]cas.Hash

// delivered reports whether root holds exactly what beadle delivered there. A
// copy the user has edited since no longer matches its digest, and an edited
// copy is theirs: `your edits are yours` is the whole contract, so the edit
// hands the copy back to being an independent provider.
func (o ownership) delivered(root string, digest cas.Hash) bool {
	want, ok := o[root]

	return ok && want == digest
}

// resolution is the per-name verdict of the scan: which copies the host can
// read, which of them the canon digest matches, which drift, and the beadle
// channel that would deliver the name.
type resolution struct {
	Name      string
	Digest    cas.Hash
	Providers []provider
	Matches   []provider
	Forks     []provider
	Chosen    providerClass
}

// visibility is the per-host result of the skill copy scan.
type visibility struct {
	Caps     agent.SkillCaps
	Channels channels
	Order    []string
	Skills   map[string]resolution
	Warnings []string
}

// channels tells which beadle delivery channels are enabled for one host at
// call time; unlike agent.SkillCaps this is runtime state, not host traits.
type channels struct {
	bundle bool
	own    bool
	shared bool
}

// chosen reports the channel that delivers the canon to the host, if any.
func (c channels) chosen() providerClass {
	switch {
	case c.bundle:
		return classBundle
	case c.own:
		return classOwn
	case c.shared:
		return classShared
	default:
		return ""
	}
}

// coverage is the projection of a visibility the bundle path consumes: the
// canon names file copies already deliver, so the bundle can leave them out.
type coverage struct {
	Skills   map[string]coveredSkill
	Warnings []string
}

// coveredSkill is one canon skill name a copy outside the bundle already
// delivers into the host's read area.
type coveredSkill struct {
	Name     string
	Provider string
	Digest   cas.Hash
	// Class is the provider's class, and the render needs it: a copy another
	// tool made in the host's read area delivers the skill whoever owns the
	// kind, while beadle's own copy there is only a delivery while beadle is
	// the one writing that directory.
	Class providerClass
}

// resolveVisibility decides, for every canon skill name, which copies the
// host can read and whether a copy outside the bundle already delivers it.
// It is pure: every filesystem read happens in the adapter.
func resolveVisibility(canon map[string]skill.Tree, providers []provider, caps agent.SkillCaps, ch channels, own ownership) visibility {
	vis := visibility{Caps: caps, Channels: ch, Skills: make(map[string]resolution, len(canon)), Order: providerOrder(providers)}

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		res := resolution{Name: name, Digest: skill.TreeDigest(canon[name])}

		for _, p := range providers {
			if p.name != name {
				continue
			}

			p.independent = !own.delivered(p.root, p.digest)
			res.Providers = append(res.Providers, p)

			switch {
			case p.digest == res.Digest:
				res.Matches = append(res.Matches, p)
			case p.class == classBundle:
				// A stale render is reported by the bundle freshness check,
				// not as a fork of the canon.
			default:
				res.Forks = append(res.Forks, p)
			}
		}

		order := vis.order()

		slices.SortFunc(res.Providers, compareProviders(order))
		res.Chosen = ch.chosen()

		if forks := res.visibleForks(caps, order); len(forks) > 0 {
			vis.Warnings = append(vis.Warnings, forkWarning(name, forks))
		}

		vis.Skills[name] = res
	}

	return vis
}

// order is the read order the caps declare, or the adapter scan order.
func (v visibility) order() []string {
	if len(v.Caps.ReadOrder) > 0 {
		return v.Caps.ReadOrder
	}

	return v.Order
}

// bundleCoverage projects the visibility for the bundle path: a name is
// covered when a copy outside the bundle already delivers the canon.
func (v visibility) bundleCoverage() coverage {
	cov := coverage{Skills: map[string]coveredSkill{}, Warnings: slices.Clone(v.Warnings)}
	order := v.order()

	for _, name := range slices.Sorted(maps.Keys(v.Skills)) {
		res := v.Skills[name]
		if !res.suppressedBundle(v.Caps, order, v.Channels) {
			continue
		}

		matches := res.independentProviders()
		if len(matches) == 0 {
			continue
		}

		cov.Skills[name] = coveredSkill{Name: name, Provider: matches[0].path, Digest: res.Digest, Class: matches[0].class}
	}

	return cov
}

// foreignCoverage lists the canon names a foreign copy already delivers for
// the host: beadle's writable copy can be released without losing the skill.
func (v visibility) foreignCoverage() map[string]provider {
	out := map[string]provider{}
	order := v.order()

	for _, name := range slices.Sorted(maps.Keys(v.Skills)) {
		for _, p := range v.Skills[name].visible(v.Caps, order) {
			if p.class != classForeign || p.digest != v.Skills[name].Digest {
				continue
			}

			out[name] = p

			break
		}
	}

	return out
}

// visible returns the copies the host actually shows. Without shadowing every
// copy is visible; with shadowing only the highest-precedence file copy wins,
// and namespaced bundle copies stay visible in addition. An un-namespaced
// bundle copy is visible while no file copy shadows it: a host whose only
// content is the bundle still shows the name.
func (r resolution) visible(caps agent.SkillCaps, order []string) []provider {
	if !caps.Shadowing {
		return r.Providers
	}

	var (
		winner  *provider
		bundles []provider
		out     []provider
	)

	for _, p := range r.Providers {
		if p.class == classBundle {
			if caps.NamespacedBundle {
				out = append(out, p)
			} else {
				bundles = append(bundles, p)
			}

			continue
		}

		if winner == nil || rankIn(order, p.dir) < rankIn(order, winner.dir) {
			candidate := p
			winner = &candidate
		}
	}

	switch {
	case winner != nil:
		out = append(out, *winner)
	case !caps.NamespacedBundle:
		out = append(out, bundles...)
	}

	return out
}

// visibleForks lists the visible copies that drift from the canon. A stale
// bundle render is not a fork: the freshness check reports it.
func (r resolution) visibleForks(caps agent.SkillCaps, order []string) []provider {
	var out []provider

	for _, p := range r.visible(caps, order) {
		if p.class == classBundle {
			continue
		}

		if p.digest != r.Digest {
			out = append(out, p)
		}
	}

	return out
}

// suppressedBundle reports that file copies already deliver the name, so the
// bundle may leave it out. A namespaced bundle is visible next to the file
// copies; without namespacing only the visible winner counts (a shadowed
// duplicate would not reach the user). A beadle-writable copy on the host's
// own surface covers the name only while the bundle channel is live: enable
// withdraws such a copy, so covering the bundle with it would drop the name
// and then delete the copy.
func (r resolution) suppressedBundle(caps agent.SkillCaps, order []string, ch channels) bool {
	matches := r.independentProviders()
	if len(matches) == 0 {
		return false
	}

	if !ch.bundle && !slices.ContainsFunc(matches, func(p provider) bool { return p.class != classOwn }) {
		return false
	}

	if caps.NamespacedBundle || !caps.Shadowing {
		return true
	}

	for _, p := range r.visible(caps, order) {
		if p.class == classBundle {
			continue
		}

		return p.digest == r.Digest
	}

	return false
}

// independentProviders lists the matching copies that deliver the element
// without the bundle: everything outside the bundle except a copy beadle itself
// delivered and nobody has edited since.
//
// This is the single meaning of "covered outside the bundle". The farm render,
// a withdrawal and `explain` all read it, so they cannot drift apart on a
// phrase that decides whether a skill is duplicated or dropped.
func (r resolution) independentProviders() []provider {
	out := make([]provider, 0, len(r.Matches))

	for _, p := range r.Matches {
		if p.class != classBundle && p.independent {
			out = append(out, p)
		}
	}

	return out
}

// compareProviders sorts the copies by the host read order, then by channel
// and path, so every report is deterministic.
func compareProviders(order []string) func(a, b provider) int {
	return func(a, b provider) int {
		return cmp.Or(
			cmp.Compare(rankIn(order, a.dir), rankIn(order, b.dir)),
			cmp.Compare(a.class, b.class),
			cmp.Compare(a.path, b.path),
		)
	}
}

// providerOrder records the directory order of the scan, so hosts without a
// declared read order still rank their own directory first.
func providerOrder(providers []provider) []string {
	var dirs []string

	for _, p := range providers {
		if !slices.Contains(dirs, p.dir) {
			dirs = append(dirs, p.dir)
		}
	}

	return dirs
}

// rankIn returns the precedence rank of a directory; undeclared directories
// rank after every declared one.
func rankIn(order []string, dir string) int {
	if i := slices.Index(order, dir); i >= 0 {
		return i
	}

	return len(order)
}

// forkWarning renders one name's divergence: a single warning with every
// drifting copy, never one warning per copy.
func forkWarning(name string, forks []provider) string {
	paths := make([]string, 0, len(forks))
	for _, p := range forks {
		paths = append(paths, p.path)
	}

	return fmt.Sprintf("skills: %s differs between the canon and %s; the canon copy stays", name, strings.Join(paths, ", "))
}

// visibilityForHost resolves the canon skills against the copies the bundle
// host can read. A nil st means the caller has no run in progress and the state
// is read from disk; a sync passes the state it is holding, because the copies
// it is about to judge include the ones this same run delivered.
func (e *Engine) visibilityForHost(host bundle.Host, st *state.State) visibility {
	if !slices.Contains(host.ContentKinds(), kind.Skills) {
		return visibility{}
	}

	a := agent.ByID(e.agents, host.AgentID())
	if a == nil {
		return visibility{}
	}

	return e.visibilityForAgent(a, st)
}

// visibilityForAgent resolves the canon skills against every copy the agent can
// read. It reads only; a scan error fails open with a warning.
//
// The state it consults is the one the caller holds. Re-reading the saved file
// here is what made a run unable to see its own deliveries: the base for a copy
// the sync had just written is in memory, while the file still holds the base
// from before it - so the first render after `init` judged beadle's own copy as
// somebody else's, dropped the skill from the farm, and the run after put it
// back. That is the "the vault moved on a re-run" the gate keeps reporting.
func (e *Engine) visibilityForAgent(a *agent.Agent, held *state.State) visibility {
	surface := a.Surface(kind.Skills)
	if surface == nil {
		return visibility{}
	}

	st := held
	if st == nil {
		loaded, err := state.Load(e.vault.StatePath())
		if err != nil {
			return visibility{Warnings: []string{"skills: cannot read the state for coverage: " + err.Error()}}
		}

		st = loaded
	}

	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return visibility{Warnings: []string{"skills: cannot read the canon for coverage: " + err.Error()}}
	}

	return e.resolveSkillVisibility(a, surface, st, skill.Group(items))
}

// resolveSkillVisibility is the adapter part: it reads the skill copies the
// agent can see, classifies them, and hands the decision to the pure resolver.
func (e *Engine) resolveSkillVisibility(a *agent.Agent, surface agent.Surface, st *state.State, canon map[string]skill.Tree) visibility {
	providers, own, warns := e.skillProviders(a, surface, st)

	host, bundleHost := bundleHostFor(a.ID)

	if bundleHost {
		bundleProviders, bundleWarns := e.bundleSkillProviders(host, st, canon)
		providers = append(providers, bundleProviders...)
		warns = append(warns, bundleWarns...)
	}

	vis := resolveVisibility(canon, providers, skillCaps(surface), e.channelsFor(a, surface, st), own)

	if area, ok := surface.(agent.SkillReadArea); ok {
		vis.Order = area.ReadDirs()
	}

	vis.Warnings = append(warns, vis.Warnings...)

	return vis
}

// skillProviders reads every skill copy the agent can see and classifies it.
// skillProviders reads every skill copy the agent can see and classifies it.
// It also returns the ownership those copies imply: the roots where what is on
// disk is still exactly what beadle delivered. That is the one place with both
// halves of the question — the tree it just read and the base recorded for that
// directory — so it is the only place that answers it.
func (e *Engine) skillProviders(a *agent.Agent, surface agent.Surface, st *state.State) ([]provider, ownership, []string) {
	own := ownership{}

	reader, ok := surface.(agent.SkillReader)
	if !ok {
		return nil, own, nil
	}

	refs, err := reader.ReadableSkills()
	if err != nil {
		return nil, own, []string{fmt.Sprintf("skills: cannot scan the %s read area: %v", a.ID, err)}
	}

	bases := e.skillSurfaceBases(st)
	shared := e.sharedSkillRoots(a)
	plugins := filepath.Clean(e.vault.PluginsDir())
	ownDir := surface.Path()

	var (
		providers []provider
		warns     []string
	)

	for _, ref := range refs {
		digest, err := e.skillTreeDigest(st, ref.Root)
		if err != nil {
			warns = append(warns, fmt.Sprintf("skills: cannot read %s: %v; the canon copy stays", ref.Root, err))

			continue
		}

		// Ownership is recorded, not inferred later: a base holds content
		// hashes while a tree digest is taken over contents, so the two can
		// only be compared here, where the tree is in hand. The recorded value
		// is the digest of the copy as it stands, and it is recorded only when
		// the copy is still what beadle delivered - an edited copy is the
		// user's, and `your edits are yours` is the whole contract.
		if base := bases[ref.Dir]; len(base) > 0 && ownCopyUnchanged(ref.Root, base, ref.Name) {
			own[ref.Root] = digest
		}

		link, symlink := skillLinkTarget(ref.Dir, ref.Name, ref.File)

		path := filepath.Join(ref.Dir, ref.Name)
		if ref.File != "" {
			path = ref.File
		}

		providers = append(providers, provider{
			class:  classifySkillCopy(ref, ownDir, shared, link, symlink, plugins, bases),
			name:   ref.Name,
			dir:    ref.Dir,
			path:   path,
			file:   ref.File,
			digest: digest,
		})
	}

	return providers, own, warns
}

// classifySkillCopy places one readable copy on the delivery map: a farm link
// (raw target or resolved root under the plugin farm), the own writable real
// directory recorded in the base, a shared root; everything else is foreign —
// user symlinks, unmanaged trees, other agents' directories.
func classifySkillCopy(ref agent.SkillRef, ownDir string, shared []string, link string, symlink bool, plugins string, bases map[string]state.Base) providerClass {
	switch {
	case symlink && (underDir(filepath.Clean(link), plugins) || underDir(ref.Root, plugins)):
		return classFarm
	case ref.Dir == ownDir && !symlink && baseOwns(bases[ref.Dir], kind.Skills, ref.Name):
		return classOwn
	case slices.Contains(shared, ref.Dir):
		return classShared
	default:
		return classForeign
	}
}

// bundleSkillProviders lists the skills the rendered bundle currently holds:
// the explain path shows them as an extra channel; the resolution itself
// never lets a bundle copy cover itself.
func (e *Engine) bundleSkillProviders(host bundle.Host, st *state.State, canon map[string]skill.Tree) ([]provider, []string) {
	entry, ok := st.Bundles[string(host)]
	if !ok || !entry.Enabled || !slices.Contains(host.ContentKinds(), kind.Skills) {
		return nil, nil
	}

	root := filepath.Join(e.vault.BundlesDir(), string(host))
	if host == bundle.Claude {
		root = filepath.Join(root, "plugins", bundle.PluginName)
	}

	root = filepath.Join(root, "skills")

	var (
		providers []provider
		warns     []string
	)

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		dir := filepath.Join(root, name)
		if !skill.HasRoot(dir) {
			continue
		}

		digest, err := e.skillTreeDigest(st, dir)
		if err != nil {
			warns = append(warns, fmt.Sprintf("skills: cannot read the rendered bundle skill %s: %v", dir, err))

			continue
		}

		providers = append(providers, provider{class: classBundle, name: name, dir: root, path: dir, digest: digest})
	}

	return providers, warns
}

// channelsFor derives the enabled beadle channels from the config and state.
func (e *Engine) channelsFor(a *agent.Agent, surface agent.Surface, st *state.State) channels {
	ch := channels{}

	if e.config.KindEnabled(kind.Skills) {
		ch.own = e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode).Pushes()
	}

	if host, ok := bundleHostFor(a.ID); ok {
		entry, known := st.Bundles[string(host)]
		ch.bundle = known && entry.Enabled && entry.Verified()
	}

	ch.shared = e.sharedChannelEnabled()

	return ch
}

// sharedChannelEnabled reports whether the shared surface writes the canon:
// it is a delivery channel for every host that reads ~/.agents/skills.
func (e *Engine) sharedChannelEnabled() bool {
	if !e.config.KindEnabled(kind.Skills) || !e.config.Agents[agent.SharedID].Enabled {
		return false
	}

	shared := agent.ByID(e.agents, agent.SharedID)
	if shared == nil {
		return false
	}

	surface := shared.Surface(kind.Skills)
	if surface == nil {
		return false
	}

	return e.config.ModeFor(agent.SharedID, kind.Skills, surface.Traits().DefaultMode).Pushes()
}

// skillCaps reads the visibility caps a skills surface declares.
func skillCaps(surface agent.Surface) agent.SkillCaps {
	caps, ok := surface.(agent.SkillCapsSurface)
	if !ok {
		return agent.SkillCaps{}
	}

	return caps.SkillCaps()
}

// bundleHostFor maps an agent to its native bundle host.
func bundleHostFor(agentID string) (bundle.Host, bool) {
	for _, host := range bundle.Hosts() {
		if host.AgentID() == agentID {
			return host, true
		}
	}

	return "", false
}

// sharedSkillRoots lists the read roots that count as shared for one agent:
// beadle's shared skills surface plus the DSH agents-home root, which DSH
// reads at rank 500 ($DSH_AGENTS_HOME or ~/.agents). A copy in a shared root
// is never a foreign deliverer — it cannot release the agent's own write —
// and it is never reported as a foreign duplicate.
func (e *Engine) sharedSkillRoots(a *agent.Agent) []string {
	var roots []string

	if shared := e.sharedSkillsDir(); shared != "" {
		roots = append(roots, shared)
	}

	if a != nil && a.ID == agent.DSHID {
		if root := agent.DSHSharedSkillsDir(e.home); root != "" {
			roots = append(roots, root)
		}
	}

	return roots
}

// foreignReadDirs lists the read directories that can hold a copy foreign to
// the agent: its own directory cannot (one entry per name) and a shared root
// is beadle's own channel.
func (e *Engine) foreignReadDirs(a *agent.Agent, surface agent.Surface) []string {
	area, ok := surface.(agent.SkillReadArea)
	if !ok {
		return nil
	}

	own := surface.Path()
	shared := e.sharedSkillRoots(a)

	var out []string

	for _, dir := range area.ReadDirs() {
		if dir == own || slices.Contains(shared, dir) {
			continue
		}

		out = append(out, dir)
	}

	return out
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

// skillLinkTarget returns the raw symlink target resolved against its
// directory, and whether the entry is a symlink at all. file overrides the
// entry path for a flat copy.
func skillLinkTarget(dir, name, file string) (string, bool) {
	path := filepath.Join(dir, name)
	if file != "" {
		path = file
	}

	link, err := os.Readlink(path)
	if err != nil {
		return "", false
	}

	if !filepath.IsAbs(link) {
		link = filepath.Join(dir, link)
	}

	return link, true
}

// filterCovered drops the skills a copy outside the bundle already delivers:
// the bundle is a patch for the deficit, not a mirror of the canon.
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
// the rendered elements plus the copies that deliver the rest.
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

// bundleRenderRequest is the render-time request: a skill another host already
// delivers stays with that host, and the disable path keeps the unfiltered
// canon so a covered name can still be materialized back.
//
// One copy is deliberately not credited: the one inside the host's own skills
// surface. A bundle that owns the skills kind delivers them, so a copy sitting
// in the host's skills directory — whether it is the one beadle wrote and the
// withdrawal kept, or a leftover from before the kind was switched off — is not
// somebody else delivering the skill. Crediting it made the rendered farm a
// function of which files happened to be on disk: the first sync rendered with
// the skill, the next one found the copy and rendered without it, and the vault
// moved on a re-run. The render is now a function of the canon and of what
// other hosts hold, which is the same question on the enable path and on every
// refresh after it.
//
// The coverage that comes back is the real one, unadjusted, because the
// ownsSkills is the caller's answer to "does this host's bundle deliver the
// skills". It is a parameter rather than something read from the config because
// the two callers know it at different moments and the config only agrees with
// one of them: on the enable path the kinds have not been switched off yet, and
// they will be by the end of the same call.
func (e *Engine) bundleRenderRequest(host bundle.Host, st *state.State, ownsSkills bool) (bundle.Request, coverage, []string, []string, error) {
	req, notes, warns, err := e.bundleRequest(host)
	if err != nil {
		return req, coverage{}, notes, warns, err
	}

	cov := e.visibilityForHost(host, st).bundleCoverage()
	if ownsSkills {
		cov = e.withoutFileSurface(host, cov)
	}

	return filterCovered(req, cov), cov, notes, warns, nil
}

// withoutFileSurface drops the coverage the host's own skills directory
// provides, and it is only asked for when the bundle delivers those skills
// instead of beadle writing that directory.
//
// While the file surface is the delivery path, a copy in it is exactly what it
// looks like - beadle's own, or a foreign tool's symlink - and it delivers the
// skill, so the bundle must not duplicate it. Once the bundle owns the kind
// that directory is no longer a delivery path: the copy there is either the one
// beadle wrote and the withdrawal kept, or a leftover from before the kind was
// switched off, and crediting it made the rendered farm a function of which
// files happened to be on disk. The first sync rendered with the skill, the next
// found the copy and rendered without it, and the vault moved on a re-run.
func (e *Engine) withoutFileSurface(host bundle.Host, cov coverage) coverage {
	a := agent.ByID(e.agents, host.AgentID())
	if a == nil {
		return cov
	}

	surface := a.Surface(kind.Skills)
	if surface == nil {
		return cov
	}

	root := filepath.Clean(surface.Path())
	out := coverage{Skills: make(map[string]coveredSkill, len(cov.Skills)), Warnings: cov.Warnings}

	for name, skill := range cov.Skills {
		// Another tool's copy in the read area is left alone: Claude reads that
		// directory whoever owns the kind, so the skill really is delivered and
		// duplicating it in the bundle would be waste. Only beadle's own copy
		// stops counting, and only because beadle is no longer the one writing
		// that directory - a copy nothing manages is not a delivery.
		if skill.Class == classOwn && isUnder(filepath.Clean(skill.Provider), root) {
			continue
		}

		out.Skills[name] = skill
	}

	return out
}

// isUnder reports whether path is root or lies inside it. Both are expected
// cleaned; a provider outside the host's surface never matches, which is the
// answer we want for every other host's copy.
func isUnder(path, root string) bool {
	if path == root {
		return true
	}

	return fsutil.Under(root, path)
}
