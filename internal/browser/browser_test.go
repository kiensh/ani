package browser

import "testing"

func TestRejectNonWebURLs(t *testing.T) {
	for _, raw := range []string{"", "--help", "file:///tmp/test", "javascript:void(0)", "https://", "https://user@host.example/"} {
		if err := Open(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
