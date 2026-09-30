package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

const pluginLedgerVersion = 1

// Paths the old farm owned. They are the plugin library's own paths now (the
// registry, the marketplaces, the cache), plus the two directories the farm
// created inside the vault: the rendered host-format copies and the quarantine
// area. The one-time migration and the id rename still have to name them, and
// the vault's writer must never present them as marketplaces.
const (
	pluginRegistryPath   = ".claude/plugins/installed_plugins.json"
	pluginMarketplaceDir = ".claude/plugins/marketplaces"
	pluginCacheDir       = ".claude/plugins/cache"
	// farmRenderDirName holds the rendered host-format copies of plugin
	// definitions: <vault>/plugins/farm/<marketplace>/<plugin>/<kind>/<ns>.<ext>.
	farmRenderDirName = "farm"
	quarantineDirName = "quarantine"
)

// hostPluginLedger reads what the host plugin registries hold and presents it
// in the shape the farm's ledger used to have, so the rules written against
// that shape - which host owns a plugin, where it is installed, which servers
// beadle presented for it - keep one meaning. A key installed by several hosts
// appears once, from the first source in host order, and the sources that lost
// that conflict are recorded as overridden.
//
// The one part of the vault's ledger that is still beadle's own is the set of
// server names it presented per plugin: that is not derivable from a registry
// (it includes the canon-rendered names) and an upgrade must not drop a name
// the user has already seen in a host's config.
func (e *Engine) hostPluginLedger() pluginLedger {
	ledger := emptyPluginLedger()

	if stored, _, err := loadPluginLedger(e.vault.PluginsLedgerPath()); err == nil {
		for key, rec := range stored.Plugins {
			if len(rec.Servers) == 0 {
				continue
			}

			ledger.Plugins[key] = pluginLedgerRec{Servers: rec.Servers}
		}
	}

	if e.home == "" {
		return ledger
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return ledger
	}

	groups, _, _ := groupPlugins(manifest.Plugins)

	for _, group := range groups {
		key := pluginKey(group.Origin, group.Name)

		chosen := chooseRecord(group.Plugins)

		rec := ledger.Plugins[key]
		rec.Version = chosen.Version
		rec.Sha = chosen.GitCommitSha
		rec.Target = chosen.InstallPath
		rec.Source = chosen.Source
		rec.Overridden = group.Overridden

		ledger.Plugins[key] = rec
	}

	return ledger
}

// farmKindDir names the payload directory a plugin declares for one kind.
func farmKindDir(source, installPath string, k kind.ID) string {
	skills, agents, commands := plugin.ArtifactDirs(source, installPath)

	switch k {
	case kind.Subagents:
		return agents
	case kind.Commands:
		return commands
	default:
		return skills
	}
}

// samePath compares two paths after resolving them, so a symlinked or relative
// spelling of the same directory is recognised as the same place.
func samePath(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)

	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}

	return resolvedA == resolvedB
}

// The ledger is the farm's own record of what it installed and where it put
// it. The farm is gone; the one-time migration into the plugin library still
// reads it - and rewrites its keys when agent ids are renamed - so the reader
// stays, and with it the writer that rename needs. Nothing else in beadle
// delivers plugins, and nothing else writes this file.
type pluginLedger struct {
	Version int                        `json:"version"`
	Plugins map[string]pluginLedgerRec `json:"plugins"`
}

type pluginLedgerRec struct {
	Version       string    `json:"version"`
	Sha           string    `json:"sha,omitempty"`
	Target        string    `json:"target"`
	Source        string    `json:"source,omitempty"`
	Servers       []string  `json:"servers,omitempty"`
	Overridden    []string  `json:"overridden,omitempty"`
	QuarantinedAt time.Time `json:"quarantined_at,omitzero"`
	RetiredAt     time.Time `json:"retired_at,omitzero"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func loadPluginLedger(path string) (pluginLedger, []string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the vault ledger, resolved by the caller
	if errors.Is(err, fs.ErrNotExist) {
		return emptyPluginLedger(), nil, nil
	}

	if err != nil {
		return pluginLedger{}, nil, fmt.Errorf("read plugin ledger %s: %w", path, err)
	}

	var ledger pluginLedger

	if err := json.Unmarshal(data, &ledger); err != nil {
		return pluginLedger{}, nil, fmt.Errorf("parse plugin ledger %s: %w", path, err)
	}

	if ledger.Plugins == nil {
		ledger.Plugins = map[string]pluginLedgerRec{}
	}

	return ledger, nil, nil
}

func (l pluginLedger) save(path string) error {
	l.Version = pluginLedgerVersion

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin ledger: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write plugin ledger: %w", err)
	}

	return nil
}

func emptyPluginLedger() pluginLedger {
	return pluginLedger{Version: pluginLedgerVersion, Plugins: map[string]pluginLedgerRec{}}
}

func validPluginKey(marketplace, name string) bool {
	return filepath.IsLocal(marketplace) && filepath.IsLocal(name)
}

func pluginKey(marketplace, name string) string {
	return marketplace + "/" + name
}
