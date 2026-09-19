package agent

import (
	"bytes"
	"context"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

type rulesSurface struct {
	path   string
	traits Traits
}

func (s *rulesSurface) Kind() kind.ID { return kind.Rules }

func (s *rulesSurface) Path() string { return s.path }

func (s *rulesSurface) WatchPaths() []string { return []string{s.path} }

func (s *rulesSurface) Traits() Traits { return s.traits }

func (s *rulesSurface) Read(context.Context) (Snapshot, error) {
	data, present, err := readFile(s.path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	if data == nil {
		data = []byte{}
	}

	return Snapshot{Items: kind.Items{kind.RulesKey: data}, Present: true}, nil
}

func (s *rulesSurface) Write(_ context.Context, desired kind.Items) error {
	content, ok := desired[kind.RulesKey]
	if !ok {
		return nil
	}

	current, present, err := readFile(s.path)
	if err != nil {
		return err
	}

	if present && bytes.Equal(current, content) {
		return nil
	}

	return writeFile(s.path, content, 0o644)
}
