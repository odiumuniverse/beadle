package engine

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheSkillTargetParsing(t *testing.T) {
	t.Parallel()

	home := "/home/tester"
	root := filepath.Join(home, ".claude", "plugins", "cache")

	cases := []struct {
		desc   string
		target string
		key    string
		skill  string
	}{
		{desc: "plain", target: root + "/acme/tool/1.0.0/skills/alpha", key: "acme/tool", skill: "alpha"},
		{desc: "trailing slash", target: root + "/acme/tool/1.0.0/skills/alpha/", key: "acme/tool", skill: "alpha"},
		{desc: "marketplaces", target: filepath.Join(home, ".claude", "plugins", "marketplaces", "acme", "tool", "skills", "alpha")},
		{desc: "not under cache", target: filepath.Join(home, ".claude", "plugins", "acme", "tool", "1.0.0", "skills", "alpha")},
		{desc: "missing skills segment", target: root + "/acme/tool/1.0.0/alpha"},
		{desc: "too few segments", target: root + "/acme/tool/1.0.0"},
		{desc: "too many segments", target: root + "/acme/tool/1.0.0/skills/alpha/extra"},
		{desc: "empty version", target: root + "/acme/tool//skills/alpha"},
		{desc: "escaping above cache", target: root + "/acme/../../outside/name/1.0.0/skills/alpha"},
		{desc: "escaping skill", target: root + "/acme/tool/1.0.0/skills/.."},
		{desc: "foreign prefix", target: filepath.Join(home, "elsewhere", "acme", "tool", "1.0.0", "skills", "alpha")},
		{desc: "relative", target: "acme/tool/1.0.0/skills/alpha"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			key, skillName, ok := cacheSkillTarget(home, tc.target)
			require.Equal(t, tc.key != "", ok)
			require.Equal(t, tc.key, key)
			require.Equal(t, tc.skill, skillName)
		})
	}
}
