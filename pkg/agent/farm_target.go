package agent

import "errors"

// FarmTarget tells the plugin farm where a file surface presents
// plugin-sourced copies next to the canon. Markdown hosts take a symlink
// named by FarmName; hosts whose file format differs from the plugin source
// (Codex TOML) implement FarmRenderer instead.
type FarmTarget interface {
	// FarmDir is the writable directory the host reads.
	FarmDir() string
	// FarmName names the presented copy of a plugin-namespaced item.
	FarmName(name string) string
}

// FarmRenderer converts a plugin's markdown definition into the host file
// bytes. ok is false when the host cannot express the definition.
type FarmRenderer interface {
	FarmRender(name string, markdown []byte) ([]byte, bool, error)
}

func (s *fileSurface[T]) FarmDir() string { return s.writeDir }

func (s *fileSurface[T]) FarmName(name string) string { return s.writeName(name) }

// FarmRender renders a plugin-sourced markdown definition in the host format:
// the same codec that writes canon items, fed with a document parsed from the
// plugin file.
func (s *fileSurface[T]) FarmRender(name string, markdown []byte) ([]byte, bool, error) {
	doc, err := s.model.parse(name, markdown)
	if err != nil {
		return nil, false, err
	}

	if !s.model.validName(s.model.name(doc)) {
		return nil, false, nil
	}

	out, err := s.render(doc, nil)
	if errors.Is(err, errCommandInexpressible) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	return out, true, nil
}
