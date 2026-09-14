package adapter

import "github.com/odiumuniverse/agents-sync/pkg/mcp"

func collectExtra(entry map[string]any, core map[string]struct{}) map[string]any {
	extra := make(map[string]any)

	for key, value := range entry {
		if _, isCore := core[key]; !isCore {
			extra[key] = value
		}
	}

	return extra
}

func stringField(entry map[string]any, key string) string {
	value, _ := entry[key].(string)

	return value
}

func singleCommandFromEntry(entry map[string]any, core map[string]struct{}, agentID string) mcp.Server {
	server := mcp.Server{
		Transport:  stringField(entry, fieldType),
		Extensions: marshalExtension(agentID, collectExtra(entry, core)),
	}

	if value, ok := entry[fieldCommand]; ok {
		command, _ := value.(string)
		if command != "" {
			server.Command = append(server.Command, command)
		}
	}

	if value, ok := entry[fieldArgs]; ok {
		server.Command = append(server.Command, toStringSlice(value)...)
	}

	if value, ok := entry[fieldEnv]; ok {
		server.Env = toStringMap(value)
	}

	if value, ok := entry[fieldURL]; ok {
		server.URL, _ = value.(string)
	}

	if value, ok := entry[fieldHeaders]; ok {
		server.Headers = toStringMap(value)
	}

	if server.Transport == "" {
		server.Transport = transportOf(server)
	}

	return server
}

func renderCommandFields(entry map[string]any, server mcp.Server, envKey string) {
	if len(server.Command) > 0 {
		entry[fieldCommand] = server.Command[0]

		if len(server.Command) > 1 {
			entry[fieldArgs] = server.Command[1:]
		}
	}

	if len(server.Env) > 0 {
		entry[envKey] = server.Env
	}
}
