package engine

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestCacheSkillTargetParsing(t *testing.T) {
	Convey("Given a table of cache skill targets", t, func() {
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
			Convey("When parsing the "+tc.desc+" target", func() {
				key, skillName, ok := cacheSkillTarget(home, tc.target)

				Convey("Then the parsed key and skill match", func() {
					So(ok, ShouldEqual, tc.key != "")
					So(key, ShouldEqual, tc.key)
					So(skillName, ShouldEqual, tc.skill)
				})
			})
		}
	})
}
