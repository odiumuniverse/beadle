package plugin

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// codexManifest is the union of the Codex manifest shapes: the portable root
// plugin.json and the legacy .codex-plugin/plugin.json compatibility overlay.
// Portable identity (name, version, description) is canonical; the skills
// field is honoured only for legacy packages, and the hooks declaration is
// resolved by ReadHooksFor.
type codexManifest struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Skills      stringList `json:"skills"`
}

// readCodex reads the installed Codex plugins: the install cache
// (~/.codex/plugins/cache/<marketplace>/<name>/<version>) and the plugins a
// personal marketplace (~/.agents/plugins/marketplace.json) points at when
// they are not cached yet.
func readCodex(home string) ([]Plugin, []string) {
	var (
		plugins []Plugin
		warns   []string
	)

	cache := filepath.Join(home, codexDir, pluginsDir, cacheDir)
	installed := map[string]struct{}{}

	for _, origin := range subdirs(cache) {
		for _, name := range subdirs(filepath.Join(cache, origin)) {
			chosen, choiceWarns := chooseCodexVersion(origin, name, codexVersions(cache, origin, name))

			warns = append(warns, choiceWarns...)

			if chosen.dir == "" {
				continue
			}

			plugin, ok, pluginWarns := codexCachePlugin(origin, name, chosen.version, chosen.dir)

			warns = append(warns, pluginWarns...)

			if !ok {
				continue
			}

			installed[origin+"/"+name] = struct{}{}

			plugins = append(plugins, plugin)
		}
	}

	marketplacePlugins, marketplaceWarns := readCodexMarketplace(home, installed)

	plugins = append(plugins, marketplacePlugins...)
	warns = append(warns, marketplaceWarns...)

	return plugins, warns
}

// codexVersion is one cached version directory of a Codex plugin.
type codexVersion struct {
	version  string
	dir      string
	orphaned bool
	manifest bool
}

func codexVersions(cache, origin, name string) []codexVersion {
	var versions []codexVersion

	for _, version := range subdirs(filepath.Join(cache, origin, name)) {
		dir := filepath.Join(cache, origin, name, version)

		versions = append(versions, codexVersion{
			version:  version,
			dir:      dir,
			orphaned: exists(filepath.Join(dir, orphanMarker)),
			manifest: codexManifestExists(dir),
		})
	}

	return versions
}

// chooseCodexVersion picks the active cached version of one Codex plugin. The
// Codex cache has no registry that would name the installed version, so the
// newest readable install wins: a manifest-bearing directory first, then the
// latest modification time, then the greatest version string. Marked
// (.orphaned_at) versions are ignored; when every version is orphaned the
// plugin is skipped with a warning, because guessing there could resurrect a
// plugin the host removed.
func chooseCodexVersion(origin, name string, versions []codexVersion) (codexVersion, []string) {
	if len(versions) == 0 {
		return codexVersion{}, nil
	}

	active := slices.DeleteFunc(slices.Clone(versions), func(v codexVersion) bool { return v.orphaned })
	if len(active) == 0 {
		return codexVersion{}, []string{warnf(SourceCodex, "plugin %s/%s: all %d cached version(s) are orphaned; skipped", origin, name, len(versions))}
	}

	slices.SortFunc(active, func(a, b codexVersion) int {
		return cmp.Or(
			cmp.Compare(featureRank(b.manifest), featureRank(a.manifest)),
			b.modTime().Compare(a.modTime()),
			strings.Compare(b.version, a.version),
		)
	})

	if len(versions) > 1 {
		names := make([]string, 0, len(versions))

		for _, version := range versions {
			names = append(names, version.version)
		}

		return active[0], []string{warnf(SourceCodex, "plugin %s/%s has %d cached versions (%s); using %s",
			origin, name, len(versions), strings.Join(slices.Sorted(slices.Values(names)), ", "), active[0].version)}
	}

	return active[0], nil
}

