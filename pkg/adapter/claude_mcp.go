package adapter

import (
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const claudeID = "claude-code"

var claudeCoreKeys = map[string]struct{}{
	fieldType:    {},
	fieldCommand: {},
	fieldArgs:    {},
	fieldEnv:     {},
	fieldURL:     {},
	fieldHeaders: {},
}

func claudeFromEntry(entry map[string]any) mcp.Server {
	server := singleCommandFromEntry(entry, claudeCoreKeys, claudeID)
	server.Env = mapValues(server.Env, claudeCanonicalizeRefs)
	server.Headers = mapValues(server.Headers, claudeCanonicalizeRefs)

	return server
}

func claudeToEntry(server mcp.Server) map[string]any {
	server.Env = mapValues(server.Env, claudeRenderRefs)
	server.Headers = mapValues(server.Headers, claudeRenderRefs)

	entry := make(map[string]any)

	if server.URL != "" && server.Transport != "" && server.Transport != transportStdio {
		entry[fieldType] = server.Transport
		entry[fieldURL] = server.URL
	} else {
		entry[fieldType] = transportStdio
		renderCommandFields(entry, server, fieldEnv)
	}

	if len(server.Headers) > 0 {
		entry[fieldHeaders] = server.Headers
	}

	mergeExtension(entry, server.Extensions, claudeID)

	return entry
}
