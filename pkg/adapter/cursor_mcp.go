package adapter

import (
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const cursorID = "cursor"

var cursorCoreKeys = map[string]struct{}{
	fieldType:    {},
	fieldCommand: {},
	fieldArgs:    {},
	fieldEnv:     {},
	fieldURL:     {},
	fieldHeaders: {},
}

func cursorFromEntry(entry map[string]any) mcp.Server {
	server := singleCommandFromEntry(entry, cursorCoreKeys, cursorID)
	server.Env = mapValues(server.Env, cursorCanonicalizeRefs)
	server.Headers = mapValues(server.Headers, cursorCanonicalizeRefs)

	return server
}

func cursorToEntry(server mcp.Server) map[string]any {
	server.Env = mapValues(server.Env, cursorRenderRefs)
	server.Headers = mapValues(server.Headers, cursorRenderRefs)

	entry := make(map[string]any)

	if server.URL != "" {
		entry[fieldURL] = server.URL
	} else {
		entry[fieldType] = transportStdio
		renderCommandFields(entry, server, fieldEnv)
	}

	if len(server.Headers) > 0 {
		entry[fieldHeaders] = server.Headers
	}

	mergeExtension(entry, server.Extensions, cursorID)

	return entry
}