func featureRank(present bool) int {
	if present {
		return 1
	}

	return 0
}

func (v codexVersion) modTime() time.Time {
	info, err := os.Stat(v.dir)
	if err != nil {
		return time.Time{}
	}

	return info.ModTime()
}

// codexManifestExists reports whether a cached version directory holds a
// readable manifest.
func codexManifestExists(dir string) bool {
	return slices.ContainsFunc(codexManifestPaths(dir), isRegularFile)
}

func codexManifestPaths(dir string) []string {
	return []string{
		filepath.Join(dir, metaFile),
		filepath.Join(dir, codexMetaDir, metaFile),
		filepath.Join(dir, metaDir, metaFile),
	}
}

func codexCachePlugin(origin, name, version, dir string) (Plugin, bool, []string) {
	if !isDir(dir) {
		return Plugin{}, false, []string{warnf(SourceCodex, "plugin %s/%s@%s points to a missing directory: %s", origin, name, version, dir)}
	}

	manifest, state, warns := readCodexManifest(dir)
	if state == manifestMissing {
		return Plugin{}, false, append(warns, warnf(SourceCodex, "plugin %s/%s@%s has no plugin.json; skipped", origin, name, version))
	}

	if state != manifestParsed {
		return Plugin{}, false, warns
	}

	plugin := Plugin{
		Source:      SourceCodex,
		Origin:      origin,
		Name:        fallback(manifest.Name, name),
		Version:     fallback(manifest.Version, version),
		Description: manifest.Description,
		Scope:       scopeUser,
		InstallPath: dir,
	}

	plugin.fillArtifacts(SourceCodex, &warns)

	return plugin, true, warns
}

// readCodexMarketplace reads the personal marketplace and reports the local
// plugins it points at that are not installed into the cache yet.
func readCodexMarketplace(home string, installed map[string]struct{}) ([]Plugin, []string) {
	path := filepath.Join(home, agentsHomeDir, pluginsDir, marketplaceFile)

	var doc struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name    string          `json:"name"`
			Version string          `json:"version"`
			Source  json.RawMessage `json:"source"`
		} `json:"plugins"`
	}

	state, warns := readJSONManifest(path, SourceCodex, &doc)

	if state != manifestParsed {
		return nil, warns
	}

	origin := fallback(doc.Name, SourceCodex)

	var plugins []Plugin

	seen := map[string]bool{}

	for _, entry := range doc.Plugins {
		key := origin + "/" + entry.Name

		if _, cached := installed[key]; cached {
			continue
		}

		rel, ok := localMarketplaceSource(entry.Source)
		if !ok || entry.Name == "" || seen[key] {
			continue
		}

		dir, ok := localPath(home, rel)
		if !ok {
			warns = append(warns, warnf(SourceCodex, "marketplace %s: plugin %s source path %q escapes the home directory; ignored", origin, entry.Name, rel))

			continue
		}

		if !isDir(dir) {
			warns = append(warns, warnf(SourceCodex, "marketplace %s: plugin %s points to a missing directory: %s", origin, entry.Name, dir))

			continue
		}

		manifest, state, manifestWarns := readCodexManifest(dir)

		warns = append(warns, manifestWarns...)

		if state == manifestMissing {
			warns = append(warns, warnf(SourceCodex, "marketplace %s: plugin %s has no plugin.json; skipped", origin, entry.Name))

			continue
		}

		if state != manifestParsed {
			continue
		}

		plugin := Plugin{
			Source:      SourceCodex,
			Origin:      origin,
			Name:        fallback(entry.Name, fallback(manifest.Name, filepath.Base(dir))),
			Version:     fallback(entry.Version, manifest.Version),
			Description: manifest.Description,
			Scope:       scopeUser,
			InstallPath: dir,
		}

		plugin.fillArtifacts(SourceCodex, &warns)

		seen[key] = true

		plugins = append(plugins, plugin)
	}

	return plugins, warns
}

