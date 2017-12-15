package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDomains(t *testing.T) {
	d1, err := domainToRegex("www.youtube.com")
	if !assert.NoError(t, err) {
		return
	}

	d2, err := domainToRegex("*.youtube.com")
	if !assert.NoError(t, err) {
		return
	}

	d3, err := domainToRegex("*.*.youtube.com")
	if !assert.NoError(t, err) {
		return
	}

	assert.True(t, d1.MatchString("www.youtube.com"))
	assert.True(t, d2.MatchString("www.youtube.com"))
	assert.False(t, d3.MatchString("www.youtube.com"))
	assert.False(t, d1.MatchString("sub.www.youtube.com"))
	assert.False(t, d2.MatchString("sub.www.youtube.com"))
	assert.True(t, d3.MatchString("sub.www.youtube.com"))
	assert.False(t, d1.MatchString("other.youtube.com"))
	assert.True(t, d2.MatchString("other.youtube.com"))
	assert.False(t, d3.MatchString("other.youtube.com"))
}
