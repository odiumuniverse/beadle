package adapter

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tailscale/hujson"
)

func patchJSON(data []byte, pointer string, value any) ([]byte, error) {
	root, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode patch value: %w", err)
	}

	patch := []byte(`[{"op":"add","path":` + strconv.Quote(pointer) + `,"value":` + string(encoded) + `}]`)

	if err := root.Patch(patch); err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}

	return root.Pack(), nil
}

func jsonPointerExists(data []byte, pointer string) bool {
	root, err := hujson.Parse(data)
	if err != nil {
		return false
	}

	return root.Find(pointer) != nil
}

func findJSON(data []byte, pointer string, dst any) error {
	root, err := hujson.Parse(data)
	if err != nil {
		return fmt.Errorf("parse json: %w", err)
	}

	node := root.Find(pointer)
	if node == nil {
		return nil
	}

	standardized, err := hujson.Standardize(node.Pack())
	if err != nil {
		return fmt.Errorf("standardize %s: %w", pointer, err)
	}

	if err := json.Unmarshal(standardized, dst); err != nil {
		return fmt.Errorf("decode %s: %w", pointer, err)
	}

	return nil
}
