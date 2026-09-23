package plugin

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	claudeDir        = ".claude"
	pluginsDir       = "plugins"
	cacheDir         = "cache"
	installedFile    = "installed_plugins.json"
	marketplacesFile = "known_marketplaces.json"
	orphanMarker     = ".orphaned_at"
	metaDir          = ".claude-plugin"
	metaFile         = "plugin.json"
	skillsDir        = "skills"
	skillFile        = "SKILL.md"
	// agentsDir holds the plugin's subagent definitions (Claude plugin
	// layout): beadle presents them to hosts that do not read plugins.
	agentsDir        = "agents"
	commandsDir      = "commands"
	hooksDir         = "hooks"
	hooksFile        = "hooks.json"
	mcpFile          = ".mcp.json"
	pluginRootMarker = "${CLAUDE_PLUGIN_ROOT}"
	missingDirNote   = "points to a missing directory"
)

type Manifest struct {
	Plugins      []Plugin
	Marketplaces []Marketplace
	Orphans      []Orphan
	Warnings     []string
}

type Plugin struct {
	Name           string
	Marketplace    string
	Version        string
	Scope          string
	InstallPath    string
	GitCommitSha   string
	Description    string
	Skills         []string
	Agents         []string
	Commands       []string
	Hooks          []string
	MCPServers     []string
	PluginRootRefs []string
}

type Marketplace struct {
	Name        string
	Repo        string
	InstallPath string
	AutoUpdate  bool
}

type Orphan struct {
	Marketplace string
	Name        string
	Version     string
	Path        string
	OrphanedAt  time.Time
}

type installRecord struct {
	Scope        string    `json:"scope"`
	InstallPath  string    `json:"installPath"`
	Version      string    `json:"version"`
	InstalledAt  time.Time `json:"installedAt"`
	LastUpdated  time.Time `json:"lastUpdated"`
	GitCommitSha string    `json:"gitCommitSha"`
}

type installedDocument struct {
	Plugins map[string][]installRecord `json:"plugins"`
}

type pluginMeta struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type marketplaceRecord struct {
	Source struct {
		Repo string `json:"repo"`
	} `json:"source"`
	InstallLocation string `json:"installLocation"`
	AutoUpdate      bool   `json:"autoUpdate"`
}

func Read(home string) (Manifest, error) {
	if home == "" {
		return Manifest{}, errors.New("empty home directory")
	}

	root := filepath.Join(home, claudeDir, pluginsDir)

	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, nil
		}

		return Manifest{}, fmt.Errorf("stat %s: %w", root, err)
	}

	var manifest Manifest

	listed, err := readInstalled(root, &manifest)
	if err != nil {
		return Manifest{}, err
	}

	readMarketplaces(root, &manifest)
	scanOrphans(root, listed, &manifest)

	slices.SortFunc(manifest.Plugins, func(a, b Plugin) int {
		return cmp.Or(
			cmp.Compare(a.Marketplace, b.Marketplace),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.Version, b.Version),
		)
	})

	return manifest, nil
}

func readInstalled(root string, manifest *Manifest) (map[string]struct{}, error) {
	listed := map[string]struct{}{}

	path := filepath.Join(root, installedFile)

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the caller-provided home directory
	if errors.Is(err, fs.ErrNotExist) {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("%s not found under %s", installedFile, root))

		return listed, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc installedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	for _, key := range slices.Sorted(maps.Keys(doc.Plugins)) {
		name, marketplace, ok := strings.Cut(key, "@")
		if !ok {
			manifest.Warnings = append(manifest.Warnings, "malformed plugin key "+key)

			continue
		}

		for _, record := range doc.Plugins[key] {
			if record.InstallPath != "" {
				listed[record.InstallPath] = struct{}{}
			}

			manifest.Plugins = append(manifest.Plugins, pluginFromRecord(name, marketplace, record, manifest))
		}
	}

	return listed, nil
}

func pluginFromRecord(name, marketplace string, record installRecord, manifest *Manifest) Plugin {
	p := Plugin{
		Name:         name,
		Marketplace:  marketplace,
		Version:      record.Version,
		Scope:        record.Scope,
		InstallPath:  record.InstallPath,
		GitCommitSha: record.GitCommitSha,
	}

	info, err := os.Stat(p.InstallPath)
	if p.InstallPath == "" || err != nil || !info.IsDir() {
		manifest.Warnings = append(manifest.Warnings,
			fmt.Sprintf("installed plugin %s@%s %s: %s", name, marketplace, missingDirNote, record.InstallPath))

		return p
	}

	if meta, ok := readMeta(p.InstallPath, manifest); ok {
		if p.Version == "" {
			p.Version = meta.Version
		}

		p.Description = meta.Description
	}

	p.Skills = scanSkills(p.InstallPath)
	p.Commands = scanCommands(p.InstallPath)
	p.Agents = scanAgents(p.InstallPath)
	p.Hooks = scanHooks(p.InstallPath)
	p.MCPServers = scanMCPServers(p.InstallPath)
	p.PluginRootRefs = scanRootRefs(p.InstallPath)

	return p
}

