package agent

import (
	"cmp"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// errCommandInexpressible marks a canonical command a host cannot render: the
// template uses a placeholder the host has no syntax for. The surface hides
// the item from that host instead of failing the sync.
var errCommandInexpressible = errors.New("the host cannot express this command")

// commandShell lists the shell-block syntaxes of the command hosts.
type commandShell int

const (
	shellBang   commandShell = iota // !`cmd`
	shellBraces                     // !{cmd}
	shellNone                       // templates have no shell blocks
)

// commandFile lists the file-reference syntaxes of the command hosts.
type commandFile int

const (
	fileAt    commandFile = iota // @path
	fileBrace                    // @{path}
	fileNone                     // references stay literal text
)

// commandArgs describes how one host spells template placeholders.
type commandArgs struct {
	// all is the all-arguments placeholder; "" means the host has none.
	all string
	// aliases lists additional all-arguments spellings (Pi: $@).
	aliases []string
	// positional reports $1.. placeholders.
	positional bool
	// shift maps a host index to the canonical one: canonical = index - shift.
	shift int
	// indexed reports $ARGUMENTS[N] placeholders (Claude, 0-based).
	indexed bool
	// named reports $NAME placeholders.
	named bool
	// params reports ${N:-default} and ${@:...} placeholders.
	params bool
	shell  commandShell
	file   commandFile
}

// commandAllArgs is the all-arguments placeholder shared by every host but
// Gemini.
const commandAllArgs = "$ARGUMENTS"

// commandCanon is the canonical dialect: 1-based $1.., $ARGUMENTS, $NAME,
// !`cmd` and @path.
var commandCanon = commandArgs{
	all: commandAllArgs, positional: true, named: true, params: true,
	shell: shellBang, file: fileAt,
}

// commandArgsClaude is the Claude dialect: 0-based $0.., $ARGUMENTS[N].
var commandArgsClaude = commandArgs{
	all: commandAllArgs, positional: true, shift: -1, indexed: true, named: true,
	shell: shellBang, file: fileAt,
}

// commandArgsOpenCode is the OpenCode and Kilo dialect: 1-based, no named
// arguments, file references stay literal.
var commandArgsOpenCode = commandArgs{
	all: commandAllArgs, positional: true, shell: shellBang, file: fileNone,
}

// commandArgsGemini is the Gemini dialect: {{args}}, !{cmd}, @{path}, no
// positional or named arguments.
var commandArgsGemini = commandArgs{
	all: "{{args}}", shell: shellBraces, file: fileBrace,
}

// commandArgsPi is the Pi dialect: 1-based positional and defaults, no shell
// or file expansion inside templates.
var commandArgsPi = commandArgs{
	all: commandAllArgs, aliases: []string{"$@"}, positional: true, params: true,
	shell: shellNone, file: fileNone,
}

// commandArgsCodex is the Codex prompts dialect (pull-only): 1-based and
// named, no shell or file expansion inside templates.
var commandArgsCodex = commandArgs{
	all: commandAllArgs, positional: true, named: true,
	shell: shellNone, file: fileNone,
}

// commandTemplate is the outcome of rewriting one template body.
type commandTemplate struct {
	Body  string
	Notes []string // constructs kept literally where the target does not expand them
	// Foreign lists the construct kinds the source dialect does not own but
	// that other dialects treat as active (the text is lifted literally).
	Foreign []string
}

// rewriteCommandBody rewrites a template body from one dialect to another.
// It fails with errCommandInexpressible when the body uses a placeholder the
// target cannot express; the caller then skips that host.
func rewriteCommandBody(body string, from, to commandArgs) (commandTemplate, error) {
	var (
		out    strings.Builder
		result commandTemplate
	)

	for i := 0; i < len(body); {
		consumed, err := rewriteCommandAt(body[i:], from, to, &out, &result)
		if err != nil {
			return commandTemplate{}, err
		}

		if consumed == 0 {
			out.WriteByte(body[i])

			i++

			continue
		}

		i += consumed
	}

	result.Body = out.String()

	return result, nil
}

// rewriteCommandAt rewrites the construct that starts at the given offset and
// returns the number of bytes it consumed; zero means plain text.
func rewriteCommandAt(rest string, from, to commandArgs, out *strings.Builder, result *commandTemplate) (int, error) {
	switch {
	case strings.HasPrefix(rest, "$$"):
		out.WriteString("$$")

		return 2, nil
	case strings.HasPrefix(rest, "$ARGUMENTS["):
		index, consumed, ok := commandIndexedAt(rest)
		if !ok {
			return 0, nil
		}

		if !from.indexed {
			result.Foreign = append(result.Foreign, "indexed")

			return 0, nil
		}

		if err := writeCommandPositional(out, index-from.shift, to); err != nil {
			return 0, err
		}

		return consumed, nil
	case strings.HasPrefix(rest, "${"):
		param, consumed, ok := commandParamAt(rest)
		if !ok {
			return 0, nil
		}

		if !to.params {
			return 0, errCommandInexpressible
		}

		if !from.params {
			result.Foreign = append(result.Foreign, "params")
		}

		out.WriteString(param)

		return consumed, nil
	case strings.HasPrefix(rest, "!`"), strings.HasPrefix(rest, "!{"):
		return rewriteCommandShell(rest, from, to, out, result)
	case rest[0] == '@':
		return rewriteCommandFile(rest, from, to, out, result)
	}

	return rewriteCommandWord(rest, from, to, out, result)
}

// rewriteCommandWord rewrites the all-arguments and dollar placeholders.
func rewriteCommandWord(rest string, from, to commandArgs, out *strings.Builder, result *commandTemplate) (int, error) {
	if spelling := commandAllArgsSpelling(rest, from); spelling != "" {
		return len(spelling), writeCommandAllArgs(out, to)
	}

	if spelling := commandForeignAllArgs(rest); spelling != "" {
		// Another dialect's all-arguments spelling is inert text here: keep
		// it literal instead of silently making it active.
		result.Foreign = append(result.Foreign, "all:"+spelling)

		out.WriteString(spelling)

		return len(spelling), nil
	}

	if rest[0] == '$' {
		return rewriteCommandDollar(rest, from, to, out, result)
	}

	return 0, nil
}

// commandForeignAllArgs returns the all-arguments spelling of another dialect
// that prefixes rest, if any.
func commandForeignAllArgs(rest string) string {
	for _, spelling := range []string{commandAllArgs, "$@", "{{args}}"} {
		if strings.HasPrefix(rest, spelling) {
			return spelling
		}
	}

	return ""
}

// commandAllArgsSpelling returns the source spelling of the all-arguments
// placeholder that prefixes rest, if any.
func commandAllArgsSpelling(rest string, from commandArgs) string {
	candidates := append([]string{from.all}, from.aliases...)

	slices.SortFunc(candidates, func(a, b string) int { return cmp.Compare(len(b), len(a)) })

	for _, spelling := range candidates {
		if spelling != "" && strings.HasPrefix(rest, spelling) {
			return spelling
		}
	}

	return ""
}

// rewriteCommandDollar rewrites $N and $NAME placeholders.
func rewriteCommandDollar(rest string, from, to commandArgs, out *strings.Builder, result *commandTemplate) (int, error) {
	if len(rest) < 2 {
		return 0, nil
	}

	if rest[1] >= '0' && rest[1] <= '9' {
		index, consumed := commandPositionalAt(rest)

		if !from.positional {
			result.Foreign = append(result.Foreign, "positional")

			out.WriteString(rest[:consumed])

			return consumed, nil
		}

		if err := writeCommandPositional(out, index-from.shift, to); err != nil {
			return 0, err
		}

		return consumed, nil
	}

	if !isCommandNameStart(rest[1]) {
		return 0, nil
	}

	name := commandNameAt(rest)

	if !to.named {
		return 0, errCommandInexpressible
	}

	if !from.named {
		result.Foreign = append(result.Foreign, "named")
	}

	out.WriteString(name)

	return len(name), nil
}

// rewriteCommandShell rewrites a shell block when the source dialect owns the
// syntax; the command text keeps going through the same conversion.
func rewriteCommandShell(rest string, from, to commandArgs, out *strings.Builder, result *commandTemplate) (int, error) {
	if from.shell == shellNone {
		if _, _, ok := commandShellAt(rest); ok {
			result.Foreign = append(result.Foreign, "shell")
		}

		return 0, nil
	}

	if from.shell == shellBraces && !strings.HasPrefix(rest, "!{") {
		return 0, nil
	}

	if from.shell == shellBang && !strings.HasPrefix(rest, "!`") {
		return 0, nil
	}

	command, consumed, ok := commandShellAt(rest)
	if !ok {
		return 0, nil
	}

	inner, err := rewriteCommandBody(command, from, to)
	if err != nil {
		return 0, err
	}

	result.Notes = append(result.Notes, inner.Notes...)

	switch to.shell {
	case shellBraces:
		out.WriteString("!{" + inner.Body + "}")
	case shellBang:
		out.WriteString("!`" + inner.Body + "`")
	default:
		// The host has no shell syntax: keep the text literal instead of
		// dropping it, so a round trip never loses host bytes.
		out.WriteString(rest[:consumed])

		result.Notes = append(result.Notes, "shell blocks are not expanded here; the text stays literal")
	}

	return consumed, nil
}

// rewriteCommandFile rewrites a file reference; a host without file expansion
// keeps the source spelling and gets a note.
func rewriteCommandFile(rest string, from, to commandArgs, out *strings.Builder, result *commandTemplate) (int, error) {
	if from.file == fileNone {
		if _, _, ok := commandFileAt(rest, commandArgs{file: fileAt}); ok {
			result.Foreign = append(result.Foreign, "file")
		}

		return 0, nil
	}

	path, consumed, ok := commandFileAt(rest, from)
	if !ok {
		return 0, nil
	}

	switch to.file {
	case fileBrace:
		out.WriteString("@{" + path + "}")
	case fileAt:
		out.WriteString("@" + path)
	default:
		out.WriteString(rest[:consumed])

		result.Notes = append(result.Notes, "file references are not expanded here; the text stays literal")
	}

	return consumed, nil
}

// writeCommandPositional renders one positional placeholder; canonical is the
// 1-based canonical index.
func writeCommandPositional(out *strings.Builder, canonical int, to commandArgs) error {
	if !to.positional || canonical+to.shift < 0 {
		return errCommandInexpressible
	}

	out.WriteString("$" + strconv.Itoa(canonical+to.shift))

	return nil
}

// writeCommandAllArgs renders the all-arguments placeholder.
func writeCommandAllArgs(out *strings.Builder, to commandArgs) error {
	if to.all == "" {
		return errCommandInexpressible
	}

	out.WriteString(to.all)

	return nil
}

// commandPositionalAt reads a $N placeholder and returns the host index.
func commandPositionalAt(rest string) (index, consumed int) {
	pos := 1

	for pos < len(rest) && rest[pos] >= '0' && rest[pos] <= '9' {
		pos++
	}

	index, err := strconv.Atoi(rest[1:pos])
	if err != nil {
		return 0, 1
	}

	return index, pos
}

// commandIndexedAt reads a $ARGUMENTS[N] placeholder and returns the index.
func commandIndexedAt(rest string) (index, consumed int, ok bool) {
	const prefix = "$ARGUMENTS["

	closing := strings.IndexByte(rest[len(prefix):], ']')
	if closing < 0 {
		return 0, 0, false
	}

	index, err := strconv.Atoi(strings.TrimSpace(rest[len(prefix) : len(prefix)+closing]))
	if err != nil || index < 0 {
		return 0, 0, false
	}

	return index, len(prefix) + closing + 1, true
}

// commandParamAt reads a ${...} placeholder; only defaults and Pi slices are
// recognized as placeholders.
func commandParamAt(rest string) (param string, consumed int, ok bool) {
	closing := strings.IndexByte(rest, '}')
	if closing < 0 {
		return "", 0, false
	}

	expr := rest[2:closing]
	if !strings.Contains(expr, ":-") && !strings.HasPrefix(expr, "@") {
		return "", 0, false
	}

	return rest[:closing+1], closing + 1, true
}

// commandShellAt reads a shell block and returns its command text.
func commandShellAt(rest string) (command string, consumed int, ok bool) {
	if strings.HasPrefix(rest, "!`") {
		closing := strings.IndexByte(rest[2:], '`')
		if closing < 0 {
			return "", 0, false
		}

		return rest[2 : 2+closing], 2 + closing + 1, true
	}

	if !strings.HasPrefix(rest, "!{") {
		return "", 0, false
	}

	depth := 0

	for i := 2; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return rest[2:i], i + 1, true
			}

			depth--
		}
	}

	return "", 0, false
}

