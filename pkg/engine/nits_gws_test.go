package engine_test

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
)

func canonicalIssues(issues []engine.Issue) []engine.Issue {
	var out []engine.Issue

	for _, issue := range issues {
		if issue.Severity == engine.SeverityWarn && strings.Contains(issue.Message, "not in canonical form") {
			out = append(out, issue)
		}
	}

	return out
}

func TestPermissionCanonicalIssues(t *testing.T) {
	Convey("Given a permissions canon with a non-canonical key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.vault.PermissionsPath(), `{"tool:WebFetch":"deny","tool:webfetch":"allow"}`)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then only the non-canonical key is warned with the expected form", func() {
				canon := canonicalIssues(issues)
				So(canon, ShouldHaveLength, 1)
				So(canon[0].Severity, ShouldEqual, engine.SeverityWarn)
				So(canon[0].Message, ShouldContainSubstring, `"tool:WebFetch"`)
				So(canon[0].Message, ShouldContainSubstring, `expected "tool:webfetch"`)
			})
		})

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then one kind-level note names the count", func() {
				So(report.Kind(kind.Permissions).Warnings, ShouldHaveLength, 1)
				So(report.Kind(kind.Permissions).Warnings[0], ShouldContainSubstring, "1 permission key(s)")
			})
		})

		Convey("When the canon holds canonical keys only", func() {
			write(t, f.vault.PermissionsPath(), `{"tool:webfetch":"allow","bash:git status:*":"ask"}`)

			report := f.sync(t)
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then nothing is warned", func() {
				So(canonicalIssues(issues), ShouldBeEmpty)
				So(report.Kind(kind.Permissions).Warnings, ShouldBeEmpty)
			})
		})
	})
}
