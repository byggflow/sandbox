// Package netrules implements a compiled matcher for sandbox egress rules.
//
// Rules are compiled once at registration into a fast lookup structure:
//   - exact host map  (O(1))
//   - host suffix trie (O(label depth))
//   - regex tail       (O(n), evaluated last and only if needed)
//
// Within each bucket, the first matching rule wins (rules retain their
// registration order via Priority).
package netrules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Action is what the daemon does when a rule matches.
type Action string

const (
	// ActionAllow forwards the request unchanged to the upstream.
	ActionAllow Action = "allow"
	// ActionDeny short-circuits with a synthetic 403.
	ActionDeny Action = "deny"
	// ActionInject applies a header/query/body mutation, then forwards.
	ActionInject Action = "inject"
	// ActionDefer streams the request to the SDK for programmatic handling.
	// Only used when a handler is registered with the rule.
	ActionDefer Action = "defer"
)

// Match selects which requests a rule applies to.
// All non-zero fields must match (AND semantics).
type Match struct {
	// Host matches by exact host, *.suffix glob, or /regex/.
	// Empty string matches any host.
	Host string `json:"host,omitempty"`
	// Port matches the destination port. 0 means any port.
	Port int `json:"port,omitempty"`
	// Method is a comma-separated list of HTTP methods, e.g. "GET,POST".
	// Empty matches any method.
	Method string `json:"method,omitempty"`
	// PathPrefix matches if the request path starts with this string.
	// Empty matches any path.
	PathPrefix string `json:"path_prefix,omitempty"`
}

// Inject describes a static mutation applied to a matched request.
type Inject struct {
	// SetHeaders sets/overwrites these headers on the request before forwarding.
	SetHeaders map[string]string `json:"set_headers,omitempty"`
	// RemoveHeaders removes these headers before forwarding.
	RemoveHeaders []string `json:"remove_headers,omitempty"`
	// SetQuery sets URL query parameters.
	SetQuery map[string]string `json:"set_query,omitempty"`
}

// Rule is a single registered intercept directive.
type Rule struct {
	// ID is an opaque caller-assigned identifier, used for metrics/log tagging
	// and to address handlers when Action is ActionDefer.
	ID string `json:"id,omitempty"`
	// Priority is the index in the registration order. Lower wins on ties.
	Priority int `json:"-"`

	Match  Match  `json:"match"`
	Action Action `json:"action"`
	Inject Inject `json:"inject,omitempty"`
}

// Compiled is the data structure the daemon's hot path queries.
// It is built from a []Rule via Compile and is immutable after construction.
type Compiled struct {
	exact  map[string][]*compiledRule  // host -> rules in priority order
	suffix *suffixTrie                  // host suffix -> rules
	regex  []*compiledRule              // ordered regex rules
}

type compiledRule struct {
	rule      *Rule
	hostRegex *regexp.Regexp
	methodSet map[string]struct{} // nil means any method
	depth     int                 // for trie-stored rules: label count of the suffix
}

// Compile turns a rule list into a fast matcher. Order of input is preserved
// as priority — earlier rules win when host buckets overlap.
func Compile(rules []Rule) (*Compiled, error) {
	c := &Compiled{
		exact:  make(map[string][]*compiledRule),
		suffix: newSuffixTrie(),
	}

	for i := range rules {
		r := rules[i]
		r.Priority = i

		cr := &compiledRule{rule: &r}

		if r.Match.Method != "" {
			cr.methodSet = parseMethods(r.Match.Method)
		}

		host := strings.ToLower(r.Match.Host)
		switch {
		case host == "":
			// Any-host rule. Park it in regex tail with a match-all.
			cr.hostRegex = anyHostRegex
			c.regex = append(c.regex, cr)
		case strings.HasPrefix(host, "/") && strings.HasSuffix(host, "/") && len(host) > 1:
			re, err := regexp.Compile(host[1 : len(host)-1])
			if err != nil {
				return nil, fmt.Errorf("rule %d: invalid host regex: %w", i, err)
			}
			cr.hostRegex = re
			c.regex = append(c.regex, cr)
		case strings.HasPrefix(host, "*."):
			suffix := host[1:] // includes leading dot, e.g. ".openai.com"
			c.suffix.insert(suffix, cr)
		default:
			c.exact[host] = append(c.exact[host], cr)
		}
	}

	// Within each bucket, sort by priority (already insertion-ordered, so this
	// is a stability guarantee rather than a real reorder).
	for h := range c.exact {
		sortByPriority(c.exact[h])
	}
	return c, nil
}

// anyHostRegex matches any host. Used for host=="" rules.
var anyHostRegex = regexp.MustCompile(`.*`)

