package cli

import (
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/kind"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// The documents a machine reads. `engine.Report` is the engine's own working
// shape: seventeen fields, most of them optional, so its JSON differs per run
// and per command, and it will keep changing as the engine changes. What a
// command prints is a typed document instead: a struct that belongs to the
// contract, with the envelope in front, and fields that are only ever added
// within a major.
//
// The projection is explicit on purpose. A field beadle stops filling must be
// removed here deliberately, with a major bump, rather than disappear because
// the engine stopped setting it.

// syncDocument is what `beadle sync --json` prints (and what `pull`/`push`
// print, distinguished by `direction`).
type syncDocument struct {
	withSchema

	DryRun    bool   `json:"dry_run"`
	Direction string `json:"direction"`
	// NoActiveAgents says the vault has no host agent on it, which is why the
	// kinds below read `skipped` rather than `in sync`: a script that only sees
	// per-kind statuses would otherwise have to guess the run-level reason.
	NoActiveAgents bool               `json:"no_active_agents,omitempty"`
	Kinds          []syncKindDocument `json:"kinds"`
	Conflicts      []state.Conflict   `json:"conflicts"`
	// Plugins is the section `beadle sync` prints for the vault's package spec.
	// The key is the word the user types (`beadle plugins`); "package" stays
	// the word for what the spec names.
	Plugins   *state.PackagesReport `json:"plugins"`
	Bundles   []syncBundle          `json:"bundles"`
	Rulings   syncRulings           `json:"rulings"`
	Adoptions []syncAdoption        `json:"adoptions"`
	Notes     []string              `json:"notes"`
	Warnings  []string              `json:"warnings"`
}

type syncKindDocument struct {
	Kind string `json:"kind"`
	// Status is the word a human reads on this kind's line — delivered, in
	// sync or skipped — so a script and a person are told the same thing about
	// one run. Detail says why when the word is not `delivered`.
	Status       string            `json:"status"`
	Detail       string            `json:"detail,omitempty"`
	VaultChanged bool              `json:"vault_changed"`
	Agents       []syncAgentResult `json:"agents"`
	Warnings     []string          `json:"warnings"`
	Error        string            `json:"error,omitempty"`
	Rulings      syncRulings       `json:"rulings"`
}

type syncAgentResult struct {
	Agent   string   `json:"agent"`
	State   string   `json:"state"`
	Changed []string `json:"changed"`
	Note    string   `json:"note,omitempty"`
}

type syncBundle struct {
	Host       string `json:"host"`
	State      string `json:"state"`
	Version    string `json:"version,omitempty"`
	Registered bool   `json:"registered,omitempty"`
	Note       string `json:"note,omitempty"`
}

type syncRulings struct {
	Applied   []syncRuling `json:"applied"`
	Suggested []syncRuling `json:"suggested"`
	Demoted   []syncRuling `json:"demoted"`
}

type syncRuling struct {
	Kind      string `json:"kind"`
	Agent     string `json:"agent"`
	Key       string `json:"key"`
	Ruling    string `json:"ruling,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type syncAdoption struct {
	Key      string `json:"key"`
	Agent    string `json:"agent"`
	State    string `json:"state"`
	Provider string `json:"provider,omitempty"`
	Note     string `json:"note,omitempty"`
}

// adoptionWord is the §1 word for what an adoption did.
func adoptionWord(action string) string {
	switch action {
	case "adopted", "moved":
		return wordDelivered
	case "would-adopt", "would-move":
		return wordSkipped
	default:
		return action
	}
}

func newSyncDocument(report *engine.Report, direction config.Mode) syncDocument {
	doc := syncDocument{
		withSchema:     newEnvelope("beadle.sync"),
		DryRun:         report.DryRun,
		Direction:      directionWord(direction),
		NoActiveAgents: report.NoActiveAgents,
		Kinds:          []syncKindDocument{},
		Conflicts:      []state.Conflict{},
		Plugins:        report.Packages,
		Bundles:        []syncBundle{},
		Adoptions:      []syncAdoption{},
		Notes:          []string{},
		Warnings:       []string{},
	}

	if report.Conflicts != nil {
		doc.Conflicts = report.Conflicts
	}

	if report.Notes != nil {
		doc.Notes = report.Notes
	}

	if report.Warnings != nil {
		doc.Warnings = report.Warnings
	}

	for _, kr := range report.Kinds {
		// The same word the human table prints, and the reason when the word
		// is not "delivered": a script that reads the document has to be able
		// to tell "wrote nothing, nothing to write" from "wrote nothing,
		// could not write", which is the whole of the finding.
		status, detail := kindStatus(kr, report.NoActiveAgents)

		doc.Kinds = append(doc.Kinds, syncKindDocument{
			Kind:         string(kr.Kind),
			Status:       status,
			Detail:       detail,
			VaultChanged: kr.VaultChanged,
			Agents:       agentResults(kr),
			Warnings:     kr.Warnings,
			Error:        kr.Err,
			Rulings: syncRulings{
				Applied:   rulingRows(kr.RulingsApplied),
				Suggested: rulingRows(kr.RulingSuggestions),
			},
		})
	}

	for _, bundle := range report.Bundles {
		doc.Bundles = append(doc.Bundles, syncBundle{
			Host:       bundle.Host,
			State:      bundleWord(bundle.Action),
			Version:    bundle.Version,
			Registered: bundle.Registered,
			Note:       bundle.Note,
		})
	}

	doc.Rulings = syncRulings{
		Applied:   rulingRows(report.RulingsApplied),
		Suggested: rulingRows(report.RulingSuggestions),
		Demoted:   rulingRows(report.RulingsDemoted),
	}

	for _, adoption := range report.Adoptions {
		doc.Adoptions = append(doc.Adoptions, syncAdoption{
			Key:      adoption.Name,
			Agent:    adoption.Agent,
			State:    adoptionWord(adoption.Action),
			Provider: adoption.Provider,
			Note:     adoption.Note,
		})
	}

	return doc
}

func agentResults(kr engine.KindReport) []syncAgentResult {
	out := []syncAgentResult{}

	for _, result := range kr.Agents {
		out = append(out, syncAgentResult{
			Agent:   result.Agent,
			State:   actionWord(result.Action),
			Changed: changedKeys(result.Changes),
			Note:    result.Note,
		})
	}

	return out
}

func changedKeys(changes []engine.ItemChange) []string {
	out := []string{}

	for _, change := range changes {
		out = append(out, change.Key)
	}

	return out
}

func rulingRows(events []engine.RulingEvent) []syncRuling {
	out := []syncRuling{}

	for _, event := range events {
		out = append(out, syncRuling{
			Kind:      string(event.Kind),
			Agent:     event.Agent,
			Key:       event.Key,
			Ruling:    event.Ruling,
			Signature: strings.Join([]string{string(event.Signature.Kind), event.Signature.Target, event.Signature.Divergence, event.Signature.Scope}, "/"),
		})
	}

	return out
}

// actionWord is the §1 vocabulary for one agent's outcome, so the document and
// the human table say the same thing about the same event.
func actionWord(action engine.Action) string {
	switch action {
	case engine.ActionPushed, engine.ActionWouldPush:
		return wordDelivered
	case engine.ActionSkipped, engine.ActionAlias, engine.ActionNoop, engine.ActionPullOnly:
		return wordSkipped
	case engine.ActionError:
		return wordFailed
	default:
		return string(action)
	}
}

func directionWord(direction config.Mode) string {
	switch direction {
	case config.ModePull:
		return "pull"
	case config.ModePush:
		return "push"
	default:
		return "sync"
	}
}

// pluginsDocument is what `beadle plugins list --json` prints. The library
// document is copied field by field, so a change in the library's struct is not
// a change in beadle's contract.
type pluginsDocument struct {
	withSchema

	Home     string           `json:"home,omitempty"`
	Packages []pluginsPackage `json:"packages"`
	Cells    []verger.Cell    `json:"cells"`
}

type pluginsPackage struct {
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
}

func newPluginsDocument(doc *verger.StatusDocument) pluginsDocument {
	out := pluginsDocument{
		withSchema: newEnvelope("beadle.plugins"),
		Packages:   []pluginsPackage{},
		Cells:      []verger.Cell{},
	}

	if doc == nil {
		return out
	}

	out.Home = doc.Home
	out.Cells = doc.Cells

	seen := map[string]string{}

	for _, cell := range doc.Cells {
		if _, ok := seen[cell.Package]; !ok {
			seen[cell.Package] = beadleWord(cell)
			out.Packages = append(out.Packages, pluginsPackage{
				Package: cell.Package,
				Version: cell.Version,
				State:   beadleWord(cell),
			})
		}
	}

	return out
}

// bundlesDocument is what `beadle bundles --json` prints.
type bundlesDocument struct {
	withSchema

	Bundles []syncBundle `json:"bundles"`
}

func newBundlesDocument(results []engine.BundleResult) bundlesDocument {
	doc := bundlesDocument{
		withSchema: newEnvelope("beadle.bundles"),
		Bundles:    []syncBundle{},
	}

	for _, bundle := range results {
		doc.Bundles = append(doc.Bundles, syncBundle{
			Host:       bundle.Host,
			State:      bundleWord(bundle.Action),
			Version:    bundle.Version,
			Registered: bundle.Registered,
			Note:       bundle.Note,
		})
	}

	return doc
}

// bundleStates projects the recorded bundle state into the document's rows.
// The state is beadle's own record, not the engine's report, so the projection
// is a read of one struct rather than a copy of a working type.
func bundleStates(st *state.State) []engine.BundleResult {
	out := []engine.BundleResult{}

	for _, host := range bundle.Hosts() {
		entry := st.Bundles[string(host)]

		action := "disabled"
		if entry.Enabled {
			action = "enabled"
		}

		if !entry.Verified() {
			action = "pending"
		}

		out = append(out, engine.BundleResult{
			Host:       string(host),
			Action:     action,
			Version:    entry.Version,
			Registered: entry.Registered,
			Tier:       entry.VerifyTier,
		})
	}

	return out
}

// agentsDocument is what `beadle agents --json` prints.
type agentsDocument struct {
	withSchema
	Agents []agentRow   `json:"agents"`
	Kinds  []agentsKind `json:"kinds"`
}

// agentRow is one agent in a document: which agents, whether each is on, and
// the mode each kind resolves to. `beadle status` and `beadle agents` both
// publish exactly this, so there is one row type for them — two identical
// structs under two names is a change to one of them waiting to happen.
type agentRow struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`
	Installed bool              `json:"installed"`
	Modes     map[string]string `json:"modes"`
}

// agentRows is the projection both agent documents are built from. A host that
// cannot be probed is reported as not installed rather than failing the whole
// document, and a kind the host lists twice keeps its first mode.
func agentRows(cfg *config.Config, agents []*agent.Agent) []agentRow {
	rows := make([]agentRow, 0, len(agents))

	for _, ag := range agents {
		detected, err := ag.Detect()
		if err != nil {
			detected = false
		}

		row := agentRow{
			ID:        ag.ID,
			State:     "off",
			Installed: detected,
			Modes:     map[string]string{},
		}

		if cfg.Agents[ag.ID].Enabled {
			row.State = "on"
		}

		for _, surface := range ag.Surfaces {
			if _, seen := row.Modes[string(surface.Kind())]; seen {
				continue
			}

			row.Modes[string(surface.Kind())] = string(
				effectiveMode(cfg, ag, surface.Kind(), surface.Traits().DefaultMode))
		}

		rows = append(rows, row)
	}

	return rows
}

type agentsKind struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// historyDocument is what `beadle history <kind> --json` prints.
type historyDocument struct {
	withSchema

	Kind      string            `json:"kind"`
	Snapshots []historySnapshot `json:"snapshots"`
}

type historySnapshot struct {
	At       string `json:"at"`
	Manifest string `json:"manifest"`
}

// conflictsDocument is what `beadle conflicts --json` prints.
type conflictsDocument struct {
	withSchema

	Conflicts []engine.ConflictView `json:"conflicts"`
}

// newAgentsDocument projects the agent table into its document.
func newAgentsDocument(cfg *config.Config, agents []*agent.Agent) agentsDocument {
	doc := agentsDocument{
		withSchema: newEnvelope("beadle.agents"),
		Agents:     []agentRow{},
		Kinds:      []agentsKind{},
	}

	doc.Agents = agentRows(cfg, agents)

	for _, spec := range kind.All() {
		state := "off"
		if cfg.KindEnabled(spec.ID) {
			state = "on"
		}

		doc.Kinds = append(doc.Kinds, agentsKind{ID: string(spec.ID), State: state})
	}

	return doc
}

// newHistoryDocument projects one kind's snapshots into its document.
func newHistoryDocument(k kind.ID, history []state.Snapshot) historyDocument {
	doc := historyDocument{
		withSchema: newEnvelope("beadle.history"),
		Kind:       string(k),
		Snapshots:  []historySnapshot{},
	}

	for _, snap := range history {
		doc.Snapshots = append(doc.Snapshots, historySnapshot{
			At:       snap.At.UTC().Format(time.RFC3339),
			Manifest: string(snap.Manifest),
		})
	}

	return doc
}
