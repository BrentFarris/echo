package server

import (
	"strings"
	"testing"
)

func TestGitNetworkFailureDiagnostic(t *testing.T) {
	const dnsError = "fatal: unable to access 'https://github.com/example/repo.git/': Could not resolve host: github.com"
	for _, config := range []map[string]any{nil, {"internet": false, "privateNetwork": false}} {
		got := gitNetworkFailureDiagnostic(config, dnsError)
		if !strings.Contains(got, "Settings → Git") || !strings.Contains(got, "Internet access") || !strings.Contains(got, dnsError) {
			t.Fatalf("missing network-permission guidance or original failure: %s", got)
		}
	}
	for _, config := range []map[string]any{{"internet": true}, {"privateNetwork": true}} {
		if got := gitNetworkFailureDiagnostic(config, dnsError); got != dnsError {
			t.Fatalf("misidentified a granted network's DNS failure: %s", got)
		}
	}
	for _, failure := range []string{"fatal: not a git repository", "fatal: Authentication failed", "fatal: couldn't find remote ref missing", ""} {
		if got := gitNetworkFailureDiagnostic(nil, failure); got != failure {
			t.Fatalf("misidentified a non-network failure: %s", got)
		}
	}
}
