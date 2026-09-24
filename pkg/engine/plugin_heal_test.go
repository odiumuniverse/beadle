package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func quarantineCurrent(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), "quarantine", marketplace, name, "current")
}

func quarantinePlugin(t *testing.T, f *fixture) {
	t.Helper()

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	if len(f.sync(t).Farm) != 2 {
		t.Fatal("expected two farm results")
	}

	// The registry record stays: only the install path is gone, which is the
	// broken-install case that quarantines (a registry-removed plugin retires).
	if err := os.RemoveAll(plugin); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}

	f.sync(t)

	if !isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")) {
		t.Fatal("expected a stub after quarantine")
	}
}

func TestHealRemovesQuarantine(t *testing.T) {
	Convey("Given a quarantined plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		Convey("When heal runs twice", func() {
			results, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			before := read(t, f.vault.PluginsLedgerPath())

			resultsAgain, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			report := f.sync(t)

			rec := ledgerRecord(t, f, "acme/tool")

			Convey("Then the stubs and quarantine are removed and the record retired", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "acme/tool")
				So(results[0].Stubs, ShouldEqual, 2)
				So(results[0].Retired, ShouldEqual, 1)
				So(results[0].Note, ShouldBeEmpty)

				for _, dir := range []string{
					filepath.Join(claudeSkillsDir(f.home), "alpha"),
					filepath.Join(openCodeSkillsDir(f.home), "alpha"),
					filepath.Join(f.vault.PluginsDir(), "quarantine"),
				} {
					_, statErr := os.Stat(dir)
					So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
				}

				So(rec.QuarantinedAt.IsZero(), ShouldBeTrue)
				So(rec.RetiredAt.IsZero(), ShouldBeFalse)
				So(rec.Target, ShouldBeEmpty)

				So(resultsAgain, ShouldBeEmpty)
				So(report.Farm, ShouldBeEmpty)

				// The host registry still lists the plugin with a missing
				// path: the healed record stays retired and quiet.
				So(report.Plugins, ShouldHaveLength, 1)
				So(report.Plugins[0].Action, ShouldEqual, engine.PluginSkipped)
				So(report.Plugins[0].Note, ShouldContainSubstring, "retired")
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
			})
		})
	})
}

func TestHealReportsCleanedArtifactOnly(t *testing.T) {
	Convey("Given a quarantined plugin whose stubs are already gone", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		So(os.RemoveAll(filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeNil)
		So(os.RemoveAll(filepath.Join(openCodeSkillsDir(f.home), "alpha")), ShouldBeNil)

		Convey("When heal runs", func() {
			results, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			resultsAgain, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			Convey("Then only the cleaned artifact counts as work", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Key, ShouldEqual, "acme/tool")
				So(results[0].Stubs, ShouldEqual, 0)
				So(results[0].Cleaned, ShouldEqual, 1)

				So(resultsAgain, ShouldBeEmpty)
			})
		})
	})
}

func TestHealRetiresCleanRecord(t *testing.T) {
	Convey("Given a quarantined plugin with no stubs and no artifact", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		So(os.RemoveAll(filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeNil)
		So(os.RemoveAll(filepath.Join(openCodeSkillsDir(f.home), "alpha")), ShouldBeNil)
		So(os.Remove(quarantineCurrent(f, "acme", "tool")), ShouldBeNil)

		before := read(t, f.vault.PluginsLedgerPath())

		Convey("When heal runs dry then real", func() {
			results, err := f.engine.Heal(t.Context(), true)
			So(err, ShouldBeNil)

			So(results, ShouldHaveLength, 1)
			So(results[0].Retired, ShouldEqual, 1)
			So(results[0].Stubs, ShouldEqual, 0)
			So(results[0].Cleaned, ShouldEqual, 0)
			So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
			resultsReal, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			rec := ledgerRecord(t, f, "acme/tool")

			resultsAgain, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			Convey("Then the real run retires the clean record and a retired record is silent", func() {
				So(resultsReal, ShouldHaveLength, 1)
				So(resultsReal[0].Retired, ShouldEqual, 1)

				So(rec.QuarantinedAt.IsZero(), ShouldBeTrue)
				So(rec.RetiredAt.IsZero(), ShouldBeFalse)

				So(resultsAgain, ShouldBeEmpty)
			})
		})
	})
}

