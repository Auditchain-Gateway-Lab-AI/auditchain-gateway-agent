package sqlident

import "regexp"

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValid reports whether name is safe to use as an unquoted Oracle identifier
// after it has been validated against a local policy. Quoting alone is not a
// substitute for this check because policy values are still configuration
// input.
func IsValid(name string) bool {
	return len(name) > 0 && len(name) <= 128 && identifierPattern.MatchString(name)
}

// Quote returns an identifier quoted for Oracle SQL. Callers must validate the
// identifier with IsValid before calling Quote.
func Quote(name string) string {
	return `"` + name + `"`
}
