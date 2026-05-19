package protocol

import "net"

// privateCIDRs is the canonical list of address ranges that sandbox egress
// must not reach by default. Covers RFC1918, loopback, link-local (which
// includes cloud metadata at 169.254.169.254 — AWS/GCP/Azure all use it),
// "this network" 0.0.0.0/8, CGNAT 100.64.0.0/10 (often used for
// container/VPN inner networks), and the IPv6 equivalents.
var privateCIDRs = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
}

var privateNetworks = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(privateCIDRs))
	for _, c := range privateCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// IsPrivateIP reports whether ip falls into any of the canonical private
// or internal address ranges egress should refuse to reach by default.
func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range privateNetworks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsPrivateHost is the string-input convenience wrapper for IsPrivateIP.
// Returns false for hostnames (only IP literals are checked).
func IsPrivateHost(host string) bool {
	return IsPrivateIP(net.ParseIP(host))
}
