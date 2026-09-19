package engine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

const (
	pluginLedgerVersion  = 1
	manifestAttempts     = 3
	scopeUser            = "user"
	pluginRegistryPath   = ".claude/plugins/installed_plugins.json"
	pluginMarketplaceDir = ".claude/plugins/marketplaces"
	quarantineDirName    = "quarantine"

	noteInvalidPluginKey   = "invalid plugin key"
	noteMissingInstall     = "install path is missing"
	noteNotInstalled       = "plugin is no longer installed"
	noteQuarantined        = "install path is missing; pivot quarantined; run beadle heal"
	noteQuarantinedAlready = "quarantined already"

	noteUnstableRegistry = "the plugin registry kept changing while being read; using the last consistent view"
)

type PluginAction string

const (
	PluginNoop        PluginAction = "noop"
	PluginCreated     PluginAction = "created"
	PluginRepointed   PluginAction = "repointed"
	PluginSkipped     PluginAction = "skipped"
	PluginQuarantined PluginAction = "quarantined"
)

type PluginResult struct {
	Key     string       `json:"key"`
	Version string       `json:"version"`
	Target  string       `json:"target,omitempty"`
	Action  PluginAction `json:"action"`
	Note    string       `json:"note,omitempty"`
}

type pluginLedger struct {
	Version int                        `json:"version"`
	Plugins map[string]pluginLedgerRec `json:"plugins"`
}

