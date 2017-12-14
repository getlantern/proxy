package proxy

import (
	"regexp"
	"strings"
)

func domainToRegex(domain string) (*regexp.Regexp, error) {
	parts := strings.Split(domain, ".")
	if parts[0] == "*" {
		parts[0] = `(.+)`
	}
	return regexp.Compile(strings.Join(parts, "."))
}
