package hunk

import "testing"

func TestApplyLineMappings(t *testing.T) {
	for _, tc := range []struct {
		name, before, source, want string
		destination, replacement   Range
	}{
		{"replace", "one\ntwo\nthree\n", "one\nTWO\nTHREE\n", "one\nTWO\nthree\n", Range{2, 3}, Range{2, 3}},
		{"insert beginning", "one\n", "new\none\n", "new\none\n", Range{1, 1}, Range{1, 2}},
		{"insert end", "one\n", "one\ntwo\n", "one\ntwo\n", Range{2, 2}, Range{2, 3}},
		{"delete end", "one\ntwo", "one", "one", Range{2, 3}, Range{2, 2}},
		{"add final newline", "one", "one\n", "one\n", Range{2, 2}, Range{2, 3}},
		{"remove final newline", "one\n", "one", "one", Range{2, 3}, Range{2, 2}},
		{"empty model", "", "new\n", "new\n", Range{1, 1}, Range{1, 2}},
		{"CRLF", "one\r\ntwo\r\n", "one\nTWO\n", "one\r\nTWO\r\n", Range{2, 3}, Range{2, 3}},
		{"mixed EOL", "one\r\ntwo\nthree\r\n", "one\r\nTWO\r\nthree\r\n", "one\r\nTWO\nthree\r\n", Range{2, 3}, Range{2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eol := "lf"
			if tc.name == "CRLF" {
				eol = "crlf"
			}
			got, err := Apply(Side{Content: tc.before, Exists: true, EOL: eol, HasBOM: true}, Side{Content: tc.source, Exists: true, EOL: "lf"}, tc.destination, tc.replacement)
			if err != nil || got.Content != tc.want || !got.HasBOM {
				t.Fatalf("got %#v, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, r := range []Range{{0, 1}, {1, 0}, {1, 99}, {-1, 1}} {
		if _, err := Apply(Side{}, Side{}, r, Range{1, 2}); err == nil {
			t.Fatalf("accepted invalid range %#v", r)
		}
	}
}

func TestApplyExistenceAndToken(t *testing.T) {
	got, err := Apply(Side{Content: "file\n", Exists: true}, Side{}, Range{1, 2}, Range{1, 1})
	if err != nil || got.Exists {
		t.Fatalf("delete: %#v, %v", got, err)
	}
	if Token("repo", "path", Side{Content: "a"}) == Token("repo", "path", Side{Content: "b"}) {
		t.Fatal("token ignored content")
	}
}
