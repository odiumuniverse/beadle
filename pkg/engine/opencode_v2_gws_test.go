package engine_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
)

func TestOpenCodeMixedMCPPullKeepsCanon(t *testing.T) {
	Convey("Given a canon server that lives in the v1 OpenCode layer", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vault.ServersPath(), `{"alpha":{"transport":"stdio","command":["alpha-mcp"]}}`)
		f.sync(t)

		path := f.openCodeConfig()

		Convey("When the user adds a native container while the v1 server stays", func() {
			write(t, path, `{"mcp":{"alpha":{"type":"local","command":["alpha-mcp"]},"servers":{"beta":{"type":"local","command":["beta-mcp"]}}}}`)

			before := read(t, path)
			f.sync(t)

			Convey("Then the canon keeps alpha and gains beta, and the file is untouched", func() {
				servers := f.servers(t)
				So(servers, ShouldContainKey, "alpha")
				So(servers, ShouldContainKey, "beta")
				So(read(t, path), ShouldEqual, before)

				f.sync(t)
				So(read(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestOpenCodeMixedPermissionsPullKeepsCanon(t *testing.T) {
	Convey("Given a canon permission rule that lives in the v1 OpenCode layer", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.vault.PermissionsPath(), `{"bash:git status *":"allow"}`)
		f.sync(t)

		path := f.openCodeConfig()

		Convey("When the user adds a native permissions array next to the v1 map", func() {
			write(t, path, `{"permission":{"bash":{"git status *":"allow"}},"permissions":[{"action":"edit","resource":"*","effect":"deny"}]}`)

			before := read(t, path)
			f.sync(t)

			Convey("Then the canon keeps the v1 rule and gains the native one, and the file is untouched", func() {
				rules := f.rules(t)
				So(rules, ShouldContainKey, "bash:git status *")
				So(rules, ShouldContainKey, "tool:edit")
				So(read(t, path), ShouldEqual, before)

				f.sync(t)
				So(read(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestOpenCodeV2PermissionsConflict(t *testing.T) {
	Convey("Given a synced native permission rule", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.vault.PermissionsPath(), `{"tool:read":"allow"}`)
		f.sync(t)

		Convey("When both sides diverge, the conflict is recorded on the v2 file", func() {
			write(t, f.vault.PermissionsPath(), `{"tool:read":"deny"}`)
			write(t, f.openCodeConfig(), `{"permissions":[{"action":"read","resource":"*","effect":"ask"}]}`)

			f.sync(t)

			So(f.conflicts(t, kind.Permissions, agent.OpenCodeID), ShouldNotBeEmpty)
		})
	})
}

func TestOpenCodeV2NativeSync(t *testing.T) {
	Convey("Given a canon and a native V2 OpenCode config", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.vault.ServersPath(), `{"alpha":{"transport":"stdio","command":["alpha-mcp"]}}`)
		write(t, f.vault.PermissionsPath(), `{"tool:read":"allow"}`)

		path := f.openCodeConfig()
		write(t, path, `{
  // keep this comment
  "$schema": "https://opencode.ai/config.json",
  "mcp": {"servers": {"beta": {"type": "local", "command": ["beta-mcp"]}}},
  "permissions": [{"action": "edit", "resource": "*", "effect": "deny"}]
}
`)

		f.sync(t)

		Convey("Then both sides merge in the native dialect and the second sync is a noop", func() {
			servers := f.servers(t)
			So(servers, ShouldContainKey, "alpha")
			So(servers, ShouldContainKey, "beta")

			rules := f.rules(t)
			So(rules, ShouldContainKey, "tool:read")
			So(rules, ShouldContainKey, "tool:edit")

			text := read(t, path)
			So(text, ShouldContainSubstring, "keep this comment")
			So(text, ShouldContainSubstring, `"servers"`)
			So(text, ShouldContainSubstring, "alpha-mcp")
			So(text, ShouldContainSubstring, "beta-mcp")
			So(text, ShouldContainSubstring, "permissions")

			before := text

			f.sync(t)
			So(read(t, path), ShouldEqual, before)
		})
	})
}
