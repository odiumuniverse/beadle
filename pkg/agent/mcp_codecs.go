package agent

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const (
	keyArgs        = "args"
	keyCommand     = "command"
	keyEnv         = "env"
	keyEnvironment = "environment"
	keyHeaders     = "headers"
	keyHTTPURL     = "httpUrl"
	keyType        = "type"
	keyURL         = "url"
)

var (
	dollarBraceRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	cursorRef      = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	bareDollarRef  = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)
	canonicalRef   = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

type refSyntax struct {
	in  func(string) string
	out func(string) string
}

var (
	claudeRefs = refSyntax{
		in:  func(v string) string { return dollarBraceRef.ReplaceAllString(v, "{env:$1}") },
		out: func(v string) string { return canonicalRef.ReplaceAllString(v, "$${$1}") },
	}
	openCodeRefs = refSyntax{
		in:  func(v string) string { return v },
		out: func(v string) string { return v },
	}
	geminiRefs = refSyntax{
		in: func(v string) string {
			out := dollarBraceRef.ReplaceAllString(v, "{env:$1}")
			if match := bareDollarRef.FindStringSubmatch(out); match != nil {
				return "{env:" + match[1] + "}"
			}

			return out
		},
		out: func(v string) string { return canonicalRef.ReplaceAllString(v, "$${$1}") },
	}
	cursorRefs = refSyntax{
		in:  func(v string) string { return cursorRef.ReplaceAllString(v, "{env:$1}") },
		out: func(v string) string { return canonicalRef.ReplaceAllString(v, "$${env:$1}") },
	}
)

func mapValues(values map[string]string, transform func(string) string) map[string]string {
	if values == nil {
		return nil
	}

	out := make(map[string]string, len(values))

	for key, value := range values {
		out[key] = transform(value)
	}

	return out
}

func normalizeTransport(name string) string {
	switch strings.ToLower(name) {
	case "", "local":
		return strings.ToLower(name)
	case "streamable-http", "streamablehttp", "streamable_http", "remote":
		return mcp.TransportHTTP
	default:
		return strings.ToLower(name)
	}
}

func commandServer(entry map[string]any, refs refSyntax) mcp.Server {
	server := mcp.Server{
		Transport: normalizeTransport(stringField(entry, keyType)),
		Env:       mapValues(toStringMap(entry[keyEnv]), refs.in),
		URL:       stringField(entry, keyURL),
		Headers:   mapValues(toStringMap(entry[keyHeaders]), refs.in),
	}

	if command := stringField(entry, keyCommand); command != "" {
		server.Command = append(server.Command, command)
	}

	server.Command = append(server.Command, toStringSlice(entry[keyArgs])...)

	return server
}

func inferTransport(server mcp.Server) mcp.Server {
	if server.Transport == "" {
		server.Transport = mcp.TransportStdio
		if server.URL != "" && len(server.Command) == 0 {
			server.Transport = mcp.TransportHTTP
		}
	}

	return server
}

func renderCommand(entry map[string]any, server mcp.Server, envKey string, refs refSyntax) {
	if len(server.Command) > 0 {
		entry[keyCommand] = server.Command[0]

		if len(server.Command) > 1 {
			entry[keyArgs] = server.Command[1:]
		}
	}

	if env := mapValues(server.Env, refs.out); len(env) > 0 {
		entry[envKey] = env
	}
}

func renderHeaders(entry map[string]any, server mcp.Server, refs refSyntax) {
	if headers := mapValues(server.Headers, refs.out); len(headers) > 0 {
		entry[keyHeaders] = headers
	}
}

var claudeMCP = mcpCodec{
	owned: []string{keyType, keyCommand, keyArgs, keyEnv, keyURL, keyHeaders},
	decode: func(entry map[string]any) (mcp.Server, bool) {
		server := inferTransport(commandServer(entry, claudeRefs))

		return server, server.Valid()
	},
	encode: func(server mcp.Server) map[string]any {
		entry := map[string]any{}

		if server.Remote() {
			entry[keyType] = cmp.Or(server.Transport, mcp.TransportHTTP)
			entry[keyURL] = server.URL
			renderHeaders(entry, server, claudeRefs)

			return entry
		}

		entry[keyType] = mcp.TransportStdio
		renderCommand(entry, server, keyEnv, claudeRefs)

		return entry
	},
}

var openCodeMCP = mcpCodec{
	owned: []string{keyType, keyCommand, keyEnvironment, keyURL, keyHeaders},
	decode: func(entry map[string]any) (mcp.Server, bool) {
		server := mcp.Server{
			Command: toStringSlice(entry[keyCommand]),
			Env:     toStringMap(entry[keyEnvironment]),
			URL:     stringField(entry, keyURL),
			Headers: toStringMap(entry[keyHeaders]),
		}

		switch stringField(entry, keyType) {
		case "local":
			server.Transport = mcp.TransportStdio
		case "remote":
			server.Transport = mcp.TransportHTTP
		}

		server = inferTransport(server)

		return server, server.Valid()
	},
	encode: func(server mcp.Server) map[string]any {
		entry := map[string]any{}

		if server.Remote() {
			entry[keyType] = "remote"
			entry[keyURL] = server.URL
			renderHeaders(entry, server, openCodeRefs)

			return entry
		}

		entry[keyType] = "local"
		entry[keyCommand] = server.Command

		if len(server.Env) > 0 {
			entry[keyEnvironment] = server.Env
		}

		return entry
	},
}

var geminiMCP = mcpCodec{
	owned: []string{keyType, keyCommand, keyArgs, keyEnv, keyURL, keyHTTPURL, keyHeaders},
	decode: func(entry map[string]any) (mcp.Server, bool) {
		server := commandServer(entry, geminiRefs)

		switch {
		case stringField(entry, keyHTTPURL) != "":
			server.URL = stringField(entry, keyHTTPURL)
			server.Transport = mcp.TransportHTTP
		case server.Transport == "" && server.URL != "":
			server.Transport = mcp.TransportSSE
		}

		server = inferTransport(server)

		return server, server.Valid()
	},
	encode: func(server mcp.Server) map[string]any {
		entry := map[string]any{}

		switch {
		case server.Remote() && server.Transport == mcp.TransportSSE:
			entry[keyURL] = server.URL
			renderHeaders(entry, server, geminiRefs)
		case server.Remote():
			entry[keyHTTPURL] = server.URL
			renderHeaders(entry, server, geminiRefs)
		default:
			renderCommand(entry, server, keyEnv, geminiRefs)
		}

		return entry
	},
}

var cursorMCP = mcpCodec{
	owned: []string{keyType, keyCommand, keyArgs, keyEnv, keyURL, keyHeaders},
	decode: func(entry map[string]any) (mcp.Server, bool) {
		server := commandServer(entry, cursorRefs)
		if server.Transport == mcp.TransportSSE {
			server.Transport = mcp.TransportHTTP
		}

		server = inferTransport(server)

		return server, server.Valid()
	},
	encode: func(server mcp.Server) map[string]any {
		entry := map[string]any{}

		if server.Remote() {
			entry[keyURL] = server.URL
			renderHeaders(entry, server, cursorRefs)

			return entry
		}

		entry[keyType] = mcp.TransportStdio
		renderCommand(entry, server, keyEnv, cursorRefs)

		return entry
	},
}