type pluginLedgerRec struct {
	Version       string    `json:"version"`
	Sha           string    `json:"sha,omitempty"`
	Target        string    `json:"target"`
	Servers       []string  `json:"servers,omitempty"`
	QuarantinedAt time.Time `json:"quarantined_at,omitzero"`
	RetiredAt     time.Time `json:"retired_at,omitzero"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type pluginGroup struct {
	Marketplace string
	Name        string
	Plugins     []plugin.Plugin
}

func (e *Engine) reconcilePlugins(_ context.Context) ([]PluginResult, []string, error) {
	var warns []string

	if err := e.vault.EnsureGitIgnore(); err != nil {
		return nil, []string{"plugins: " + err.Error()}, nil //nolint:nilerr // a broken .gitignore must not fail the whole sync; nothing plugin-related is written without it
	}

	if e.home == "" {
		return nil, warns, nil
	}

	manifest, manifestWarns, err := stableManifest(e.home)
	warns = append(warns, manifestWarns...)

	if err != nil {
		return nil, warns, err
	}

	ledger, ledgerWarns, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	warns = append(warns, ledgerWarns...)

	dirty := err != nil
	if err != nil {
		warns = append(warns, "plugins: "+err.Error())
		ledger = emptyPluginLedger()
	}

	groups := groupPlugins(manifest.Plugins)
	results := make([]PluginResult, 0, len(groups)+len(ledger.Plugins))
	installed := make(map[string]struct{}, len(groups))

	for _, group := range groups {
		key := pluginKey(group.Marketplace, group.Name)
		installed[key] = struct{}{}

		if !validPluginKey(group.Marketplace, group.Name) {
			results = append(results, PluginResult{Key: key, Action: PluginSkipped, Note: noteInvalidPluginKey})
			warns = append(warns, "plugins: "+noteInvalidPluginKey+" "+key)

			continue
		}

		result, rec, wrote := e.pivotPlugin(key, group, ledger.Plugins[key])
		if wrote {
			ledger.Plugins[key] = rec
			dirty = true
		}

		results = append(results, result)
	}

	removed, removedDirty := e.quarantineRemoved(installed, &ledger)

	results = append(results, removed...)

	dirty = dirty || removedDirty

	if dirty {
		if err := ledger.save(e.vault.PluginsLedgerPath()); err != nil {
			warns = append(warns, "plugins: "+err.Error())
		}
	}

	slices.SortFunc(results, func(a, b PluginResult) int { return cmp.Compare(a.Key, b.Key) })

	return results, warns, nil
}

func (e *Engine) quarantineRemoved(installed map[string]struct{}, ledger *pluginLedger) ([]PluginResult, bool) {
	var (
		results []PluginResult
		dirty   bool
	)

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		if _, ok := installed[key]; ok {
			continue
		}

		prev := ledger.Plugins[key]
		if !prev.RetiredAt.IsZero() {
			continue
		}

		result, rec, wrote := e.quarantinePlugin(key, prev, PluginResult{
			Key:     key,
			Version: prev.Version,
			Action:  PluginSkipped,
			Note:    noteNotInstalled,
		})
		if wrote {
			ledger.Plugins[key] = rec
			dirty = true
		}

		results = append(results, result)
	}

	return results, dirty
}

func (e *Engine) pivotPlugin(key string, group pluginGroup, prev pluginLedgerRec) (PluginResult, pluginLedgerRec, bool) {
	record := chooseRecord(group.Plugins)
	target := record.InstallPath
	result := PluginResult{Key: key, Version: record.Version, Target: target}

	if target == "" || !isDir(target) {
		result.Note = noteMissingInstall

		return e.quarantinePlugin(key, prev, result)
	}

	pivot := filepath.Join(e.vault.PluginsDir(), group.Marketplace, group.Name, farmPivotName)

	link, linkErr := os.Readlink(pivot)
	if link == target && prev.Version == record.Version && prev.Sha == record.GitCommitSha && prev.Target == target &&
		prev.QuarantinedAt.IsZero() && prev.RetiredAt.IsZero() {
		result.Action = PluginNoop

		return result, prev, false
	}

	if err := os.MkdirAll(filepath.Dir(pivot), 0o700); err != nil {
		result.Action = PluginSkipped
		result.Note = err.Error()

		return result, prev, false
	}

	if err := fsutil.ReplaceSymlink(pivot, target); err != nil {
		result.Action = PluginSkipped
		result.Note = err.Error()

		return result, prev, false
	}

	result.Action = PluginCreated
	if linkErr == nil {
		result.Action = PluginRepointed
	}

	rec := pluginLedgerRec{
		Version:   record.Version,
		Sha:       record.GitCommitSha,
		Target:    target,
		Servers:   prev.Servers,
		UpdatedAt: e.now(),
	}

	return result, rec, true
}

func (e *Engine) quarantinePlugin(key string, prev pluginLedgerRec, result PluginResult) (PluginResult, pluginLedgerRec, bool) {
	result.Action = PluginSkipped

	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) || prev.Target == "" || !prev.RetiredAt.IsZero() {
		return result, prev, false
	}

	if !prev.QuarantinedAt.IsZero() {
		result.Note = noteQuarantinedAlready

		return result, prev, false
	}

	pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName)

	info, err := os.Lstat(pivot)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return result, prev, false
	}

	link, err := os.Readlink(pivot)
	if err != nil || fsutil.Exists(link) {
		return result, prev, false
	}

	dst := filepath.Join(e.vault.PluginsDir(), quarantineDirName, marketplace, name, farmPivotName)

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		result.Note = err.Error()

		return result, prev, false
	}

	if err := removeStaleQuarantine(dst); err != nil {
		result.Note = err.Error()

		return result, prev, false
	}

	if err := os.Rename(pivot, dst); err != nil {
		result.Note = err.Error()

		return result, prev, false
	}

	rec := prev
	rec.QuarantinedAt = e.now()

	result.Version = prev.Version
	result.Action = PluginQuarantined
	result.Note = noteQuarantined

	return result, rec, true
}

func removeStaleQuarantine(dst string) error {
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink == 0:
		return fmt.Errorf("quarantine path %s exists and is not a symlink", dst)
	default:
		return os.Remove(dst)
	}
}

func stableManifest(home string) (plugin.Manifest, []string, error) {
	path := filepath.Join(home, pluginRegistryPath)

	var last plugin.Manifest

	for range manifestAttempts {
		before, err := readRegistryBytes(path)
		if err != nil {
			return plugin.Manifest{}, nil, err
		}

		manifest, err := plugin.Read(home)
		if err != nil {
			return plugin.Manifest{}, nil, err
		}

		after, err := readRegistryBytes(path)
		if err != nil {
			return plugin.Manifest{}, nil, err
		}

		if bytes.Equal(before, after) {
			return manifest, nil, nil
		}

		last = manifest
	}

	return last, []string{noteUnstableRegistry}, nil
}

func readRegistryBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is under the caller-provided home directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read plugin registry %s: %w", path, err)
	}

	return data, nil
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

func groupPlugins(plugins []plugin.Plugin) []pluginGroup {
	index := map[string]int{}

	var groups []pluginGroup

	for _, p := range plugins {
		key := pluginKey(p.Marketplace, p.Name)

		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i

			groups = append(groups, pluginGroup{Marketplace: p.Marketplace, Name: p.Name})
		}

		groups[i].Plugins = append(groups[i].Plugins, p)
	}

	slices.SortFunc(groups, func(a, b pluginGroup) int {
		return cmp.Compare(pluginKey(a.Marketplace, a.Name), pluginKey(b.Marketplace, b.Name))
	})

	return groups
}

func chooseRecord(plugins []plugin.Plugin) plugin.Plugin {
	chosen := plugins[0]

	for _, p := range plugins[1:] {
		if pickRecord(p) && !pickRecord(chosen) {
			chosen = p
		}
	}

	return chosen
}

func pickRecord(p plugin.Plugin) bool {
	return p.Scope == scopeUser
}

func validPluginKey(marketplace, name string) bool {
	return filepath.IsLocal(marketplace) && filepath.IsLocal(name)
}

func pluginKey(marketplace, name string) string {
	return marketplace + "/" + name
}
