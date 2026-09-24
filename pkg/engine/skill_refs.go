package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// skillReferenceReason names why a reference is dangling.
type skillReferenceReason int

const (
	refNotDelivered skillReferenceReason = iota
	refNotInCanon
	refPluginMissing
)

// skillReference is one dangling skill reference: of one host, or of the
// whole canon when Agent is empty (a name missing from the canon).
type skillReference struct {
	Agent   string
	Skill   string // the delivered skill whose body carries the reference
	Missing string // the referenced skill name
	Plugin  string // the plugin namespace of a `/plugin:skill` reference
	Reason  skillReferenceReason
}

// message renders the diagnostic line for one finding.
func (r skillReference) message() string {
	display := r.Missing
	if r.Plugin != "" {
		display = "/" + r.Plugin + ":" + r.Missing
	}

	switch r.Reason {
	case refPluginMissing:
		return fmt.Sprintf("skill %s references %s but plugin %s is not installed for %s; the reference will fail there",
			r.Skill, display, r.Plugin, r.Agent)
	case refNotInCanon:
		return fmt.Sprintf("skill %s references skill %s which is not in the canon; add the skill or fix the reference",
			r.Skill, display)
	default:
		return fmt.Sprintf("skill %s references skill %s which is not delivered to %s; the reference will fail there",
			r.Skill, display, r.Agent)
	}
}

// skillDelivery is the per-host delivery verdict: the skill names the host
// can invoke and the plugin namespaces it has.
type skillDelivery struct {
	Names   map[string]string
	Plugins map[string]bool
}

// deliveredSkills returns the skill names one host can actually invoke: the
// canon names the channels it reads deliver (the served bundle, the own
// surface, the shared directory). A name the bundle skips as covered stays
// delivered when the copy covering it is a beadle channel the host reads (its
// own surface or the shared directory); a foreign or farm copy is not a beadle
// channel, so such a name is dropped.
func (e *Engine) deliveredSkills(a *agent.Agent, st *state.State, canon map[string]skill.Tree) skillDelivery {
	surface := a.Surface(kind.Skills)
	if surface == nil {
		return skillDelivery{}
	}

	ch := e.channelsFor(a, surface, st)
	delivery := skillDelivery{Names: map[string]string{}, Plugins: e.installedPlugins(st, a)}

	// The host reads a registered bundle even when its last probe failed, so
	// the read side asks bundleServesSkills rather than the write-side
	// ch.bundle. Only a bundle that actually carries skills counts: the
	// Gemini bundle delivers MCP servers only.
	bundleSkills := e.bundleServesSkills(st, a.ID)

	for name := range canon {
		switch {
		case bundleSkills:
			delivery.Names[name] = "bundle"
		case ch.own:
			delivery.Names[name] = "own"
		case ch.shared && e.readsSharedSkills(surface):
			delivery.Names[name] = "shared"
		}
	}

	if !bundleSkills {
		return delivery
	}

	vis := e.resolveSkillVisibility(a, surface, st, canon)
	caps, order := vis.Caps, vis.order()

	for name, res := range vis.Skills {
		if !res.suppressedBundle(caps, order, ch) {
			continue
		}

		if coveredByChannel(res.matchesOutside(classBundle)) {
			continue
		}

		delete(delivery.Names, name)
	}

	return delivery
}

// bundleCarriesSkills reports whether the agent's bundle delivers skills at
// all: the Gemini bundle carries MCP servers only.
func (e *Engine) bundleCarriesSkills(agentID string) bool {
	host, ok := bundleHostFor(agentID)
	if !ok {
		return false
	}

	return slices.Contains(host.ContentKinds(), kind.Skills)
}

// bundleServesSkills reports whether the host reads canon skills from its
// beadle bundle: the bundle carries skills and the host serves it. This is a
// read-side answer; channelsFor stays the write-side one, because a served
// but unverified bundle must never count as covering a name the render or the
// withdrawal would then drop.
func (e *Engine) bundleServesSkills(st *state.State, agentID string) bool {
	host, ok := bundleHostFor(agentID)
	if !ok || !e.bundleCarriesSkills(agentID) {
		return false
	}

	entry, known := st.Bundles[string(host)]

	return known && entry.Serves()
}

// coveredByChannel reports whether the copies covering a name outside the
// bundle are beadle channels the host reads: such a copy keeps delivering the
// name even though the bundle leaves it out.
func coveredByChannel(matches []provider) bool {
	return slices.ContainsFunc(matches, func(p provider) bool {
		return p.class == classOwn || p.class == classShared
	})
}

// installedPlugins lists the plugin namespaces the host has: the beadle
// bundle plugin it serves plus the reconciled farm plugins.
func (e *Engine) installedPlugins(st *state.State, a *agent.Agent) map[string]bool {
	out := map[string]bool{}

	if e.bundleServesSkills(st, a.ID) {
		out[bundle.PluginName] = true
	}

	ledger, _, _ := loadPluginLedger(e.vault.PluginsLedgerPath())

	for _, key := range pluginLedgerKeys(ledger) {
		if _, name, ok := strings.Cut(key, "/"); ok && name != "" {
			out[name] = true
		}
	}

	return out
}

