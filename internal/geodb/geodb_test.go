package geodb

import "testing"

func TestRegion(t *testing.T) {
	cases := []struct {
		ip   string
		want string // substring we expect in the display string
	}{
		{"8.8.8.8", "美国"},              // Google DNS (US, xdb labels Level3)
		{"114.114.114.114", "南京"},        // 江苏南京
		{"223.5.5.5", "阿里"},              // 阿里 DNS
		{"115.199.174.92", ""},            // not asserted; must at least not panic
		{"::1", ""},                       // IPv6 unsupported -> ""
		{"not-an-ip", ""},                 // invalid -> ""
	}
	for _, c := range cases {
		got := Region(c.ip)
		if c.want != "" && !contains(got, c.want) {
			t.Errorf("Region(%q) = %q, want substring %q", c.ip, got, c.want)
		}
	}
	t.Logf("8.8.8.8   -> %q", Region("8.8.8.8"))
	t.Logf("223.5.5.5 -> %q", Region("223.5.5.5"))
	t.Logf("115.199.174.92 -> %q", Region("115.199.174.92"))
	t.Logf("127.0.0.1 -> %q", Region("127.0.0.1"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
