package hooks_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/hooks"
)

// opsByPath indexes patch operations by their path.
func opsByPath(ops []hooks.Op) map[string]hooks.Op {
	out := map[string]hooks.Op{}

	for _, op := range ops {
		out[op.Path] = op
	}

	return out
}

func TestPlanHostFileCursor(t *testing.T) {
	Convey("Given a Cursor hooks file with foreign and stale beadle entries", t, func() {
		const existing = `{
  "version": 1,
  "hooks": {
    "preToolUse": [{"command": "foreign-tool"}],
    "stop": [{"command": "foreign-stop"}, {"command": "old-beadle"}]
  }
}
`

		canon := map[string]hooks.Hook{
			"keep": {Event: "stop", Command: "new-beadle"},
			"new":  {Event: "session-start", Command: "session-init", Timeout: 30},
			"warn": {Event: "notification", Command: "notify"},
		}
		approved := map[string]bool{"keep": true, "new": true, "warn": true}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), canon, approved, []string{"old-beadle"})
			So(err, ShouldBeNil)

			Convey("Then foreign hooks stay, beadle entries are replaced and the unmappable warns", func() {
				So(plan.Rendered, ShouldEqual, 2)
				So(plan.Owned, ShouldResemble, []string{"new-beadle", "session-init"})
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "notification")

				ops := opsByPath(plan.Ops)
				So(ops["/hooks/stop"].Value, ShouldResemble, []any{
					map[string]any{"command": "foreign-stop"},
					map[string]any{"command": "new-beadle"},
				})
				So(ops["/hooks/sessionStart"].Value, ShouldResemble, []any{
					map[string]any{"command": "session-init", "timeout": 30},
				})

				_, touched := ops["/hooks/preToolUse"]
				So(touched, ShouldBeFalse)

				_, hasVersion := ops["/version"]
				So(hasVersion, ShouldBeFalse)
			})
		})
	})

	Convey("Given a missing Cursor hooks file", t, func() {
		canon := map[string]hooks.Hook{
			"init": {Event: "session-start", Command: "session-init", Matcher: "*"},
		}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, nil, canon, map[string]bool{"init": true}, nil)
			So(err, ShouldBeNil)

			Convey("Then the document is created with the version and the wildcard is omitted", func() {
				ops := opsByPath(plan.Ops)
				So(ops["/version"].Value, ShouldEqual, 1)
				So(ops["/hooks"].Value, ShouldResemble, map[string]any{})
				So(ops["/hooks/sessionStart"].Value, ShouldResemble, []any{
					map[string]any{"command": "session-init"},
				})
			})
		})
	})

	Convey("Given a Cursor hooks file whose only entry is a stale beadle one", t, func() {
		const existing = `{"version": 1, "hooks": {"stop": [{"command": "ours"}]}}`

		Convey("When the canon no longer renders it", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), map[string]hooks.Hook{}, map[string]bool{}, []string{"ours"})
			So(err, ShouldBeNil)

			Convey("Then the event member is removed", func() {
				So(plan.Owned, ShouldBeEmpty)
				So(plan.Ops, ShouldHaveLength, 1)
				So(plan.Ops[0].Op, ShouldEqual, "remove")
				So(plan.Ops[0].Path, ShouldEqual, "/hooks/stop")
			})
		})
	})

	Convey("Given a Cursor hooks file already holding the rendered state", t, func() {
		const existing = `{
  "version": 1,
  "hooks": {
    "sessionStart": [{"command": "session-init"}]
  }
}
`

		canon := map[string]hooks.Hook{"init": {Event: "session-start", Command: "session-init"}}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), canon, map[string]bool{"init": true}, []string{"session-init"})
			So(err, ShouldBeNil)

			Convey("Then nothing changes", func() {
				So(plan.Ops, ShouldBeEmpty)
				So(plan.Owned, ShouldResemble, []string{"session-init"})
			})
		})
	})

	Convey("Given a Cursor hooks file with a broken document", t, func() {
		Convey("Then invalid JSON errors", func() {
			_, err := hooks.PlanHostFile(hooks.Cursor, []byte(`{"hooks": `), nil, nil, nil)
			So(err, ShouldBeError)
		})

		Convey("Then a JSONC comment errors: hosts parse plain JSON", func() {
			_, err := hooks.PlanHostFile(hooks.Cursor, []byte("{\n  // comment\n  \"hooks\": {}\n}\n"), nil, nil, nil)
			So(err, ShouldBeError)
		})

		Convey("Then an unsupported version errors", func() {
			_, err := hooks.PlanHostFile(hooks.Cursor, []byte(`{"version": 2, "hooks": {}}`), nil, nil, nil)
			So(err, ShouldBeError)
		})
	})
}

