package strmatcher

import (
	"regexp"
	"strings"
)

type fullMatcher string

func (m fullMatcher) Match(s string) bool {
	return string(m) == s
}

type substrMatcher string

func (m substrMatcher) Match(s string) bool {
	return strings.Contains(s, string(m))
}

type domainMatcher string

func (m domainMatcher) Match(s string) bool {
	pattern := string(m)
	if !strings.HasSuffix(s, pattern) {
		return false
	}
	return len(s) == len(pattern) || s[len(s)-len(pattern)-1] == '.'
}

type regexMatcher struct {
	pattern *regexp.Regexp
}

func (m *regexMatcher) Match(s string) bool {
	return m.pattern.MatchString(s)
}

type notMatcher struct {
	matcher Matcher
}

func (m *notMatcher) Match(s string) bool {
	return !(m.matcher.Match(s))
}

type andMatcher struct {
	matcherA Matcher
	matcherB Matcher
}

func (m *andMatcher) Match(s string) bool {
	return m.matcherA.Match(s) && m.matcherB.Match(s)
}