// commandFileAt reads a file reference in the source dialect.
func commandFileAt(rest string, from commandArgs) (path string, consumed int, ok bool) {
	if strings.HasPrefix(rest, "@{") && from.file == fileBrace {
		closing := strings.IndexByte(rest[2:], '}')
		if closing < 0 {
			return "", 0, false
		}

		return rest[2 : 2+closing], 2 + closing + 1, true
	}

	if from.file != fileAt {
		return "", 0, false
	}

	pos := 1
	for pos < len(rest) && !isCommandSpace(rest[pos]) {
		pos++
	}

	if pos == 1 {
		return "", 0, false
	}

	// Sentence punctuation right after a bare reference is not part of the
	// path: "@README.md." refers to README.md.
	path = strings.TrimRight(rest[1:pos], ".,;:!?)]")

	if path == "" {
		return "", 0, false
	}

	return path, 1 + len(path), true
}

// commandNameAt reads a $NAME placeholder including the dollar sign.
func commandNameAt(rest string) string {
	pos := 2

	for pos < len(rest) && isCommandNamePart(rest[pos]) {
		pos++
	}

	return rest[:pos]
}

// isCommandNameStart reports whether a byte can start a named placeholder.
func isCommandNameStart(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_'
}

// isCommandNamePart reports whether a byte can continue a named placeholder.
func isCommandNamePart(char byte) bool {
	return isCommandNameStart(char) || (char >= '0' && char <= '9') || char == '-'
}

