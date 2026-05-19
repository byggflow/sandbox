package netrules

import (
	"strings"
	"testing"
)

func TestExactHostMatch(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: "api.openai.com"}, Action: ActionAllow},
		{Match: Match{Host: "api.anthropic.com"}, Action: ActionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}

	if r := c.Match("api.openai.com", 443, "GET", "/v1/models"); r == nil || r.Action != ActionAllow {
		t.Errorf("expected allow for api.openai.com, got %v", r)
	}
	if r := c.Match("api.anthropic.com", 443, "GET", "/"); r == nil || r.Action != ActionDeny {
		t.Errorf("expected deny for api.anthropic.com, got %v", r)
	}
	if r := c.Match("example.com", 443, "GET", "/"); r != nil {
		t.Errorf("expected no match for example.com, got %v", r)
	}
}

func TestSuffixWildcardMatch(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: "*.github.com"}, Action: ActionInject},
		{Match: Match{Host: "*.api.github.com"}, Action: ActionAllow}, // more specific
	})
	if err != nil {
		t.Fatal(err)
	}

	if r := c.Match("raw.github.com", 443, "GET", "/"); r == nil || r.Action != ActionInject {
		t.Errorf("expected inject for raw.github.com, got %v", r)
	}
	// More specific suffix wins.
	if r := c.Match("foo.api.github.com", 443, "GET", "/"); r == nil || r.Action != ActionAllow {
		t.Errorf("expected allow for foo.api.github.com, got %v", r)
	}
	// Bare apex doesn't match a wildcard suffix.
	if r := c.Match("github.com", 443, "GET", "/"); r != nil {
		t.Errorf("expected no match for bare github.com, got %v", r)
	}
}

func TestExactBeatsSuffix(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: "*.github.com"}, Action: ActionInject},
		{Match: Match{Host: "raw.github.com"}, Action: ActionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Match("raw.github.com", 443, "GET", "/"); r == nil || r.Action != ActionDeny {
		t.Errorf("exact host should win, got %v", r)
	}
}

func TestRegexHost(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: `/^[a-z]+\.evil\.test$/`}, Action: ActionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Match("abc.evil.test", 443, "GET", "/"); r == nil || r.Action != ActionDeny {
		t.Errorf("expected deny match via regex, got %v", r)
	}
	if r := c.Match("abc.good.test", 443, "GET", "/"); r != nil {
		t.Errorf("expected no match for good host, got %v", r)
	}
}

func TestInvalidRegex(t *testing.T) {
	_, err := Compile([]Rule{
		{Match: Match{Host: "/[/"}, Action: ActionAllow},
	})
	if err == nil {
		t.Fatal("expected compile error for invalid regex")
	}
}

func TestMethodAndPath(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: "api.example.com", Method: "POST", PathPrefix: "/v1/secret"}, Action: ActionDeny},
		{Match: Match{Host: "api.example.com"}, Action: ActionAllow},
	})
	if err != nil {
		t.Fatal(err)
	}

	if r := c.Match("api.example.com", 443, "POST", "/v1/secret/leak"); r == nil || r.Action != ActionDeny {
		t.Errorf("expected deny, got %v", r)
	}
	if r := c.Match("api.example.com", 443, "GET", "/v1/secret/leak"); r == nil || r.Action != ActionAllow {
		t.Errorf("expected allow on GET, got %v", r)
	}
	if r := c.Match("api.example.com", 443, "POST", "/v1/public"); r == nil || r.Action != ActionAllow {
		t.Errorf("expected allow on different path, got %v", r)
	}
}

func TestPriorityWithinBucket(t *testing.T) {
	c, err := Compile([]Rule{
		{ID: "first", Match: Match{Host: "api.example.com"}, Action: ActionAllow},
		{ID: "second", Match: Match{Host: "api.example.com"}, Action: ActionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := c.Match("api.example.com", 443, "GET", "/")
	if r == nil || r.ID != "first" {
		t.Errorf("expected first rule to win, got %v", r)
	}
}

func TestAnyHostRule(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{}, Action: ActionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Match("whatever.example", 443, "GET", "/"); r == nil || r.Action != ActionDeny {
		t.Errorf("expected catch-all deny, got %v", r)
	}
}

func TestCaseInsensitiveHost(t *testing.T) {
	c, err := Compile([]Rule{
		{Match: Match{Host: "API.EXAMPLE.COM"}, Action: ActionAllow},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Match("api.example.com", 443, "GET", "/"); r == nil || r.Action != ActionAllow {
		t.Errorf("expected case-insensitive match, got %v", r)
	}
}

func TestRulesRoundTrip(t *testing.T) {
	in := []Rule{
		{ID: "a", Match: Match{Host: "exact.test"}, Action: ActionAllow},
		{ID: "b", Match: Match{Host: "*.suffix.test"}, Action: ActionDeny},
		{ID: "c", Match: Match{Host: `/re\.test/`}, Action: ActionInject},
	}
	c, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	out := c.Rules()
	if len(out) != len(in) {
		t.Fatalf("round-trip length mismatch: got %d want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].ID != in[i].ID {
			t.Errorf("round-trip order mismatch at %d: got %s want %s", i, out[i].ID, in[i].ID)
		}
	}
}

// Benchmark the hot path: 1000 rules with mixed exact/suffix/regex, looking up
// a host that hits the exact map. Should stay O(1) per lookup.
func BenchmarkMatchExactHit(b *testing.B) {
	var rules []Rule
	for i := 0; i < 1000; i++ {
		rules = append(rules, Rule{
			Match:  Match{Host: "host" + itoa(i) + ".example.com"},
			Action: ActionAllow,
		})
	}
	c, err := Compile(rules)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Match("host500.example.com", 443, "GET", "/")
	}
}

func BenchmarkMatchSuffixHit(b *testing.B) {
	var rules []Rule
	for i := 0; i < 100; i++ {
		rules = append(rules, Rule{
			Match:  Match{Host: "*.zone" + itoa(i) + ".example.com"},
			Action: ActionAllow,
		})
	}
	c, err := Compile(rules)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Match("foo.zone50.example.com", 443, "GET", "/")
	}
}

func itoa(i int) string {
	// quick local int->string for benchmarks without importing strconv.
	if i == 0 {
		return "0"
	}
	var b strings.Builder
	for i > 0 {
		b.WriteByte(byte('0' + i%10))
		i /= 10
	}
	// reverse
	s := []byte(b.String())
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return string(s)
}
