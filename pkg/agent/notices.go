package agent

import (
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// Notice is one host-expressiveness diagnostic for a canonical document.
type Notice struct {
	Agent   string
	Message string
}

// Notices reports what the agent cannot express from a canonical document of
// one kind: unmappable tools, unexpressible placeholders, lossy fields. The
// doctor reports them.
func (a *Agent) Notices(k kind.ID, key string, value []byte) []Notice {
	var out []Notice

	for _, surface := range a.SurfacesOf(k) {
		report, ok := surface.(noticer)
		if !ok {
			continue
		}

		for _, notice := range report.notices(key, value) {
			notice.Agent = a.ID

			out = append(out, notice)
		}
	}

	return out
}
