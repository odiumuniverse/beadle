package adapter

import "regexp"

var (
	claudeRefRe  = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	cursorRefRe  = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	geminiRefRe  = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	geminiBareRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

	canonicalRefRe = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

func claudeCanonicalizeRefs(value string) string {
	return claudeRefRe.ReplaceAllString(value, "{env:$1}")
}

func claudeRenderRefs(value string) string {
	return canonicalRefRe.ReplaceAllString(value, "$${$1}")
}

func cursorCanonicalizeRefs(value string) string {
	return cursorRefRe.ReplaceAllString(value, "{env:$1}")
}

func cursorRenderRefs(value string) string {
	return canonicalRefRe.ReplaceAllString(value, "$${env:$1}")
}

func geminiCanonicalizeRefs(value string) string {
	out := geminiRefRe.ReplaceAllString(value, "{env:$1}")

	if match := geminiBareRe.FindStringSubmatch(out); match != nil {
		return "{env:" + match[1] + "}"
	}

	return out
}

func geminiRenderRefs(value string) string {
	return canonicalRefRe.ReplaceAllString(value, "$${$1}")
}

func mapValues(values map[string]string, transform func(string) string) map[string]string {
	if values == nil {
		return nil
	}

	out := make(map[string]string, len(values))

	for key, value := range values {
		out[key] = transform(value)
	}

	return out
}
