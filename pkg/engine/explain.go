package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// Explanation is one visibility row of `beadle explain`: a copy a host can
// read, its channel, and its role in delivering the canon.
type Explanation struct {
	Host    string `json:"host"`
	Channel string `json:"channel"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Role    string `json:"role"`
}

// Explain resolves one canon skill against every active host and reports the
// copies each host can read.
func (e *Engine) Explain(ctx context.Context, name string) ([]Explanation, error) {
	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return nil, err
	}

	canon := skill.Group(items)

	tree, ok := canon[name]
	if !ok {
		return nil, fmt.Errorf("skill %s is not in the canon", name)
	}

	digest := skill.TreeDigest(tree)

	rows := []Explanation{{
		Host: "canon", Channel: "canon",
		Path:   filepath.Join(e.vault.SkillsDir(), name),
		Digest: shortDigest(digest), Role: "source",
	}}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range active {
		if a.Surface(kind.Skills) == nil {
			continue
		}

		rows = append(rows, explainHost(a.ID, e.visibilityForAgent(a), name, digest, e.home)...)
	}

	return rows, nil
}

// explainHost renders the copies one host can read, with the role each copy
// plays in delivering the canon.
func explainHost(host string, vis visibility, name string, canonDigest cas.Hash, home string) []Explanation {
	res, ok := vis.Skills[name]
	if !ok {
		return nil
	}

	caps, order := vis.Caps, vis.order()
	visible := res.visible(caps, order)
	suppressed := res.suppressedBundle(caps, order, vis.Channels)

	var rows []Explanation

	for _, p := range res.Providers {
		rows = append(rows, Explanation{
			Host:    host,
			Channel: string(p.class),
			Path:    displayHomePath(p.path, home),
			Digest:  shortDigest(p.digest),
			Role:    providerRole(p, res, visible, suppressed, canonDigest),
		})
	}

	return rows
}

// providerRole names the part one copy plays: the copy that delivers the
// canon, a redundant duplicate, or a fork the canon cannot follow.
func providerRole(p provider, res resolution, visible []provider, suppressed bool, canonDigest cas.Hash) string {
	if p.digest != canonDigest {
		return "fork"
	}

	if !slices.ContainsFunc(visible, func(q provider) bool { return q.path == p.path }) {
		return "loser"
	}

	// The chosen channel's copy delivers when the host shows it; every other
	// matching copy is a duplicate next to it.
	if slices.ContainsFunc(res.Matches, func(q provider) bool { return q.class == res.Chosen && visibleCopy(visible, q) }) {
		if p.class == res.Chosen {
			return "winner"
		}

		return "loser"
	}

	switch {
	case suppressed && p.class != classBundle:
		return "winner" // a file copy delivers; the bundle copy is the duplicate
	case res.Chosen == "":
		return "winner" // nothing beadle-side writes; the visible copy delivers
	default:
		return "loser"
	}
}

// visibleCopy reports that the copy is among the visible ones.
func visibleCopy(visible []provider, p provider) bool {
	return slices.ContainsFunc(visible, func(q provider) bool { return q.path == p.path })
}

// shortDigest trims a content hash for the explain table.
func shortDigest(hash cas.Hash) string {
	if len(hash) <= 12 {
		return string(hash)
	}

	return string(hash)[:12]
}
