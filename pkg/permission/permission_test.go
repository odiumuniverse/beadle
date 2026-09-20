package permission_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/permission"
)

func TestCanonical(t *testing.T) {
	Convey("Given a table of permission keys", t, func() {
		cases := []struct {
			key  string
			want bool
		}{
			{key: "bash:git status:*", want: true},
			{key: "bash:Bash(rm -rf)", want: true},
			{key: "bash:*", want: true},
			{key: "tool:webfetch", want: true},
			{key: "tool:mcp__github__create_issue", want: true},
			{key: "tool:WebFetch", want: false},
			{key: "tool:", want: false},
			{key: "mcp:github:create_issue", want: true},
			{key: "mcp:GitHub:create_issue", want: true},
			{key: "mcp:github:CreateIssue", want: false},
			{key: "mcp:github:", want: false},
			{key: "mcp::create_issue", want: false},
			{key: "tool:a:b", want: false},
			{key: "mcp:github:create:issue", want: false},
			{key: "unknown:key", want: false},
			{key: "bash:", want: false},
			{key: "tool", want: false},
			{key: "", want: false},
		}

		for _, tc := range cases {
			Convey("When checking "+tc.key, func() {
				Convey("Then canonicity matches", func() {
					So(permission.Canonical(tc.key), ShouldEqual, tc.want)
				})
			})
		}
	})
}
