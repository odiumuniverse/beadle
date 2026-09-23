package agent

import (
	"github.com/odiumuniverse/beadle/pkg/command"
)

// commandLabel names the command kind in diagnostics.
const commandLabel = "command"

// commandSurface presents the vault command canon to one host directory.
type commandSurface = fileSurface[command.Document]
