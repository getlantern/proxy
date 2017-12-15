package proxy

import (
	"regexp"
	"strings"
)

func domainToRegex(domain string) (*regexp.Regexp, error) {
	parts := strings.Split(domain, ".")
	for i, part := range parts {
		if part == "*" {
			parts[i] = `[^.]+`
		}
	}
	return regexp.Compile("^" + strings.Join(parts, "\\."))
}
