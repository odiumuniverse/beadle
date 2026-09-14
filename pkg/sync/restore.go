package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type RestoreOptions struct {
	Rev cas.Hash
}

func (e *Engine) Restore(ctx context.Context, resource string, opts RestoreOptions) error {
	switch resource {
	case ResourceRules, ResourceMCP, ResourceSkills, ResourcePermissions:
	default:
		return fmt.Errorf("unknown resource %q", resource)
	}

	data, err := e.baseBlob(resource)
	if err != nil {
		return err
	}

	if opts.Rev != "" {
		data, err = e.store.Get(opts.Rev)
		if err != nil {
			return fmt.Errorf("read revision: %w", err)
		}
	}

	if data == nil {
		return fmt.Errorf("resource %s has no base snapshot to restore", resource)
	}

	if err := e.writeCanon(resource, data); err != nil {
		return err
	}

	entry := e.ensureEntry(resource)
	entry.Vault = cas.HashOf(data)
	entry.Conflict = false

	if err := e.clearConflictArtifacts(resource); err != nil {
		return err
	}

	if err := e.state.Save(e.vault.RegistryPath()); err != nil {
		return err
	}

	_, err = e.Run(ctx, ModePush)

	return err
}

func (e *Engine) writeCanon(resource string, data []byte) error {
	switch resource {
	case ResourceRules:
		return writeVaultFile(filepath.Join(e.vault.Root(), "rules", "base.md"), data)
	case ResourceMCP:
		servers, err := mcp.ParseCanonical(data)
		if err != nil {
			return err
		}

		canonical, err := servers.MarshalCanonical()
		if err != nil {
			return err
		}

		return writeVaultFile(filepath.Join(e.vault.Root(), "mcp", "servers.json"), canonical)
	case ResourcePermissions:
		if _, err := permission.Parse(data); err != nil {
			return err
		}

		return writeVaultFile(e.permissionsPath(), data)
	case ResourceSkills:
		return e.restoreSkills(data)
	default:
		return fmt.Errorf("unknown resource %q", resource)
	}
}

func (e *Engine) restoreSkills(data []byte) error {
	manifests := map[string]skill.Manifest{}
	if err := json.Unmarshal(data, &manifests); err != nil {
		return fmt.Errorf("parse skills base: %w", err)
	}

	dir := filepath.Join(e.vault.Root(), "skills")

	for name, manifest := range manifests {
		tree := make(skill.Tree, len(manifest))

		for path, hash := range manifest {
			content, err := e.store.Get(hash)
			if err != nil {
				return fmt.Errorf("read skills base file %s/%s: %w", name, path, err)
			}

			tree[path] = content
		}

		if err := skill.SyncTree(dir, name, tree); err != nil {
			return err
		}
	}

	current, err := skill.ReadDir(dir)
	if err != nil {
		return err
	}

	for name := range current {
		if _, ok := manifests[name]; ok {
			continue
		}

		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove skill %s: %w", name, err)
		}
	}

	return nil
}

func writeVaultFile(path string, data []byte) error {
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
