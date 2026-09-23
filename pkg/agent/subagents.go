package agent

import (
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// subagentSurface presents the vault subagent canon to one host directory.
type subagentSurface = fileSurface[subagent.Document]

// subagentModel adapts the canonical subagent package to the file surface.
type subagentModel struct{}

func (subagentModel) parse(_ string, value []byte) (subagent.Document, error) {
	return subagent.Parse(value)
}

func (subagentModel) render(doc subagent.Document) []byte { return subagent.Render(doc) }

func (subagentModel) name(doc subagent.Document) string { return doc.Name }

func (subagentModel) validName(name string) bool { return subagent.ValidName(name) }
