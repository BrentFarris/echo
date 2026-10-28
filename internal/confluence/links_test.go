package confluence

import "testing"

func TestConfluenceCanonicalLinksRespectContextPath(t *testing.T) {
	for _, test := range []struct{ deployment, base, link, want string }{
		{"datacenter", "https://docs.example/confluence", "/display/ENG/Guide", "https://docs.example/confluence/display/ENG/Guide"},
		{"datacenter", "https://docs.example/confluence", "/confluence/display/ENG/Guide", "https://docs.example/confluence/display/ENG/Guide"},
		{"datacenter", "https://docs.example/confluence", "/confluence/download/attachments/42/doc.pdf", "https://docs.example/confluence/download/attachments/42/doc.pdf"},
		{"cloud", "https://site.atlassian.net", "/wiki/spaces/ENG/pages/42/Guide", "https://site.atlassian.net/wiki/spaces/ENG/pages/42/Guide"},
		{"cloud", "https://site.atlassian.net", "/spaces/ENG/pages/42/Guide", "https://site.atlassian.net/wiki/spaces/ENG/pages/42/Guide"},
		{"cloud", "https://site.atlassian.net", "javascript:alert(1)", ""},
	} {
		t.Run(test.link, func(t *testing.T) {
			client := &Client{Config: Config{Deployment: test.deployment, URL: test.base}}
			if got := client.webURL(test.link); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}
