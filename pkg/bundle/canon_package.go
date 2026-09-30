package bundle

import (
	"github.com/odiumuniverse/beadle/pkg/agentplugins"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// CanonPackageName is the package name the canon renders under. It is the same
// name `beadle export agent-plugins` has always used, so the two produce one
// package rather than two dialects.
const CanonPackageName = "beadle-canon"

// CanonPackage is the canon rendered as one portable Agent Plugins package:
// the same renderer the export path uses, no second dialect.
type CanonPackage struct {
	Files    agentplugins.Files
	Version  string
	Warnings []string
	Skills   agentplugins.Counts
	MCP      agentplugins.Counts
}

// CanonPackageInput is everything the canon package is rendered from.
type CanonPackageInput struct {
	Skills   map[string]skill.Tree
	Servers  mcp.Servers
	Hooks    map[string]hooks.Hook
	Approved map[string]bool
}

// canonVersion is the version placeholder the content hash is computed over.
// The manifest is rendered with it, hashed, and re-rendered with the result, so
// the version describes the package and not the process that produced it.
const canonVersion = "0.0.0-placeholder"

// canonHooks turns the canon's hook map into the package's hook list. Only an
// approved hook travels: a package is installed by a machine that has not been
// at this keyboard, and beadle never exports a command the user did not
// approve here.
func canonHooks(in CanonPackageInput) []agentplugins.Hook {
	var out []agentplugins.Hook

	for key, hook := range in.Hooks {
		out = append(out, agentplugins.Hook{
			Event:    hook.Event,
			Matcher:  hook.Matcher,
			Command:  hook.Command,
			Timeout:  hook.Timeout,
			Approved: in.Approved[key],
		})
	}

	return out
}

// RenderCanonPackage renders the canon as one package with a
// content-addressed version. The version is derived from the rendered bytes,
// so the same canon always produces the same version and any change to any
// file produces a different one.
func RenderCanonPackage(in CanonPackageInput) (CanonPackage, error) {
	opts := agentplugins.Options{
		Name:        CanonPackageName,
		Version:     canonVersion,
		Description: "The beadle canon: portable skills, MCP servers and approved hooks",
		License:     "MIT",
		Keywords:    []string{"beadle", "skills", "mcp"},
		Hooks:       canonHooks(in),
	}

	// The hash is taken over the content with the placeholder version, then
	// the manifest is stamped with the result. Hashing the stamped package
	// would be circular.
	stamped, err := agentplugins.Render(in.Skills, in.Servers, opts)
	if err != nil {
		return CanonPackage{}, err
	}

	version := agentplugins.ContentVersion(stamped.Files)

	opts.Version = version

	pkg, err := agentplugins.Render(in.Skills, in.Servers, opts)
	if err != nil {
		return CanonPackage{}, err
	}

	return CanonPackage{
		Files:    pkg.Files,
		Version:  version,
		Warnings: pkg.Warnings,
		Skills:   pkg.Skills,
		MCP:      pkg.MCP,
	}, nil
}

// CanonServers reads the canon's MCP servers for the package renderer. The
// package carries `{secret:NAME}` references whole, never their values.
func CanonServers(data []byte) (mcp.Servers, error) {
	if len(data) == 0 {
		return mcp.Servers{}, nil
	}

	return mcp.ParseCanonical(data)
}
