package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
	TransportWS    = "ws"
)

type Server struct {
	Transport string            `json:"transport,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (s Server) Remote() bool {
	return s.URL != "" && s.Transport != TransportStdio
}

func (s Server) Valid() bool {
	return len(s.Command) > 0 || s.URL != ""
}

type Servers map[string]Server

func Encode(s Server) []byte {
	fields := map[string]any{}

	if s.Transport != "" {
		fields["transport"] = s.Transport
	}

	if len(s.Command) > 0 {
		fields["command"] = s.Command
	}

	if len(s.Env) > 0 {
		fields["env"] = s.Env
	}

	if s.URL != "" {
		fields["url"] = s.URL
	}

	if len(s.Headers) > 0 {
		fields["headers"] = s.Headers
	}

	data, err := json.Marshal(fields)
	if err != nil {
		panic(fmt.Sprintf("encode mcp server: %v", err))
	}

	return data
}

func Decode(data []byte) (Server, error) {
	var s Server

	if err := json.Unmarshal(data, &s); err != nil {
		return Server{}, fmt.Errorf("decode mcp server: %w", err)
	}

	return s, nil
}

func (s Servers) MarshalCanonical() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode servers: %w", err)
	}

	return append(data, '\n'), nil
}

func ParseCanonical(data []byte) (Servers, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Servers{}, nil
	}

	servers := Servers{}
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse servers: %w", err)
	}

	return servers, nil
}
