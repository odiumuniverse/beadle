package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

func exportPermissionBlock(
	path, key string,
	convert func(map[string]any) (permission.Rules, permission.Override),
) (permission.Rules, permission.Override, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the adapter
	if errors.Is(err, fs.ErrNotExist) {
		return nil, permission.Override{}, false, nil
	}

	if err != nil {
		return nil, permission.Override{}, false, fmt.Errorf("read %s: %w", path, err)
	}

	doc := map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, permission.Override{}, false, fmt.Errorf("parse %s: %w", path, err)
	}

	block, _ := doc[key].(map[string]any)

	rules, override := convert(block)

	return rules, override, true, nil
}

func applyPermissionBlock(path, blockKey string, subKeys []string, rendered map[string]any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the adapter
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("agent config %s: %w", path, ErrNotConfigured)
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	if !jsonPointerExists(data, "/"+blockKey) {
		data, err = patchJSON(data, "/"+blockKey, map[string]any{})
		if err != nil {
			return fmt.Errorf("patch %s: %w", path, err)
		}
	}

	for _, key := range subKeys {
		data, err = patchJSON(data, "/"+blockKey+"/"+key, rendered[key])
		if err != nil {
			return fmt.Errorf("patch %s: %w", path, err)
		}
	}

	if err := writeFilePreserveMode(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
