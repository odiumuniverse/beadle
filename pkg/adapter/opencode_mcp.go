package adapter

import "github.com/odiumuniverse/agents-sync/pkg/mcp"

const openCodeID = "opencode"

var openCodeCoreKeys = map[string]struct{}{
	fieldType:        {},
	fieldCommand:     {},
	fieldEnvironment: {},
	fieldURL:         {},
	fieldHeaders:     {},
}

func openCodeFromEntry(entry map[string]any) mcp.Server {
	server := mcp.Server{}
	extra := make(map[string]any)

	for key, value := range entry {
		if _, core := openCodeCoreKeys[key]; !core {
			extra[key] = value

			continue
		}

		switch key {
		case fieldType:
			server.Transport, _ = value.(string)
		case fieldCommand:
			server.Command = toStringSlice(value)
		case fieldEnvironment:
			server.Env = toStringMap(value)
		case fieldURL:
			server.URL, _ = value.(string)
		case fieldHeaders:
			server.Headers = toStringMap(value)
		}
	}

	switch {
	case server.Transport == "local":
		server.Transport = transportStdio
	case server.Transport == "remote":
		server.Transport = transportHTTP
	case server.Transport == "" && (len(server.Command) > 0 || server.URL != ""):
		server.Transport = transportOf(server)
	}

	server.Extensions = marshalExtension(openCodeID, extra)

	return server
}

func openCodeToEntry(server mcp.Server) map[string]any {
	entry := make(map[string]any)

	switch server.Transport {
	case transportHTTP, transportSSE, "ws", "streamable-http":
		entry[fieldType] = "remote"
		if server.URL != "" {
			entry[fieldURL] = server.URL
		}

		if len(server.Headers) > 0 {
			entry[fieldHeaders] = server.Headers
		}
	case "", transportStdio:
		if len(server.Command) > 0 {
			entry[fieldType] = "local"
			entry[fieldCommand] = server.Command

			if len(server.Env) > 0 {
				entry[fieldEnvironment] = server.Env
			}
		}
	}

	mergeExtension(entry, server.Extensions, openCodeID)

	return entry
}

func transportOf(server mcp.Server) string {
	if server.URL != "" {
		return transportHTTP
	}

	return transportStdio
}