// localMarketplaceSource reports whether a marketplace entry points at a
// local directory and returns its plugin-relative path.
func localMarketplaceSource(raw json.RawMessage) (string, bool) {
	var plain string

	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain, plain != ""
	}

	var typed struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	}

	if err := json.Unmarshal(raw, &typed); err != nil {
		return "", false
	}

	if typed.Source != localSourceName || typed.Path == "" {
		return "", false
	}

	return typed.Path, true
}

const localSourceName = "local"

// fillArtifacts resolves the plugin payload into the unified model fields.
func (p *Plugin) fillArtifacts(source string, warns *[]string) {
	artifacts, artifactWarns := resolveArtifacts(source, p.InstallPath)

	*warns = append(*warns, artifactWarns...)

	p.Skills = namesOf(artifacts.Skills)
	p.Agents = namesOf(artifacts.Agents)
	p.Commands = namesOf(artifacts.Commands)
	p.MCPServers = artifacts.MCP
	p.Hooks = artifacts.Hooks
}

// readCodexManifest reads the Codex manifest candidate of one plugin
// directory: the portable root plugin.json wins, then the legacy
// .codex-plugin/plugin.json, then a Claude-compatible .claude-plugin one.
func readCodexManifest(dir string) (codexManifest, manifestState, []string) {
	candidates := codexManifestPaths(dir)

	for i, path := range candidates {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}

		var manifest codexManifest

		state, warns := readJSONManifest(path, SourceCodex, &manifest)
		if state != manifestParsed {
			return codexManifest{}, state, warns
		}

		if i == 0 {
			// Portable packages discover skills in skills/ and hooks in the
			// OpenAI extension; the legacy skill declaration does not apply.
			manifest.Skills = nil
		}

		return manifest, manifestParsed, warns
	}

	return codexManifest{}, manifestMissing, nil
}

// codexArtifacts resolves the Codex plugin payload: skills/ (or the legacy
// manifest skills path), agents/ and commands/ of Claude-compatible packages,
// hooks, and the portable or legacy MCP document.
func codexArtifacts(installPath string) (pluginArtifacts, []string) {
	manifest, state, warns := readCodexManifest(installPath)

	if state == manifestParsed && len(manifest.Skills) > 0 {
		return codexManifestArtifacts(installPath, manifest, &warns)
	}

	out := pluginArtifacts{
		Skills:   scanSkillDirs(filepath.Join(installPath, skillsDir)),
		Agents:   scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false),
		Commands: scanNamedFiles(filepath.Join(installPath, commandsDir), []string{markdownExt}, false),
	}

	out.Hooks, out.MCP = codexHookAndMCPNames(installPath, &warns)

	return out, warns
}

func codexManifestArtifacts(installPath string, manifest codexManifest, warns *[]string) (pluginArtifacts, []string) {
	out := pluginArtifacts{
		Skills:   manifestSkillPaths(installPath, SourceCodex, manifest.Skills, warns),
		Agents:   scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false),
		Commands: scanNamedFiles(filepath.Join(installPath, commandsDir), []string{markdownExt}, false),
	}

	out.Hooks, out.MCP = codexHookAndMCPNames(installPath, warns)

	return out, *warns
}

// codexHookAndMCPNames reports the hook event names and the MCP server names
// of a Codex plugin.
func codexHookAndMCPNames(installPath string, warns *[]string) ([]string, []string) {
	hooks, hookWarns, err := ReadHooksFor(SourceCodex, installPath)
	if err != nil {
		*warns = append(*warns, warnf(SourceCodex, "plugin %s: %v", installPath, err))
	} else {
		*warns = append(*warns, hookWarns...)
	}

	mcp, mcpWarns := resolveMCPNames(SourceCodex, installPath)
	*warns = append(*warns, mcpWarns...)

	return hookEventNames(hooks), mcp
}
