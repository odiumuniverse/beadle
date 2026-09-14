package skill

import (
	"bytes"
	"maps"
	"slices"
)

const (
	ConflictModified = "modified"
	ConflictDeleted  = "deleted"
	ConflictAdded    = "added"
)

type Conflict struct {
	Skill string `json:"skill"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
}

type Tree3Result struct {
	Tree      Tree
	Conflicts []Conflict
}

func Tree3(skillName string, base, vault, agent Tree) Tree3Result {
	merged := Tree{}
	conflicts := make([]Conflict, 0)

	for _, path := range unionPaths(base, vault, agent) {
		baseValue, baseOK := base[path]
		vaultValue, vaultOK := vault[path]
		agentValue, agentOK := agent[path]

		switch {
		case sameFile(vaultOK, vaultValue, agentOK, agentValue):
			if vaultOK {
				merged[path] = vaultValue
			}
		case sameAsBase(baseOK, baseValue, vaultOK, vaultValue):
			if agentOK {
				merged[path] = agentValue
			}
		case sameAsBase(baseOK, baseValue, agentOK, agentValue):
			if vaultOK {
				merged[path] = vaultValue
			}
		default:
			conflicts = append(conflicts, Conflict{Skill: skillName, Path: path, Kind: conflictKind(vaultOK, agentOK)})

			if vaultOK {
				merged[path] = vaultValue
			}
		}
	}

	return Tree3Result{Tree: merged, Conflicts: conflicts}
}

func conflictKind(vaultOK, agentOK bool) string {
	switch {
	case !vaultOK:
		return ConflictAdded
	case !agentOK:
		return ConflictDeleted
	default:
		return ConflictModified
	}
}

func unionPaths(base, vault, agent Tree) []string {
	seen := make(map[string]struct{}, len(vault)+len(agent))

	for _, tree := range []Tree{base, vault, agent} {
		for path := range tree {
			seen[path] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

func sameFile(present bool, value []byte, otherPresent bool, other []byte) bool {
	if present != otherPresent {
		return false
	}

	if !present {
		return true
	}

	return bytes.Equal(value, other)
}

func sameAsBase(basePresent bool, baseValue []byte, present bool, value []byte) bool {
	return sameFile(basePresent, baseValue, present, value)
}
