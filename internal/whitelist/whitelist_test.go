package whitelist

import (
	"net/netip"
	"testing"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseRuleNormalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{" 1.2.3.4 ", "1.2.3.4"},
		{"1.2.3.4", "1.2.3.4"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"10.1.2.0/24", "10.1.2.0/24"},
		{"192.168.1.7/24", "192.168.1.0/24"}, // host bits masked off
		{"::1", "::1"},
		{"2001:db8:1::/32", "2001:db8::/32"}, // masked
	}
	for _, c := range cases {
		got, err := ParseRule(c.in)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseRule(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRuleRejects(t *testing.T) {
	bad := []string{"", "   ", "1.2.3", "999.1.1.1", "10.0.0.0/99", "1.2.3.4.5", "abc", ":::", "10.0.0.0/8junk"}
	for _, in := range bad {
		if _, err := ParseRule(in); err == nil {
			t.Errorf("ParseRule(%q): expected error", in)
		}
	}
}

func TestContainsExactAndCIDR(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, r := range []string{"1.2.3.4", "10.0.0.0/8", "2001:db8::/32"} {
		if _, err := s.Add(r); err != nil {
			t.Fatalf("Add(%q): %v", r, err)
		}
	}
	allowed := []string{"1.2.3.4", "10.1.2.3", "10.255.255.255", "2001:db8::1", "2001:db8:ffff:ffff::1"}
	blocked := []string{"1.2.3.5", "11.0.0.1", "172.16.0.1", "2001:db9::1", "::1"}
	for _, a := range allowed {
		if !s.Contains(mustAddr(a)) {
			t.Errorf("Contains(%s): want allowed", a)
		}
	}
	for _, a := range blocked {
		if s.Contains(mustAddr(a)) {
			t.Errorf("Contains(%s): want blocked", a)
		}
	}
	// Invalid addr never allowed
	if s.Contains(netip.Addr{}) {
		t.Error("Contains(invalid): want false")
	}
}

func TestBoundaryMasks(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// /24: 192.168.1.255 allowed, 192.168.2.0 blocked
	if _, err := s.Add("192.168.1.1/24"); err != nil {
		t.Fatal(err)
	}
	if !s.Contains(mustAddr("192.168.1.255")) {
		t.Error("want .1.255 allowed inside /24")
	}
	if s.Contains(mustAddr("192.168.2.0")) {
		t.Error("want .2.0 blocked outside /24")
	}
}

func TestAddIdempotentAndDelete(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r1, err := s.Add("1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Add("1.2.3.4") // duplicate
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID != r2.ID {
		t.Fatalf("duplicate add returned different id: %d vs %d", r1.ID, r2.ID)
	}
	if s.Count() != 1 {
		t.Fatalf("count = %d, want 1", s.Count())
	}
	// delete removes it and clears the cache
	ok, err := s.Delete(r1.ID)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if s.Count() != 0 {
		t.Fatal("count after delete != 0")
	}
	if s.Contains(mustAddr("1.2.3.4")) {
		t.Fatal("cache not refreshed after delete")
	}
	// deleting again reports false
	ok, _ = s.Delete(r1.ID)
	if ok {
		t.Fatal("second delete reported true")
	}
}

func TestListOrderAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/wl.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("5.6.7.8"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("9.9.9.0/24"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// reopen: rows persist
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rules, err := s2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0].Rule != "5.6.7.8" || rules[1].Rule != "9.9.9.0/24" {
		t.Fatalf("unexpected list after reopen: %+v", rules)
	}
	if s2.Count() != 2 || !s2.Contains(mustAddr("9.9.9.200")) {
		t.Fatal("cache not loaded after reopen")
	}
}
