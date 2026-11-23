package compliance

import "testing"

// Batch 5 rows S1 to S7, as the rule itself.
func TestApprovedLinks(t *testing.T) {
	approved := []string{"acme.example", "https://www.shop.example/path", "bit.ly"}
	cases := []struct {
		text    string
		refused string
	}{
		{"No link at all", ""},
		{"Visit https://acme.example/offer", ""},
		{"Visit https://www.acme.example/offer", ""},                        // S5: www. is the same host
		{"Visit https://shop.example/x.", ""},                               // listed with www and a path
		{"Visit https://evil.example/x", "evil.example"},                    // S1
		{"Visit https://deals.acme.example/x", "deals.acme.example"},        // S4: a subdomain is another host
		{"Visit https://bit.ly/abc", "bit.ly"},                              // S6: a listed shortener is still refused
		{"Visit https://ACME.example/a and https://x.example", "x.example"}, // case-insensitive, first bad one
	}
	for _, tc := range cases {
		if got, _ := UnapprovedLink(tc.text, approved); got != tc.refused {
			t.Errorf("%q refused %q, want %q", tc.text, got, tc.refused)
		}
	}
	if got, _ := UnapprovedLink("https://acme.example", nil); got != "acme.example" {
		t.Errorf("an empty list refused %q, want every link", got)
	}
}