func readMeta(installPath string, manifest *Manifest) (pluginMeta, bool) {
	path := filepath.Join(installPath, metaDir, metaFile)

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the caller-provided home directory
	if errors.Is(err, fs.ErrNotExist) {
		return pluginMeta{}, false
	}

	if err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("read %s: %v", path, err))

		return pluginMeta{}, false
	}

	var meta pluginMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("parse %s: %v", path, err))

		return pluginMeta{}, false
	}

	return meta, true
}

func readMarketplaces(root string, manifest *Manifest) {
	path := filepath.Join(root, marketplacesFile)

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the caller-provided home directory
	if errors.Is(err, fs.ErrNotExist) {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("%s not found under %s", marketplacesFile, root))

		return
	}

	if err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("read %s: %v", path, err))

		return
	}

	var doc map[string]marketplaceRecord
	if err := json.Unmarshal(data, &doc); err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("parse %s: %v", path, err))

		return
	}

	for _, name := range slices.Sorted(maps.Keys(doc)) {
		record := doc[name]

		manifest.Marketplaces = append(manifest.Marketplaces, Marketplace{
			Name:        name,
			Repo:        record.Source.Repo,
			InstallPath: record.InstallLocation,
			AutoUpdate:  record.AutoUpdate,
		})
	}
}

func scanOrphans(root string, listed map[string]struct{}, manifest *Manifest) {
	cache := filepath.Join(root, cacheDir)

	for _, marketplace := range subdirs(cache) {
		marketplaceDir := filepath.Join(cache, marketplace)

		for _, name := range subdirs(marketplaceDir) {
			nameDir := filepath.Join(marketplaceDir, name)

			for _, version := range subdirs(nameDir) {
				dir := filepath.Join(nameDir, version)

				_, installed := listed[dir]

				marked := exists(filepath.Join(dir, orphanMarker))
				if installed && !marked {
					continue
				}

				manifest.Orphans = append(manifest.Orphans, Orphan{
					Marketplace: marketplace,
					Name:        name,
					Version:     version,
					Path:        dir,
					OrphanedAt:  orphanTime(dir, marked, manifest),
				})
			}
		}
	}

	slices.SortFunc(manifest.Orphans, func(a, b Orphan) int {
		return cmp.Or(
			cmp.Compare(a.Marketplace, b.Marketplace),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Version, b.Version),
		)
	})
}

func orphanTime(dir string, marked bool, manifest *Manifest) time.Time {
	if !marked {
		return time.Time{}
	}

	data, err := os.ReadFile(filepath.Join(dir, orphanMarker)) //nolint:gosec // G304: path is built from the caller-provided home directory
	if err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("cannot parse %s in %s", orphanMarker, dir))

		return time.Time{}
	}

	millis, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("cannot parse %s in %s", orphanMarker, dir))

		return time.Time{}
	}

	return time.UnixMilli(millis)
}

func scanSkills(installPath string) []string {
	dir := filepath.Join(installPath, skillsDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}

		if exists(filepath.Join(path, skillFile)) {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)

	return names
}

func scanAgents(installPath string) []string {
	entries, err := os.ReadDir(filepath.Join(installPath, agentsDir))
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}

	slices.Sort(names)

	return names
}

func scanCommands(installPath string) []string {
	entries, err := os.ReadDir(filepath.Join(installPath, commandsDir))
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		names = append(names, entry.Name())
	}

	slices.Sort(names)

	return names
}

func scanHooks(installPath string) []string {
	data, err := os.ReadFile(filepath.Join(installPath, hooksDir, hooksFile)) //nolint:gosec // G304: path is built from the caller-provided home directory
	if err != nil {
		return nil
	}

	var doc struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}

	return slices.Sorted(maps.Keys(doc.Hooks))
}

func scanMCPServers(installPath string) []string {
	data, err := os.ReadFile(filepath.Join(installPath, mcpFile)) //nolint:gosec // G304: path is built from the caller-provided home directory
	if err != nil {
		return nil
	}

	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}

	return slices.Sorted(maps.Keys(doc.MCPServers))
}

func scanRootRefs(installPath string) []string {
	var refs []string

	for _, rel := range []string{mcpFile, filepath.Join(hooksDir, hooksFile)} {
		data, err := os.ReadFile(filepath.Join(installPath, rel)) //nolint:gosec // G304: path is built from the caller-provided home directory
		if err != nil {
			continue
		}

		if strings.Contains(string(data), pluginRootMarker) {
			refs = append(refs, rel)
		}
	}

	return refs
}

func subdirs(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(path, entry.Name()))
		if err != nil || !info.IsDir() {
			continue
		}

		names = append(names, entry.Name())
	}

	slices.Sort(names)

	return names
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