// readsSharedSkills reports whether the host reads the shared skills
// directory the shared surface writes.
func (e *Engine) readsSharedSkills(surface agent.Surface) bool {
	area, ok := surface.(agent.SkillReadArea)
	if !ok {
		return false
	}

	return slices.Contains(area.ReadDirs(), e.sharedSkillsDir())
}

// danglingSkillReferences lists the references that fail: the names missing
// from the canon once for the whole canon, then the per-host findings.
func (e *Engine) danglingSkillReferences(st *state.State, active []*agent.Agent, canon map[string]skill.Tree) []skillReference {
	return append(canonSkillReferences(canon), e.hostSkillReferences(st, active, canon)...)
}

// canonSkillReferences lists the references to names missing from the canon.
// Such a reference fails on every host whatever the delivery state, so it is
// a property of the canon and carries no agent. A farm plugin's skills are
// outside the canon model (U-15 §2), so only bare and beadle-canon names count.
func canonSkillReferences(canon map[string]skill.Tree) []skillReference {
	var out []skillReference

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		for _, ref := range skillReferencesOf(canon[name]) {
			if !ref.Explicit && !plausibleUnquoted(ref, canon) {
				continue
			}

			if ref.Plugin != "" && ref.Plugin != bundle.PluginName {
				continue
			}

			if _, inCanon := canon[ref.Name]; inCanon {
				continue
			}

			out = append(out, skillReference{Skill: name, Missing: ref.Name, Plugin: ref.Plugin, Reason: refNotInCanon})
		}
	}

	return out
}

// hostSkillReferences lists the references of the skills each host can
// invoke that the same host cannot resolve: a canon name it is not delivered,
// or a plugin namespace it does not have. Names missing from the canon are
// canonSkillReferences' findings.
func (e *Engine) hostSkillReferences(st *state.State, active []*agent.Agent, canon map[string]skill.Tree) []skillReference {
	var out []skillReference

	for _, a := range active {
		delivery := e.deliveredSkills(a, st, canon)

		for _, name := range slices.Sorted(maps.Keys(delivery.Names)) {
			for _, ref := range skillReferencesOf(canon[name]) {
				if !ref.Explicit && !plausibleUnquoted(ref, canon) {
					continue
				}

				if ref.Plugin != "" {
					if !delivery.Plugins[ref.Plugin] {
						out = append(out, skillReference{
							Agent: a.ID, Skill: name, Missing: ref.Name, Plugin: ref.Plugin, Reason: refPluginMissing,
						})
					}

					// A farm plugin's skills are outside the canon model
					// (U-15 §2): an installed namespace is enough.
					if ref.Plugin != bundle.PluginName {
						continue
					}
				}

				if _, inCanon := canon[ref.Name]; !inCanon {
					continue
				}

				if _, ok := delivery.Names[ref.Name]; ok {
					continue
				}

				out = append(out, skillReference{
					Agent: a.ID, Skill: name, Missing: ref.Name, Plugin: ref.Plugin, Reason: refNotDelivered,
				})
			}
		}
	}

	return out
}

// plausibleUnquoted reports whether an unquoted candidate is skill-like: a
// canon name or a slug carrying a separator. Unquoted prose words like "care"
// never become diagnostics.
func plausibleUnquoted(ref skill.Reference, canon map[string]skill.Tree) bool {
	// A plugin-qualified reference is explicit by construction.
	if ref.Plugin != "" {
		return true
	}

	if _, ok := canon[ref.Name]; ok {
		return true
	}

	return strings.ContainsAny(ref.Name, "-_.")
}

// skillReferencesOf extracts the references of one canon skill tree.
func skillReferencesOf(tree skill.Tree) []skill.Reference {
	body, ok := tree[farmSkillFile]
	if !ok {
		return nil
	}

	return skill.References(body)
}

// skillReferenceIssues reports the dangling skill references: a name missing
// from the canon once, and a reference in a delivered skill body that the same
// host cannot resolve under that host.
func (e *Engine) skillReferenceIssues(st *state.State, active []*agent.Agent) []Issue {
	canon, err := e.skillCanon()
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Skills, Message: "skills: " + err.Error()}}
	}

	var issues []Issue

	for _, finding := range e.danglingSkillReferences(st, active, canon) {
		issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Agent: finding.Agent, Message: finding.message()})
	}

	return issues
}

// noteSkillReferences appends one warning per dangling reference on a full
// forward sync. A name missing from the canon is one canon finding by
// construction, so it prints once however many hosts get the skill.
func (e *Engine) noteSkillReferences(st *state.State, active []*agent.Agent, report *Report) {
	canon, err := e.skillCanon()
	if err != nil {
		report.Warnings = append(report.Warnings, "skills: "+err.Error())

		return
	}

	for _, finding := range e.danglingSkillReferences(st, active, canon) {
		report.Warnings = append(report.Warnings, finding.message())
	}
}

// noteSkillReferencesIfFull reports the dangling references only on a full
// forward sync: a partial or pull run does not deliver the whole canon.
func (e *Engine) noteSkillReferencesIfFull(st *state.State, active []*agent.Agent, report *Report, opts SyncOptions) {
	if !fullForwardSync(opts) {
		return
	}

	e.noteSkillReferences(st, active, report)
}

// skillCanon loads the canon skill trees.
func (e *Engine) skillCanon() (map[string]skill.Tree, error) {
	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return nil, err
	}

	return skill.Group(items), nil
}