// isCommandSpace reports whether a byte ends a bare file reference.
func isCommandSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

// commandPullNotes lists the template constructs a source host does not
// expand: the text is lifted literally and becomes active on hosts that do.
func commandPullNotes(body string, from commandArgs, host string) []string {
	tpl, err := rewriteCommandBody(body, from, commandCanon)
	if err != nil {
		return nil
	}

	return commandForeignNotes(tpl.Foreign, host)
}

// commandForeignNotes renders one notice per construct kind a host does not
// own.
func commandForeignNotes(foreign []string, host string) []string {
	messages := map[string]string{
		"positional": "$N",
		"indexed":    "$ARGUMENTS[N]",
		"named":      "$NAME",
		"params":     "${N:-default}",
	}

	var notes []string

	for _, kind := range dedupStrings(foreign) {
		switch {
		case strings.HasPrefix(kind, "all:"):
			notes = append(notes, host+" does not expand "+strings.TrimPrefix(kind, "all:")+"; the text stays literal")
		case kind == "shell":
			notes = append(notes, host+" does not expand shell blocks; the text is lifted literally and becomes a shell block on other hosts")
		case kind == "file":
			notes = append(notes, host+" does not expand file references; the text is lifted literally and becomes a file attachment on other hosts")
		default:
			notes = append(notes, host+" does not expand "+messages[kind]+"; the text is lifted literally and becomes a placeholder on other hosts")
		}
	}

	return notes
}

// commandNamedPlaceholders lists the named placeholders of a template.
func commandNamedPlaceholders(body string) []string {
	var out []string

	for i := 0; i < len(body); {
		rest := body[i:]

		if strings.HasPrefix(rest, "$$") {
			i += 2

			continue
		}

		if spelling := commandAllArgsSpelling(rest, commandCanon); spelling != "" {
			i += len(spelling)

			continue
		}

		if rest[0] == '$' && len(rest) > 1 && isCommandNameStart(rest[1]) {
			name := strings.TrimPrefix(commandNameAt(rest), "$")

			out = append(out, name)

			i += len(name) + 1

			continue
		}

		i++
	}

	return dedupStrings(out)
}
