package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Server struct {
	Transport  string                     `json:"transport,omitempty"`
	Command    []string                   `json:"command,omitempty"`
	Env        map[string]string          `json:"env,omitempty"`
	URL        string                     `json:"url,omitempty"`
	Headers    map[string]string          `json:"headers,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

type Servers map[string]Server

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