// Match returns the first rule that matches the given request, or nil.
// host should be lowercase and without the port.
func (c *Compiled) Match(host string, port int, method, path string) *Rule {
	method = strings.ToUpper(method)
	h := strings.ToLower(host)

	// 1. Exact host map.
	if rs, ok := c.exact[h]; ok {
		for _, cr := range rs {
			if cr.matches(port, method, path) {
				return cr.rule
			}
		}
	}

	// 2. Suffix trie. Walk labels right-to-left.
	if cr := c.suffix.lookup(h, port, method, path); cr != nil {
		return cr.rule
	}

	// 3. Regex tail.
	for _, cr := range c.regex {
		if cr.hostRegex != nil && !cr.hostRegex.MatchString(h) {
			continue
		}
		if cr.matches(port, method, path) {
			return cr.rule
		}
	}
	return nil
}

// Rules returns the original ruleset in registration order. Useful for
// metrics/debug and for re-serializing back to the SDK.
func (c *Compiled) Rules() []Rule {
	var out []Rule
	seen := make(map[int]struct{})
	push := func(cr *compiledRule) {
		if _, ok := seen[cr.rule.Priority]; ok {
			return
		}
		seen[cr.rule.Priority] = struct{}{}
		out = append(out, *cr.rule)
	}
	for _, rs := range c.exact {
		for _, cr := range rs {
			push(cr)
		}
	}
	c.suffix.forEach(push)
	for _, cr := range c.regex {
		push(cr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

func (cr *compiledRule) matches(port int, method, path string) bool {
	m := cr.rule.Match
	if m.Port != 0 && m.Port != port {
		return false
	}
	if cr.methodSet != nil {
		if _, ok := cr.methodSet[method]; !ok {
			return false
		}
	}
	if m.PathPrefix != "" && !strings.HasPrefix(path, m.PathPrefix) {
		return false
	}
	return true
}

func parseMethods(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, m := range strings.Split(s, ",") {
		m = strings.TrimSpace(strings.ToUpper(m))
		if m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

func sortByPriority(rs []*compiledRule) {
	sort.SliceStable(rs, func(i, j int) bool {
		return rs[i].rule.Priority < rs[j].rule.Priority
	})
}

// suffixTrie indexes rules by host suffix (e.g. ".github.com").
// Labels are stored right-to-left so the lookup walks from TLD inwards.
type suffixTrie struct {
	root *trieNode
}

type trieNode struct {
	children map[string]*trieNode
	rules    []*compiledRule // rules whose suffix ends at this node
	depth    int             // number of labels from root (root.depth == 0)
}

func newSuffixTrie() *suffixTrie {
	return &suffixTrie{root: &trieNode{children: map[string]*trieNode{}}}
}

func (t *suffixTrie) insert(suffix string, cr *compiledRule) {
	labels := splitLabelsReverse(suffix)
	node := t.root
	for i, label := range labels {
		next, ok := node.children[label]
		if !ok {
			next = &trieNode{children: map[string]*trieNode{}, depth: i + 1}
			node.children[label] = next
		}
		node = next
	}
	cr.depth = node.depth
	node.rules = append(node.rules, cr)
	sortByPriority(node.rules)
}

// lookup walks the host's labels right-to-left and returns the deepest
// matching rule. A *.suffix rule requires the host to extend at least one
// label past the suffix (so *.github.com matches foo.github.com but not
// github.com). On equal-depth ties the earliest registered rule wins.
func (t *suffixTrie) lookup(host string, port int, method, path string) *compiledRule {
	labels := splitLabelsReverse(host)
	node := t.root
	var best *compiledRule
	for _, label := range labels {
		next, ok := node.children[label]
		if !ok {
			break
		}
		node = next
		if node.depth >= len(labels) {
			// Wildcard requires at least one label below the suffix.
			continue
		}
		for _, cr := range node.rules {
			if !cr.matches(port, method, path) {
				continue
			}
			if best == nil || cr.depth > best.depth || (cr.depth == best.depth && cr.rule.Priority < best.rule.Priority) {
				best = cr
			}
			break
		}
	}
	return best
}

func (t *suffixTrie) forEach(fn func(*compiledRule)) {
	var walk func(*trieNode)
	walk = func(n *trieNode) {
		for _, cr := range n.rules {
			fn(cr)
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	walk(t.root)
}

// splitLabelsReverse splits "a.b.c" into ["c", "b", "a"]. A leading dot
// (e.g. ".github.com") is trimmed.
func splitLabelsReverse(host string) []string {
	host = strings.TrimPrefix(host, ".")
	parts := strings.Split(host, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}