func TestPlanHostFileCursorGuards(t *testing.T) {
	Convey("Given a Cursor event holding a non-array value", t, func() {
		const existing = `{"version": 1, "hooks": {"stop": {"command": "foreign"}}}`

		canon := map[string]hooks.Hook{"state": {Event: "stop", Command: "session-state"}}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), canon, map[string]bool{"state": true}, nil)
			So(err, ShouldBeNil)

			Convey("Then the event is left untouched with a warning", func() {
				So(plan.Ops, ShouldBeEmpty)
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "not an array")
				So(plan.Warnings[0], ShouldContainSubstring, "1 canon hook(s) not rendered")
			})
		})
	})

	Convey("Given a non-array host-only event no canon hook maps to", t, func() {
		const existing = `{"version": 1, "hooks": {"workspaceOpen": {"command": "foreign"}}}`

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), map[string]hooks.Hook{}, map[string]bool{}, nil)
			So(err, ShouldBeNil)

			Convey("Then it is skipped silently", func() {
				So(plan.Ops, ShouldBeEmpty)
				So(plan.Warnings, ShouldBeEmpty)
			})
		})
	})

	Convey("Given two canon hooks sharing one command", t, func() {
		canon := map[string]hooks.Hook{
			"a": {Event: "pre-tool", Command: "shared"},
			"b": {Event: "post-tool", Command: "shared"},
		}
		approved := map[string]bool{"a": true, "b": true}

		Convey("When the rendered command is missing from the file", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(`{"version": 1, "hooks": {}}`), canon, approved, []string{"shared", "shared"})
			So(err, ShouldBeNil)

			Convey("Then ownership carries the command once and the entries count twice", func() {
				So(plan.Owned, ShouldResemble, []string{"shared"})
				So(plan.Rendered, ShouldEqual, 2)
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "no longer in the file")
			})
		})
	})

	Convey("Given a rendered command the user removed from the file", t, func() {
		const existing = `{"version": 1, "hooks": {"stop": [{"command": "foreign-stop"}]}}`

		canon := map[string]hooks.Hook{"state": {Event: "stop", Command: "session-state"}}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(existing), canon, map[string]bool{"state": true}, []string{"gone"})
			So(err, ShouldBeNil)

			Convey("Then the drift is warned and the canon copy is rendered again", func() {
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "no longer in the file")
				So(opsByPath(plan.Ops)["/hooks/stop"].Value, ShouldResemble, []any{
					map[string]any{"command": "foreign-stop"},
					map[string]any{"command": "session-state"},
				})
			})
		})
	})

	Convey("Given a null hooks member", t, func() {
		canon := map[string]hooks.Hook{"init": {Event: "session-start", Command: "session-init"}}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Cursor, []byte(`{"version": null, "hooks": null}`), canon, map[string]bool{"init": true}, nil)
			So(err, ShouldBeNil)

			Convey("Then null counts as missing and the document is filled in", func() {
				ops := opsByPath(plan.Ops)
				So(ops["/version"].Value, ShouldEqual, 1)
				So(ops["/hooks"].Value, ShouldResemble, map[string]any{})
				So(ops["/hooks/sessionStart"].Value, ShouldResemble, []any{
					map[string]any{"command": "session-init"},
				})
			})
		})
	})
}

func TestPlanHostFileCodex(t *testing.T) {
	Convey("Given a Codex hooks file with a foreign matcher group", t, func() {
		const existing = `{
  "hooks": {
    "PreToolUse": [{"hooks": [{"type": "command", "command": "foreign-tool"}]}]
  }
}
`

		canon := map[string]hooks.Hook{
			"state": {Event: "pre-tool", Command: "session-state", Matcher: "Bash"},
		}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Codex, []byte(existing), canon, map[string]bool{"state": true}, nil)
			So(err, ShouldBeNil)

			Convey("Then the foreign group stays and the canon group carries matcher and type", func() {
				So(plan.Warnings, ShouldBeEmpty)

				ops := opsByPath(plan.Ops)
				So(ops["/hooks/PreToolUse"].Value, ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign-tool"}}},
					map[string]any{
						"matcher": "Bash",
						"hooks":   []any{map[string]any{"type": "command", "command": "session-state"}},
					},
				})
			})
		})
	})

	Convey("Given a canon matcher on a non-tool event", t, func() {
		canon := map[string]hooks.Hook{
			"state": {Event: "stop", Command: "session-state", Matcher: "Bash"},
		}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Codex, nil, canon, map[string]bool{"state": true}, nil)
			So(err, ShouldBeNil)

			Convey("Then the matcher is dropped with a warning", func() {
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "not expressible on codex Stop")

				So(opsByPath(plan.Ops)["/hooks/Stop"].Value, ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "session-state"}}},
				})
			})
		})
	})

	Convey("Given a Codex group mixing beadle and foreign handlers", t, func() {
		const existing = `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "ours"}, {"type": "command", "command": "foreign"}]}]}}`

		canon := map[string]hooks.Hook{"state": {Event: "stop", Command: "session-state"}}

		Convey("When the plan runs", func() {
			plan, err := hooks.PlanHostFile(hooks.Codex, []byte(existing), canon, map[string]bool{"state": true}, []string{"ours"})
			So(err, ShouldBeNil)

			Convey("Then the foreign handler stays, the canon handler is rendered again and the mix warns", func() {
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "mixes beadle and foreign handlers")

				So(opsByPath(plan.Ops)["/hooks/Stop"].Value, ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign"}}},
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "session-state"}}},
				})
			})
		})

		Convey("When the canon hook is revoked", func() {
			plan, err := hooks.PlanHostFile(hooks.Codex, []byte(existing), map[string]hooks.Hook{}, map[string]bool{}, []string{"ours"})
			So(err, ShouldBeNil)

			Convey("Then our handler is cut out, the foreign one stays and the mix warns", func() {
				So(plan.Owned, ShouldBeEmpty)
				So(plan.Warnings, ShouldHaveLength, 1)
				So(plan.Warnings[0], ShouldContainSubstring, "mixes beadle and foreign handlers")

				So(opsByPath(plan.Ops)["/hooks/Stop"].Value, ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign"}}},
				})
			})
		})
	})
}
