package compliance

import "regexp"

// tokenPattern mirrors ../SMS-UI/src/lib/templates/variables.ts exactly. The
// UI renders a live preview from the same string, so a divergence here means
// the user previews one thing and we store another.
var tokenPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// ParseVariables returns the distinct variable names in a body, in first-seen
// order. It never returns nil: `variables` is a required array in the contract
// and NOT NULL in the schema, so a nil slice would serialise as null.
func ParseVariables(body string) []string {
	seen := make(map[string]bool)
	out := []string{}
	for _, match := range tokenPattern.FindAllStringSubmatch(body, -1) {
		name := match[1]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// ValidateBody reports whether every brace pair in the body forms a well-formed
// token. Removing the valid tokens should leave no braces behind; anything that
// remains is an unclosed brace, an empty pair, or an illegal name.
func ValidateBody(body string) ValidationResult {
	remaining := tokenPattern.ReplaceAllString(body, "")
	if containsAny(remaining, "{{", "}}") {
		return invalid("Variables must look like {{name}} using letters, numbers, or underscores.")
	}
	return valid()
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		for i := 0; i+len(needle) <= len(value); i++ {
			if value[i:i+len(needle)] == needle {
				return true
			}
		}
	}
	return false
}
