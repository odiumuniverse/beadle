package plugin_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/plugin"
)

const testHome = "/home/u"

func TestReferences(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data string
		want []plugin.Reference
	}{
		{
			name: "command line token",
			data: `{"command": "node /home/u/.claude/plugins/mkt/p/1.0/x.cjs run"}`,
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/mkt/p/1.0/x.cjs"}},
		},
		{
			name: "quoted path",
			data: `{"command": "\"node\" \"/home/u/.claude/plugins/a/b/c/run.sh\" hook"}`,
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/a/b/c/run.sh"}},
		},
		{
			name: "single quoted path",
			data: `{"command": "run '/home/u/.claude/plugins/a/b/run.sh'"}`,
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/a/b/run.sh"}},
		},
		{
			name: "tilde path",
			data: `{"command": "cat ~/.claude/plugins/mkt/p/1.0/run.sh"}`,
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/mkt/p/1.0/run.sh"}},
		},
		{
			name: "plugin root placeholder",
			data: `{"command": "${CLAUDE_PLUGIN_ROOT}/scripts/x.cjs"}`,
		},
		{
			name: "embedded placeholder",
			data: `{"command": "/home/u/.claude/plugins/${VAR}/x.cjs"}`,
		},
		{
			name: "no plugins root",
			data: `{"command": "echo hello"}`,
		},
		{
			name: "two refs keep the order",
			data: `{"command": "/home/u/.claude/plugins/x/1.sh /home/u/.claude/plugins/y/2.sh"}`,
			want: []plugin.Reference{
				{Pointer: "/command", Path: "/home/u/.claude/plugins/x/1.sh"},
				{Pointer: "/command", Path: "/home/u/.claude/plugins/y/2.sh"},
			},
		},
		{
			name: "duplicate path reported once",
			data: `{"command": "/home/u/.claude/plugins/x/1.sh /home/u/.claude/plugins/x/1.sh"}`,
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/x/1.sh"}},
		},
		{
			name: "jsonc comments",
			data: "{\n// keep\n\"command\": \"x /home/u/.claude/plugins/z/1.sh\"\n}",
			want: []plugin.Reference{{Pointer: "/command", Path: "/home/u/.claude/plugins/z/1.sh"}},
		},
		{
			name: "nested pointer",
			data: `{"hooks": {"SessionStart": [{"hooks": [{"command": "/home/u/.claude/plugins/h/1.sh"}]}]}}`,
			want: []plugin.Reference{{Pointer: "/hooks/SessionStart/0/hooks/0/command", Path: "/home/u/.claude/plugins/h/1.sh"}},
		},
		{
			name: "pointer escaping",
			data: `{"a/b~c": "/home/u/.claude/plugins/x/1.sh"}`,
			want: []plugin.Reference{{Pointer: "/a~1b~0c", Path: "/home/u/.claude/plugins/x/1.sh"}},
		},
		{
			name: "prefix boundary rejects siblings",
			data: `{"a": "cat ~/.claude/plugins-old/x", "b": "/home/u/.claude/plugins2/x"}`,
		},
		{
			name: "prefix boundary keeps scanning",
			data: `{"a": "cat ~/.claude/plugins-old/x ~/.claude/plugins/ok.sh"}`,
			want: []plugin.Reference{{Pointer: "/a", Path: "/home/u/.claude/plugins/ok.sh"}},
		},
		{
			name: "bare prefix is a reference",
			data: `{"a": "ls ~/.claude/plugins"}`,
			want: []plugin.Reference{{Pointer: "/a", Path: "/home/u/.claude/plugins"}},
		},
		{
			name: "scalars and keys are not scanned",
			data: `{"/home/u/.claude/plugins/k/1": 42, "n": true, "z": null}`,
		},
		{
			name: "stop characters",
			data: `{"a": "one /home/u/.claude/plugins/x/1.sh;", "b": "(/home/u/.claude/plugins/y/2.sh)", "c": "/home/u/.claude/plugins/z/3.sh,"}`,
			want: []plugin.Reference{
				{Pointer: "/a", Path: "/home/u/.claude/plugins/x/1.sh"},
				{Pointer: "/b", Path: "/home/u/.claude/plugins/y/2.sh"},
				{Pointer: "/c", Path: "/home/u/.claude/plugins/z/3.sh"},
			},
		},
		{
			name: "more stop characters",
			data: "{\"d\": \"/home/u/.claude/plugins/q/4.sh|x\", \"e\": \"/home/u/.claude/plugins/r/5.sh>x\", \"f\": \"`/home/u/.claude/plugins/s/6.sh`\", \"g\": \"/home/u/.claude/plugins/t/7.sh}\"}",
			want: []plugin.Reference{
				{Pointer: "/d", Path: "/home/u/.claude/plugins/q/4.sh"},
				{Pointer: "/e", Path: "/home/u/.claude/plugins/r/5.sh"},
				{Pointer: "/f", Path: "/home/u/.claude/plugins/s/6.sh"},
				{Pointer: "/g", Path: "/home/u/.claude/plugins/t/7.sh"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := plugin.References([]byte(tc.data), testHome)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReferencesErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data string
		home string
	}{
		{
			name: "non json",
			data: "# markdown /home/u/.claude/plugins/x/1.sh",
			home: testHome,
		},
		{
			name: "empty home",
			data: `{"command": "x"}`,
			home: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := plugin.References([]byte(tc.data), tc.home)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestReferencesKeepsSamePathPerPointer(t *testing.T) {
	t.Parallel()

	path := testHome + "/.claude/plugins/marketplaces/thedotmack/plugin/scripts/worker-service.cjs"
	events := []string{"SessionStart", "BeforeAgent", "AfterAgent", "BeforeTool", "AfterTool", "Notification", "PreCompress"}

	var b strings.Builder

	b.WriteString(`{"hooks":{`)

	for i, event := range events {
		if i > 0 {
			b.WriteString(",")
		}

		fmt.Fprintf(&b, `%q:[{"hooks":[{"command":"bun %s hook"}]}]`, event, path)
	}

	b.WriteString("}}")

	refs, err := plugin.References([]byte(b.String()), testHome)
	require.NoError(t, err)
	require.Len(t, refs, len(events))

	for i, event := range events {
		require.Equal(t, plugin.Reference{Pointer: "/hooks/" + event + "/0/hooks/0/command", Path: path}, refs[i])
	}
}
