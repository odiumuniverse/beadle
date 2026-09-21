package bundle_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/bundle"
)

func jsonDoc(t *testing.T, data []byte) map[string]any {
	t.Helper()

	var doc map[string]any

	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse document: %v", err)
	}

	return doc
}

func handlerOf(t *testing.T, raw any) map[string]any {
	t.Helper()

	handler, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("handler is %T, not an object", raw)
	}

	return handler
}

func assertCommandHandler(t *testing.T, handler map[string]any, timeoutBound float64) {
	t.Helper()

	if handler["type"] != "command" {
		t.Fatalf("handler type is %v, expected command", handler["type"])
	}

	if command, ok := handler["command"].(string); !ok || command == "" {
		t.Fatalf("handler command is %v, expected a non-empty string", handler["command"])
	}

	if timeout, present := handler["timeout"]; present {
		value, ok := timeout.(float64)
		if !ok || value <= 0 || value >= timeoutBound {
			t.Fatalf("handler timeout is %v, expected (0, %v)", timeout, timeoutBound)
		}
	}
}

func TestBundleClaudeHookContract(t *testing.T) {
	Convey("Given the Claude fixture request", t, func() {
		_, files, err := bundle.Plan(fixtureRequest(bundle.Claude))
		So(err, ShouldBeNil)

		doc := jsonDoc(t, files["plugins/beadle-canon/hooks/hooks.json"])

		Convey("When the hooks document is inspected", func() {
			Convey("Then the required wrapper, description and matcher events are present", func() {
				events, ok := doc["hooks"].(map[string]any)
				So(ok, ShouldBeTrue)
				So(slices.Sorted(maps.Keys(events)), ShouldResemble, []string{"PostToolUse", "SessionStart"})

				_, described := doc["description"].(string)
				So(described, ShouldBeTrue)

				So(doc["SessionStart"], ShouldBeNil)

				for _, event := range slices.Sorted(maps.Keys(events)) {
					entries, ok := events[event].([]any)
					So(ok, ShouldBeTrue)
					So(entries, ShouldNotBeEmpty)

					for _, rawEntry := range entries {
						entry, ok := rawEntry.(map[string]any)
						So(ok, ShouldBeTrue)

						matcher, ok := entry["matcher"].(string)
						So(ok, ShouldBeTrue)
						So(matcher, ShouldNotBeEmpty)

						handlers, ok := entry["hooks"].([]any)
						So(ok, ShouldBeTrue)
						So(handlers, ShouldHaveLength, 1)

						handler := handlerOf(t, handlers[0])
						So(handler["type"], ShouldEqual, "command")
						assertCommandHandler(t, handler, 1000) // seconds
					}
				}
			})
		})
	})
}

func TestBundleGeminiHookContract(t *testing.T) {
	Convey("Given the Gemini fixture request", t, func() {
		_, files, err := bundle.Plan(fixtureRequest(bundle.Gemini))
		So(err, ShouldBeNil)

		doc := jsonDoc(t, files["hooks/hooks.json"])

		Convey("When the hooks document is inspected", func() {
			Convey("Then only the hooks wrapper exists and handlers carry name and milliseconds", func() {
				So(slices.Sorted(maps.Keys(doc)), ShouldResemble, []string{"hooks"})

				events, ok := doc["hooks"].(map[string]any)
				So(ok, ShouldBeTrue)
				So(slices.Sorted(maps.Keys(events)), ShouldResemble, []string{"AfterTool", "SessionStart"})

				for _, event := range slices.Sorted(maps.Keys(events)) {
					entries, ok := events[event].([]any)
					So(ok, ShouldBeTrue)

					for _, rawEntry := range entries {
						entry, ok := rawEntry.(map[string]any)
						So(ok, ShouldBeTrue)
						So(entry["matcher"], ShouldNotBeNil)

						handlers, ok := entry["hooks"].([]any)
						So(ok, ShouldBeTrue)
						So(handlers, ShouldHaveLength, 1)

						handler := handlerOf(t, handlers[0])
						So(handler["type"], ShouldEqual, "command")
						So(handler["name"], ShouldNotBeEmpty)

						if timeout, present := handler["timeout"]; present {
							So(timeout, ShouldEqual, 5000) // seconds × 1000
						}
					}
				}
			})
		})
	})
}

func TestBundleAntigravityManifestContract(t *testing.T) {
	Convey("Given the Antigravity fixture request", t, func() {
		_, files, err := bundle.Plan(fixtureRequest(bundle.Antigravity))
		So(err, ShouldBeNil)

		manifest := jsonDoc(t, files["plugin.json"])
		hooks := jsonDoc(t, files["hooks.json"])

		Convey("When the manifest and hooks are inspected", func() {
			Convey("Then the manifest carries exactly name and description", func() {
				So(slices.Sorted(maps.Keys(manifest)), ShouldResemble, []string{"description", "name"})
				So(manifest["name"], ShouldEqual, "beadle-canon")
				So(manifest["description"], ShouldNotBeEmpty)
			})

			Convey("Then hooks are owner-keyed with flat and matcher events", func() {
				So(slices.Sorted(maps.Keys(hooks)), ShouldResemble, []string{"beadle-canon"})

				owner, ok := hooks["beadle-canon"].(map[string]any)
				So(ok, ShouldBeTrue)
				So(slices.Sorted(maps.Keys(owner)), ShouldResemble, []string{"PostToolUse", "PreInvocation"})

				flat, ok := owner["PreInvocation"].([]any)
				So(ok, ShouldBeTrue)
				So(flat, ShouldHaveLength, 1)

				handler := handlerOf(t, flat[0])
				So(handler["type"], ShouldEqual, "command")
				assertCommandHandler(t, handler, 1000) // seconds
				_, wrapped := handler["hooks"]
				So(wrapped, ShouldBeFalse)

				matcherEvents, ok := owner["PostToolUse"].([]any)
				So(ok, ShouldBeTrue)
				So(matcherEvents, ShouldHaveLength, 1)

				entry := handlerOf(t, matcherEvents[0])
				So(entry["matcher"], ShouldNotBeNil)

				handlers, ok := entry["hooks"].([]any)
				So(ok, ShouldBeTrue)
				So(handlers, ShouldHaveLength, 1)
			})
		})
	})
}

func TestBundleVersionFollowsRenderedBytes(t *testing.T) {
	Convey("Given the same canon planned twice", t, func() {
		req := fixtureRequest(bundle.Claude)

		before, _, err := bundle.Plan(req)
		So(err, ShouldBeNil)

		Convey("When the rendered format changes without touching the canon", func() {
			restore := bundle.SetRenderSeamForTest(func(_ bundle.Host, files map[string][]byte) {
				for rel, data := range files {
					files[rel] = append(data, '\n')
				}
			})

			defer restore()

			after, _, err := bundle.Plan(req)
			So(err, ShouldBeNil)

			Convey("Then the version changes", func() {
				So(after.Version, ShouldNotEqual, before.Version)
			})
		})
	})
}
