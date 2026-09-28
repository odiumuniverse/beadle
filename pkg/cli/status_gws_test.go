package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// statusFixture builds a home whose claude bundle is verified, whose mcp file
// surface is off, and whose recorded complement names the servers the bundle
// cannot carry. offMode selects what the config asks for.
func statusFixture(t *testing.T, offMode bool, complement []string) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)

	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	vault := filepath.Join(home, ".beadle")

	cfg, err := config.Load(filepath.Join(vault, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	cfg.SetMode(agent.ClaudeCodeID, kind.MCP, config.ModeSync)

	if offMode {
		cfg.SetMode(agent.ClaudeCodeID, kind.MCP, config.ModeOff)
	}

	if err := cfg.Save(filepath.Join(vault, "config.json")); err != nil {
		t.Fatal(err)
	}

	st, err := state.Load(filepath.Join(vault, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	st.Bundles[string(bundle.Claude)] = state.BundleState{
		Enabled:    true,
		Registered: true,
		VerifyTier: state.VerifyExecuted,
		Complement: map[kind.ID][]string{kind.MCP: complement},
	}

	if err := st.Save(filepath.Join(vault, "state.json")); err != nil {
		t.Fatal(err)
	}

	return home
}

// TestStatusNamesBundleDelivery pins the report's answer to "mcp is off, so is
// nothing written?": a verified bundle keeps delivering the kind, and the
// servers it cannot carry reach the host file, so a bare "off" would lie.
func TestStatusNamesBundleDelivery(t *testing.T) {
	Convey("Given a verified claude bundle with mcp off and a recorded complement", t, func() {
		statusFixture(t, true, []string{"web-search-prime", "context7", "web-reader"})

		Convey("When status runs", func() {
			out, err := runCLI(t, "status")
			So(err, ShouldBeNil)

			Convey("Then the mode cell still reads off, unchanged in shape", func() {
				So(out, ShouldContainSubstring, "mcp:off")
			})

			Convey("And the report says the bundle delivers it and names the host-file copies", func() {
				So(out, ShouldContainSubstring,
					"bundles: claude mcp=off is delivered by the bundle; the servers it cannot carry go to the host file: context7, web-reader, web-search-prime")
			})

			Convey("And the agents table keeps one token per kind", func() {
				So(out, ShouldContainSubstring, "claude       enabled   installed")
			})
		})
	})
}

// TestStatusStaysSilentWithoutComplement is the control case: a verified bundle
// whose recorded complement is empty must not claim any host-file copy, and an
// unverified one must not be reported as delivering at all.
func TestStatusStaysSilentWithoutComplement(t *testing.T) {
	Convey("Given a verified claude bundle with mcp off and no complement", t, func() {
		statusFixture(t, true, nil)

		Convey("When status runs", func() {
			out, err := runCLI(t, "status")
			So(err, ShouldBeNil)

			Convey("Then the bundle delivery is reported without inventing names", func() {
				So(out, ShouldContainSubstring, "bundles: claude mcp=off is delivered by the bundle, not the file surface")
				So(out, ShouldNotContainSubstring, "go to the host file")
			})

			Convey("And the mode cell still reads off", func() {
				So(out, ShouldContainSubstring, "mcp:off")
			})
		})
	})

	Convey("Given a claude bundle that is enabled but not verified, with mcp off", t, func() {
		home := statusFixture(t, true, []string{"context7"})

		Convey("When the bundle fails verification and status runs", func() {
			st := loadCliState(t, home)
			entry := st.Bundles[string(bundle.Claude)]
			entry.VerifyTier = state.VerifyFailed
			st.Bundles[string(bundle.Claude)] = entry

			So(st.Save(filepath.Join(home, ".beadle", "state.json")), ShouldBeNil)

			out, err := runCLI(t, "status")
			So(err, ShouldBeNil)

			Convey("Then nothing claims delivery, because nothing is delivered", func() {
				So(out, ShouldContainSubstring, "mcp:off")
				So(out, ShouldNotContainSubstring, "is delivered by the bundle")
			})
		})
	})
}

// TestPrintBundleDeliveryCoversEveryBundleHost keeps the report tied to the
// bundle host list: a host added there with an off kind must be covered without
// touching this file again.
func TestPrintBundleDeliveryCoversEveryBundleHost(t *testing.T) {
	Convey("Given one agent per bundle host, each with every kind off", t, func() {
		cfg := config.Default()

		var (
			agents []*agent.Agent
			st     = &state.State{Bundles: map[string]state.BundleState{}}
			want   int
		)

		for _, host := range bundle.Hosts() {
			ag := &agent.Agent{ID: host.AgentID(), Surfaces: []agent.Surface{
				&stubSurface{kind: kind.MCP},
				&stubSurface{kind: kind.Skills},
			}}
			agents = append(agents, ag)

			for _, k := range host.Kinds() {
				cfg.SetMode(ag.ID, k, config.ModeOff)

				want++
			}

			st.Bundles[string(host)] = state.BundleState{
				Enabled:    true,
				Registered: true,
				VerifyTier: state.VerifyExecuted,
				Complement: map[kind.ID][]string{kind.MCP: {"secret-server"}},
			}
		}

		var out bytes.Buffer

		printBundleDelivery(&out, cfg, st, agents)

		Convey("Then every bundle host's kinds are named, with the complement listed", func() {
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			// One line per kind the host bundle owns: a host added to
			// bundle.Hosts() is covered without touching this test.
			So(lines, ShouldHaveLength, want)

			for _, line := range lines {
				So(line, ShouldStartWith, "bundles: ")
				So(line, ShouldContainSubstring, "=off is delivered by the bundle")
			}

			So(out.String(), ShouldContainSubstring, "secret-server")
		})
	})
}

// stubSurface is the minimum an agent needs for the report: a kind and a
// default mode, with no path behind it.
type stubSurface struct {
	kind kind.ID
}

func (s *stubSurface) Kind() kind.ID        { return s.kind }
func (s *stubSurface) Path() string         { return "" }
func (s *stubSurface) WatchPaths() []string { return nil }
func (s *stubSurface) Traits() agent.Traits { return agent.Traits{DefaultMode: config.ModeSync} }
func (s *stubSurface) Read(context.Context) (agent.Snapshot, error) {
	return agent.Snapshot{}, nil
}
func (s *stubSurface) Write(context.Context, kind.Items) error { return nil }
