package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func coveredBundleSkillsDir(t *testing.T, f *fixture) string {
	t.Helper()

	return filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", "skills")
}

func TestBundleSkipsCoveredSkills(t *testing.T) {
	Convey("Given a canon skill another tool already delivers to the claude read area", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "SKILL.md"), "# golang-x\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "docs", "guide.md"), "guide\n")

		foreign := filepath.Join(f.home, "skills-src", "golang-x")
		write(t, filepath.Join(foreign, "SKILL.md"), "# golang-x\n")
		write(t, filepath.Join(foreign, "docs", "guide.md"), "guide\n")

		So(os.MkdirAll(filepath.Join(f.home, ".claude", "skills"), 0o750), ShouldBeNil)
		So(os.Symlink(foreign, filepath.Join(f.home, ".claude", "skills", "golang-x")), ShouldBeNil)

		_, host, _ := enableClaude(t, f)

		coveredVersion := host.Version

		Convey("Then the rendered bundle leaves the covered name out", func() {
			So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "golang-x", "SKILL.md")), ShouldBeFalse)
			So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "alpha", "SKILL.md")), ShouldBeTrue)
		})

		Convey("And doctor reports the coverage as info", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "1 skill(s) covered by other tools"), ShouldBeTrue)
		})

		Convey("When the foreign copy disappears", func() {
			So(os.Remove(filepath.Join(f.home, ".claude", "skills", "golang-x")), ShouldBeNil)

			report := f.sync(t)

			Convey("Then the name returns to the bundle and the host is updated", func() {
				So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "golang-x", "SKILL.md")), ShouldBeTrue)
				So(host.Version, ShouldNotEqual, coveredVersion)
				So(host.Version, ShouldEqual, renderedClaudeVersion(t, f))
				So(report.Bundles, ShouldNotBeEmpty)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
			})
		})

		Convey("When the foreign copy drifted", func() {
			write(t, filepath.Join(foreign, "SKILL.md"), "# golang-x v2\n")

			report := f.sync(t)

			Convey("Then the canon copy stays and the fork is reported", func() {
				So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "golang-x", "SKILL.md")), ShouldBeTrue)
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "differs between the canon")
			})

			Convey("And doctor warns about the fork", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleDisableRestoresCoveredSkill(t *testing.T) {
	Convey("Given a covered skill recorded as withdrawn", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "SKILL.md"), "# golang-x\n")

		foreign := filepath.Join(f.home, "skills-src", "golang-x")
		write(t, filepath.Join(foreign, "SKILL.md"), "# golang-x\n")

		So(os.MkdirAll(filepath.Join(f.home, ".claude", "skills"), 0o750), ShouldBeNil)
		So(os.Symlink(foreign, filepath.Join(f.home, ".claude", "skills", "golang-x")), ShouldBeNil)

		enableClaude(t, f)

		st := loadState(t, f)

		entry := st.Bundles["claude"]
		entry.Withdrawn = append(entry.Withdrawn, state.WithdrawnItem{Kind: kind.Skills, Name: "golang-x", At: time.Now().UTC()})
		st.Bundles["claude"] = entry

		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		Convey("When the bundle is disabled", func() {
			report, err := f.engine.BundlesDisable(t.Context(), "claude")
			So(err, ShouldBeNil)
			So(report.Bundles, ShouldNotBeEmpty)

			Convey("Then the covered name is restored from the full canon", func() {
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(strings.Join(report.Warnings, " "), ShouldNotContainSubstring, "cannot be restored from the canon")

				after := loadState(t, f)
				So(after.Bundles, ShouldNotContainKey, "claude")
			})
		})
	})
}

func TestBundleCoverageFailsOpenOnUnreadableProvider(t *testing.T) {
	Convey("Given a foreign copy with an unreadable file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "SKILL.md"), "# golang-x\n")

		foreign := filepath.Join(f.home, "skills-src", "golang-x")
		write(t, filepath.Join(foreign, "SKILL.md"), "# golang-x\n")
		write(t, filepath.Join(foreign, "secret.md"), "hidden\n")

		secret := filepath.Join(foreign, "secret.md")
		So(os.Chmod(secret, 0o000), ShouldBeNil)
		t.Cleanup(func() { _ = os.Chmod(secret, 0o600) }) //nolint:gosec // G302: restoring the fixture file mode

		So(os.MkdirAll(filepath.Join(f.home, ".claude", "skills"), 0o750), ShouldBeNil)
		So(os.Symlink(foreign, filepath.Join(f.home, ".claude", "skills", "golang-x")), ShouldBeNil)

		enableClaude(t, f)

		Convey("Then the canon is delivered and the read error is reported", func() {
			So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "golang-x", "SKILL.md")), ShouldBeTrue)

			report := f.sync(t)
			So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "cannot read")
		})
	})
}
