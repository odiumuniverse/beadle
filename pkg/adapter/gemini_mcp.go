package adapter

import (
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const geminiID = "gemini-cli"

const fieldHTTPURL = "httpUrl"

var geminiCoreKeys = map[string]struct{}{
	fieldType:    {},
	fieldCommand: {},
	fieldArgs:    {},
	fieldEnv:     {},
	fieldHTTPURL: {},
	fieldURL:     {},
	fieldHeaders: {},
}

func geminiFromEntry(entry map[string]any) mcp.Server {
	server := singleCommandFromEntry(entry, geminiCoreKeys, geminiID)

	if url := stringField(entry, fieldHTTPURL); url != "" {
		server.URL = url
		server.Transport = transportHTTP
	} else if _, hasType := entry[fieldType]; !hasType && server.URL != "" {
		server.Transport = transportSSE
	}

	server.Env = mapValues(server.Env, geminiCanonicalizeRefs)
	server.Headers = mapValues(server.Headers, geminiCanonicalizeRefs)

	return server
}

func geminiToEntry(server mcp.Server) map[string]any {
	server.Env = mapValues(server.Env, geminiRenderRefs)
	server.Headers = mapValues(server.Headers, geminiRenderRefs)

	entry := make(map[string]any)

	switch {
	case server.Transport == transportSSE && server.URL != "":
		entry[fieldType] = transportSSE
		entry[fieldURL] = server.URL
	case server.URL != "":
		entry[fieldType] = transportHTTP
		entry[fieldHTTPURL] = server.URL
	default:
		renderCommandFields(entry, server, fieldEnv)
	}

	if len(server.Headers) > 0 {
		entry[fieldHeaders] = server.Headers
	}

	mergeExtension(entry, server.Extensions, geminiID)

	return entry
}
