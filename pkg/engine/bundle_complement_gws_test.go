package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// setHostMCPServers replaces the servers object of a host config file and
// keeps every other top-level key.
func setHostMCPServers(t *testing.T, path, pointer string, servers map[string]map[string]any) {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}

	doc[pointer] = servers

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}

	write(t, path, string(data))
}

// countMCPWarnings counts the MCP kind warnings of a sync that contain substr.
func countMCPWarnings(report *engine.Report, substr string) int {
	mcpReport := report.Kind(kind.MCP)
	if mcpReport == nil {
		return 0
	}

	count := 0

	for _, warning := range mcpReport.Warnings {
		if strings.Contains(warning, substr) {
			count++
		}
	}

	return count
}

// complementFixture enables a verified claude bundle over a canon with a
// plain server (the bundle carries it) and a secret-bearing one (it does not).
// Claude is the only agent, so no other host file feeds a literal back into
// the canon when the tests rotate a secret.
func complementFixture(t *testing.T) *fixture {
	t.Helper()

	f := bundleFixture(t)

	f.config.Disable(agent.OpenCodeID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	f.engine.Secrets().Set("API_TOKEN", "tok-1")

	if err := f.engine.Secrets().Save(); err != nil {
		t.Fatalf("save secrets: %v", err)
	}

	write(t, f.vault.ServersPath(), `{
		"plain": {"transport": "stdio", "command": ["node", "plain.js"]},
		"secret": {
			"transport": "http",
			"url": "https://example.com/mcp",
			"headers": {"Authorization": "{secret:API_TOKEN}"}
		}
	}`)

	f.sync(t)
	enableClaude(t, f)

	return f
}

func TestBundleComplementFollowsTheCanon(t *testing.T) {
	Convey("Given a verified claude bundle that manages MCP and a secret-bearing canon server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := complementFixture(t)

		Convey("When the canon changes and adds secret-bearing servers", func() {
			write(t, f.vault.ServersPath(), `{
				"plain": {"transport": "stdio", "command": ["node", "plain.js"]},
				"secret": {
					"transport": "http",
					"url": "https://changed.example.com/mcp",
					"headers": {"Authorization": "{secret:API_TOKEN}"}
				},
				"fresh": {
					"transport": "http",
					"url": "https://fresh.example.com/mcp",
					"headers": {"Authorization": "{secret:API_TOKEN}"}
				}
			}`)

			f.sync(t)

			host := hostMCPServers(t, f.claudeConfig(), "mcpServers")

			Convey("Then the host config follows the canon and the plain server stays in the bundle", func() {
				So(host["secret"]["url"], ShouldEqual, "https://changed.example.com/mcp")
				So(host["fresh"]["headers"], ShouldResemble, map[string]any{"Authorization": "tok-1"})
				So(host, ShouldNotContainKey, "plain")
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, secretRefMarkerForTest)
			})
		})

		Convey("When the secrets mode switches to env", func() {
			f.config.Secrets = secret.ModeEnv

			f.sync(t)

			Convey("Then only the rendered form of the complement copy changes", func() {
				host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(host["secret"]["headers"], ShouldResemble, map[string]any{"Authorization": "${API_TOKEN}"})
				So(host, ShouldNotContainKey, "plain")
			})
		})

		Convey("When the canon drops the secret-bearing server", func() {
			write(t, f.vault.ServersPath(), `{"plain": {"transport": "stdio", "command": ["node", "plain.js"]}}`)

			f.sync(t)

			Convey("Then its beadle copy leaves the host config", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "secret")
			})
		})

		Convey("When the user edits the host copy", func() {
			host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
			host["secret"]["url"] = "https://edited.example.com/mcp"
			setHostMCPServers(t, f.claudeConfig(), "mcpServers", host)

			report := f.sync(t)

			Convey("Then the edit is kept and reported, never overwritten", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers")["secret"]["url"], ShouldEqual, "https://edited.example.com/mcp")
				So(countMCPWarnings(report, "mcp secret: the claude-code copy differs from the canon"), ShouldEqual, 1)
			})
		})

		Convey("When the user adds a server of their own to the host config", func() {
			host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
			host["mine"] = map[string]any{"command": "node", "args": []any{"mine.js"}}
			setHostMCPServers(t, f.claudeConfig(), "mcpServers", host)

			f.sync(t)

			Convey("Then beadle neither takes nor claims it, so disabling the bundle keeps it", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldContainKey, "mine")

				_, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)

				f.sync(t)

				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldContainKey, "mine")
			})
		})
	})
}

func TestBundleComplementRestoresWithdrawnSecretServer(t *testing.T) {
	Convey("Given an older bundle that withdrew a secret-bearing server the bundle no longer carries", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := complementFixture(t)

		host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
		delete(host, "secret")
		setHostMCPServers(t, f.claudeConfig(), "mcpServers", host)

		st := loadState(t, f)
		entry := st.Bundles["claude"]
		entry.Withdrawn = append(entry.Withdrawn, state.WithdrawnItem{Kind: kind.MCP, Name: "secret"})
		st.Bundles["claude"] = entry
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		Convey("When the doctor runs before a sync", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the element-level zero delivery is an error", func() {
				So(hasIssue(issues, engine.SeverityError, "mcp secret is in the canon but neither the claude bundle nor the host config delivers it"), ShouldBeTrue)
			})
		})

		Convey("When sync runs", func() {
			f.sync(t)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the host config carries it again and the error is gone", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers")["secret"]["headers"], ShouldResemble, map[string]any{"Authorization": "tok-1"})
				So(hasIssue(issues, engine.SeverityError, "neither the claude bundle nor the host config delivers it"), ShouldBeFalse)
			})

			Convey("Then disabling the bundle still restores both servers", func() {
				_, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)

				servers := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(servers, ShouldContainKey, "plain")
				So(servers, ShouldContainKey, "secret")
			})
		})
	})
}

func TestBundleComplementUnresolvedSecret(t *testing.T) {
	Convey("Given a secret-bearing canon server whose secret does not resolve", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := complementFixture(t)

		write(t, f.vault.ServersPath(), `{
			"plain": {"transport": "stdio", "command": ["node", "plain.js"]},
			"secret": {
				"transport": "http",
				"url": "https://example.com/mcp",
				"headers": {"Authorization": "{secret:API_TOKEN}"}
			},
			"orphan": {
				"transport": "http",
				"url": "https://orphan.example.com/mcp",
				"headers": {"Authorization": "{secret:NOT_SET}"}
			}
		}`)

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then only that server stays out, with a warning, and the rest is still written", func() {
				host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(host, ShouldNotContainKey, "orphan")
				So(host, ShouldContainKey, "secret")
				So(countMCPWarnings(report, "mcp orphan: missing secrets NOT_SET"), ShouldEqual, 1)
			})
		})
	})
}
