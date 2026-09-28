package bundle

import (
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/hooks"
)

type hookTarget struct {
	event string
	flat  bool
}

const (
	keyType    = "type"
	keyCommand = "command"
	keyHooks   = "hooks"
)

var hookEvents = map[Host]map[string]hookTarget{
	Claude: {
		"pre-tool":      {event: "PreToolUse"},
		"post-tool":     {event: "PostToolUse"},
		"session-start": {event: "SessionStart"},
		"stop":          {event: "Stop"},
		"notification":  {event: "Notification"},
	},
	Gemini: {
		"pre-tool":      {event: "BeforeTool"},
		"post-tool":     {event: "AfterTool"},
		"session-start": {event: "SessionStart"},
		"notification":  {event: "Notification"},
	},
	Antigravity: {
		"pre-tool":      {event: "PreToolUse"},
		"post-tool":     {event: "PostToolUse"},
		"stop":          {event: "Stop", flat: true},
		"session-start": {event: "PreInvocation", flat: true},
	},
}

// approvedHookNames lists the hook names the user approved, in hook order.
func approvedHookNames(req Request) []string {
	names := make([]string, 0, len(req.Hooks))

	for _, name := range slices.Sorted(maps.Keys(req.Hooks)) {
		if req.Approved[name] {
			names = append(names, name)
		}
	}

	return names
}

// hooklessHosts lists the bundle hosts with no command-hook file, so the
// canon's command hooks have nowhere to be rendered. omp is hookless: its
// hooks are TypeScript/JavaScript modules (hooks/pre/*.ts, hooks/post/*.ts)
// that default-export a factory and bind handlers in code — there is no
// declarative hook schema, so the bundle carries none (live-verified against
// omp 18.4.1 and omp://hooks.md, "Discovery and loading").
var hooklessHosts = map[Host]bool{Omp: true}

func renderHooks(req Request) ([]byte, []string, error) {
	names := approvedHookNames(req)

	if hooklessHosts[req.Host] {
		if len(names) == 0 {
			return nil, nil, nil
		}

		return nil, []string{fmt.Sprintf("%s has no command-hook file; %d approved hook(s) are not delivered", req.Host, len(names))}, nil
	}

	var warns []string

	if req.Host == Antigravity {
		doc := map[string]any{}

		for _, name := range names {
			target, ok := hookEvents[req.Host][req.Hooks[name].Event]
			if !ok {
				warns = append(warns, unmappableNote(req.Host, name, req.Hooks[name].Event))

				continue
			}

			entry := antigravityHook(target, req.Hooks[name], &warns, name)

			events, _ := doc[PluginName].(map[string]any)
			if events == nil {
				events = map[string]any{}
				doc[PluginName] = events
			}

			events[target.event] = appendAny(events[target.event], entry)
		}

		data, err := encodeJSON(doc)

		return data, warns, err
	}

	events := map[string]any{}

	for _, name := range names {
		hook := req.Hooks[name]

		target, ok := hookEvents[req.Host][hook.Event]
		if !ok {
			warns = append(warns, unmappableNote(req.Host, name, hook.Event))

			continue
		}

		handler := map[string]any{keyType: "command", keyCommand: hook.Command}
		if hook.Timeout > 0 {
			timeout := hook.Timeout
			if req.Host == Gemini {
				timeout *= 1000
			}

			handler["timeout"] = timeout
		}

		if req.Host == Gemini {
			handler["name"] = name
		}

		events[target.event] = appendAny(events[target.event], map[string]any{
			"matcher": matcherOf(hook),
			"hooks":   []any{handler},
		})
	}

	doc := map[string]any{keyHooks: events}
	if req.Host == Claude {
		doc[keyDescription] = canonDescription
	}

	data, err := encodeJSON(doc)

	return data, warns, err
}

func antigravityHook(target hookTarget, hook hooks.Hook, warns *[]string, name string) any {
	handler := map[string]any{keyType: "command", keyCommand: hook.Command}

	if hook.Timeout > 0 {
		handler["timeout"] = hook.Timeout
	}

	if !target.flat {
		return map[string]any{"matcher": matcherOf(hook), "hooks": []any{handler}}
	}

	if hook.Matcher != "" && hook.Matcher != "*" {
		*warns = append(*warns, fmt.Sprintf("hook %s (%s): antigravity ignores matchers on %s; matcher dropped", name, hook.Event, target.event))
	}

	return handler
}

func matcherOf(hook hooks.Hook) string {
	if hook.Matcher == "" {
		return "*"
	}

	return hook.Matcher
}

func unmappableNote(host Host, name, event string) string {
	return fmt.Sprintf("hook %s (%s): no %s event; not rendered", name, event, host)
}

func appendAny(existing, entry any) []any {
	list, _ := existing.([]any)

	return append(list, entry)
}
