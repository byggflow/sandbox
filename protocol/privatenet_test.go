package protocol

import "testing"

func TestIsPrivateHost(t *testing.T) {
	private := []string{
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.1.1",
		"127.0.0.1",
		"169.254.169.254", // cloud metadata
		"::1",
		"fc00::1",
		"fe80::1",
	}
	public := []string{
		"8.8.8.8", "1.1.1.1", "172.32.0.1", "2606:4700:4700::1111",
	}
	notIP := []string{
		"api.openai.com", "", "not-an-ip",
	}
	for _, h := range private {
		if !IsPrivateHost(h) {
			t.Errorf("IsPrivateHost(%q) = false, want true", h)
		}
	}
	for _, h := range public {
		if IsPrivateHost(h) {
			t.Errorf("IsPrivateHost(%q) = true, want false", h)
		}
	}
	for _, h := range notIP {
		if IsPrivateHost(h) {
			t.Errorf("IsPrivateHost(%q) = true, want false (hostname, not IP)", h)
		}
	}
}