func TestHealDryRun(t *testing.T) {
	Convey("Given a quarantined plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		before := read(t, f.vault.PluginsLedgerPath())

		Convey("When heal runs as a dry run", func() {
			results, err := f.engine.Heal(t.Context(), true)
			So(err, ShouldBeNil)

			link, linkErr := os.Readlink(quarantineCurrent(f, "acme", "tool"))
			So(linkErr, ShouldBeNil)

			Convey("Then it predicts the work and writes nothing", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Stubs, ShouldEqual, 2)
				So(results[0].Retired, ShouldEqual, 1)

				So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeTrue)
				So(isStub(t, filepath.Join(openCodeSkillsDir(f.home), "alpha")), ShouldBeTrue)

				So(link, ShouldNotBeEmpty)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
			})
		})
	})
}

func TestHealKeepsForeign(t *testing.T) {
	Convey("Given a quarantine directory and a foreign skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		foreignNote := filepath.Join(f.vault.PluginsDir(), "quarantine", "acme", "tool", "README")
		write(t, foreignNote, "not ours\n")

		foreignSkill := filepath.Join(claudeSkillsDir(f.home), "Foreign")
		write(t, filepath.Join(foreignSkill, "SKILL.md"), "# foreign\n")

		Convey("When heal runs", func() {
			results, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			_, quarantineErr := os.Stat(quarantineCurrent(f, "acme", "tool"))

			rec := ledgerRecord(t, f, "acme/tool")

			Convey("Then foreign files survive and the record is retired", func() {
				So(results, ShouldHaveLength, 1)
				So(results[0].Note, ShouldContainSubstring, "is not empty")
				So(results[0].Stubs, ShouldEqual, 2)

				So(read(t, foreignNote), ShouldEqual, "not ours\n")
				So(errors.Is(quarantineErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, filepath.Join(foreignSkill, "SKILL.md")), ShouldEqual, "# foreign\n")
				So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeFalse)

				So(rec.RetiredAt.IsZero(), ShouldBeFalse)
			})
		})
	})
}

func TestHealKeepsOwnership(t *testing.T) {
	Convey("Given a quarantined plugin that owned an MCP server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		write(t, filepath.Join(plugin, ".mcp.json"), `{"mcpServers": {"plug": {"command": "plug"}}}`)

		report := f.sync(t)
		So(report.Kind(kind.MCP).Warnings, ShouldBeEmpty)
		So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldContainKey, "plug")
		So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "plug")

		removeFromRegistry(t, f.home, "acme", "tool")
		So(os.RemoveAll(plugin), ShouldBeNil)

		f.sync(t)

		Convey("When heal runs and the agent tries to re-adopt the server", func() {
			_, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			rec := ledgerRecord(t, f, "acme/tool")

			write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

			f.sync(t)

			_, vaultErr := os.Stat(f.vault.ServersPath())

			Convey("Then ownership survives and a retired server is never adopted", func() {
				So(rec.Servers, ShouldResemble, []string{"plug"})
				So(rec.RetiredAt.IsZero(), ShouldBeFalse)

				So(errors.Is(vaultErr, fs.ErrNotExist), ShouldBeTrue)
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "plug")
			})
		})
	})
}

func TestPluginQuarantineDoctorErrors(t *testing.T) {
	Convey("Given a quarantined plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		quarantinePlugin(t, f)

		Convey("When doctor runs before and after heal", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			So(hasIssue(issues, engine.SeverityError, "is quarantined since"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityError, "run beadle heal"), ShouldBeTrue)

			_, err = f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then a healed plugin is silent", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "acme/tool")
				}
			})
		})
	})
}
