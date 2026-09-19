package project

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

const PolicyVersion = 1

type FilePolicy struct {
	Enabled      bool `json:"enabled"`
	AllowSecrets bool `json:"allowSecrets,omitempty"`
}

type Policy struct {
	Version int                   `json:"version"`
	Files   map[string]FilePolicy `json:"files"`
	Servers []string              `json:"servers,omitempty"`
	Updated time.Time             `json:"updated,omitzero"`
}

func ParsePolicy(data []byte) (Policy, error) {
	policy := Policy{}

	if err := json.Unmarshal(data, &policy); err != nil {
		return Policy{}, fmt.Errorf("parse policy: %w", err)
	}

	if policy.Version != 0 && policy.Version != PolicyVersion {
		return Policy{}, fmt.Errorf("unsupported policy version %d", policy.Version)
	}

	return policy, nil
}

func (p Policy) Marshal() ([]byte, error) {
	p.Version = PolicyVersion

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode policy: %w", err)
	}

	return append(data, '\n'), nil
}

func (p Policy) File(rel string) (FilePolicy, bool) {
	file, ok := p.Files[rel]

	return file, ok
}

func (p Policy) Enabled(rel string) bool {
	file, ok := p.Files[rel]

	return ok && file.Enabled
}

func (p Policy) AllowsSecrets(rel string) bool {
	file, ok := p.Files[rel]

	return ok && file.Enabled && file.AllowSecrets
}

func (p Policy) ServerAllowed(name string) bool {
	return len(p.Servers) == 0 || slices.Contains(p.Servers, name)
}

func (p Policy) With(rel string, enabled, allowSecrets bool) Policy {
	files := make(map[string]FilePolicy, len(p.Files)+1)
	maps.Copy(files, p.Files)

	files[rel] = FilePolicy{Enabled: enabled, AllowSecrets: allowSecrets}
	p.Files = files

	return p
}

func (p Policy) Without(rel string) Policy {
	files := make(map[string]FilePolicy, len(p.Files))

	for key, file := range p.Files {
		if key == rel {
			continue
		}

		files[key] = file
	}

	p.Files = files

	return p
}
