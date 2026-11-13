package pklschema

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// UpperFirst converts a Pkl property or identifier name into a deterministic
// exported Go identifier without Go initialism rewriting:
//   - camelCase:  httpPort -> HttpPort, apiUrl -> ApiUrl, id -> Id
//   - snake_case: max_conns -> MaxConns, api_url -> ApiUrl
//   - kebab/other separators: x-env -> XEnv
func UpperFirst(name string) string {
	if name == "" {
		return "Field"
	}

	// If name contains '_' or non-identifier separators, split into segments and UpperFirst each.
	if strings.ContainsAny(name, "_-. ") || !isValidGoIdentChars(name) {
		var b strings.Builder
		segStart := true
		for _, r := range name {
			if r == '_' || r == '-' || r == '.' || unicode.IsSpace(r) {
				segStart = true
				continue
			}
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				segStart = true
				continue
			}
			if b.Len() == 0 && unicode.IsDigit(r) {
				b.WriteString("F_")
			}
			if segStart {
				b.WriteRune(unicode.ToUpper(r))
				segStart = false
			} else {
				b.WriteRune(r)
			}
		}
		if b.Len() == 0 {
			return "Field"
		}
		return b.String()
	}

	r, size := utf8.DecodeRuneInString(name)
	if unicode.IsDigit(r) {
		return "F_" + name
	}
	return string(unicode.ToUpper(r)) + name[size:]
}

func isValidGoIdentChars(s string) bool {
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
		} else {
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}
