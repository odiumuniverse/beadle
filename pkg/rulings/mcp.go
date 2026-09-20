package rulings

import (
	"strings"

	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func commandDiffers(a, b mcp.Server) bool {
	if len(a.Command) != len(b.Command) {
		return true
	}

	for i := range a.Command {
		if a.Command[i] != b.Command[i] {
			return true
		}
	}

	return false
}

func urlDiffers(a, b mcp.Server) bool {
	return !strings.EqualFold(a.URL, b.URL)
}
