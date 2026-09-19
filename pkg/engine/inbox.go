package engine

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/inbox"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

type InboxAction string

const (
	// InboxCreated marks a memory note transcribed from an inbox line.
	InboxCreated InboxAction = "created"
	// InboxSkipped marks a line that already has a note or cannot be used.
	InboxSkipped InboxAction = "skipped"
	// InboxWouldCreate is the dry-run preview of InboxCreated.
	InboxWouldCreate InboxAction = "would-create"
)

// InboxResult reports one inbox line handled by the fan-in.
type InboxResult struct {
	Agent  string      `json:"agent"`
	Hash   string      `json:"hash"`
	Note   string      `json:"note"`
	Action InboxAction `json:"action"`
}

func (e *Engine) fanInInbox(
	ctx context.Context, spec kind.Spec, agents []*agent.Agent, items kind.Items, report *KindReport, opts SyncOptions,
) {
	if spec.ID != kind.Memory || opts.Direction == config.ModePush || e.home == "" {
		return
	}

	slug, ok := e.activeProjectSlug(agents)
	if !ok {
		e.log.Print(ctx, "inbox fan-in skipped: no active project for the current directory")

		return
	}

	consumed := inboxHashes(items)

	sorted := slices.Clone(agents)

	slices.SortFunc(sorted, func(a, b *agent.Agent) int {
		return strings.Compare(a.ID, b.ID)
	})

	for _, a := range sorted {
		if !e.config.ModeFor(a.ID, kind.Memory, config.ModeSync).Pulls() {
			continue
		}

		e.consumeInbox(ctx, a, slug, items, consumed, report, opts)
	}
}

func (e *Engine) activeProjectSlug(agents []*agent.Agent) (string, bool) {
	for _, target := range e.projectTargets(agents) {
		if target.active() {
			return target.slug, true
		}
	}

	return "", false
}

func (e *Engine) consumeInbox(
	ctx context.Context, a *agent.Agent, slug string, items kind.Items, consumed map[string]struct{}, report *KindReport, opts SyncOptions,
) {
	path := agent.InboxPath(e.home, a.ID)
	if path == "" {
		return
	}

	lines, err := inbox.Read(path)
	if err != nil {
		report.Warnings = append(report.Warnings, "inbox: "+err.Error())

		return
	}

	for _, line := range lines {
		if _, done := consumed[line.Hash]; done {
			report.Inbox = append(report.Inbox, InboxResult{Agent: a.ID, Hash: line.Hash, Note: consumedNote(items, line.Hash), Action: InboxSkipped})

			continue
		}

		consumed[line.Hash] = struct{}{}

		key, data := inbox.Note(slug, a.ID, line)
		if opts.DryRun {
			report.Inbox = append(report.Inbox, InboxResult{Agent: a.ID, Hash: line.Hash, Note: key, Action: InboxWouldCreate})

			continue
		}

		guarded, names, _, err := secret.ExtractText(data, e.secrets)
		if err != nil {
			e.log.Error(ctx, "inbox secret scan failed", "agent", a.ID, "hash", line.Hash, "err", err)

			guarded = data
		}

		if len(names) > 0 {
			e.log.Print(ctx, "inbox line secrets moved to the vault store", "agent", a.ID, "count", len(names))
		}

		if !validInboxNote(guarded) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("inbox: %s line %s skipped: empty note body", a.ID, line.Hash))
			report.Inbox = append(report.Inbox, InboxResult{Agent: a.ID, Hash: line.Hash, Note: key, Action: InboxSkipped})

			continue
		}

		items[key] = guarded
		report.Inbox = append(report.Inbox, InboxResult{Agent: a.ID, Hash: line.Hash, Note: key, Action: InboxCreated})
	}
}

func inboxHashes(items kind.Items) map[string]struct{} {
	consumed := map[string]struct{}{}

	for key := range items {
		_, note, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}

		hash, ok := strings.CutPrefix(note, "inbox-")
		if !ok {
			continue
		}

		if hash, ok = strings.CutSuffix(hash, ".md"); !ok || len(hash) != 8 {
			continue
		}

		consumed[hash] = struct{}{}
	}

	return consumed
}

func consumedNote(items kind.Items, hash string) string {
	name := "inbox-" + hash + ".md"

	for _, key := range slices.Sorted(maps.Keys(items)) {
		if _, note, ok := strings.Cut(key, "/"); ok && note == name {
			return key
		}
	}

	return name
}

func validInboxNote(data []byte) bool {
	_, body, ok := bytes.Cut(data, []byte("\n---\n"))
	if !ok {
		return false
	}

	body, _, ok = bytes.Cut(body, []byte("\n\n<!-- transcribed: "))

	return ok && len(bytes.TrimSpace(body)) > 0
}
