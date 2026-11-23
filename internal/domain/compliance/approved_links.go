package compliance

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// CTAWhitelistObject is the registration object that lists the hosts a
// message may link to.
const CTAWhitelistObject = "cta_whitelist"

// LinkHost is the host a link or listed value names, the way both the
// operator and our list compare them: lowercased, without a leading "www.".
// A subdomain is a different host. "" when the value names no host.
func LinkHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\n") {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.Contains(parsed.Hostname(), ".") {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

func isShortener(host string) bool {
	for _, shortener := range shortenerHosts {
		if host == shortener || strings.HasSuffix(host, "."+shortener) {
			return true
		}
	}
	return false
}

var linkPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// LinksIn are the links a text carries, trailing punctuation trimmed.
func LinksIn(text string) []string {
	var out []string
	for _, link := range linkPattern.FindAllString(text, -1) {
		out = append(out, strings.TrimRight(link, ".,;:!?)]}"))
	}
	return out
}

// UnapprovedLink returns the first link in text that the approved hosts do not
// cover, and why, or "" when every link is approved. An empty list refuses
// every link; a text with no link needs no list; a public shortener is
// refused even when listed.
func UnapprovedLink(text string, approved []string) (host, reason string) {
	allowed := make(map[string]bool, len(approved))
	for _, entry := range approved {
		allowed[LinkHost(entry)] = true
	}
	for _, link := range LinksIn(text) {
		host := LinkHost(link)
		switch {
		case host == "":
			return link, "Enter a full, valid URL (https://...)."
		case isShortener(host):
			return host, "URL shorteners are not allowed under DLT. Use the full URL."
		case !allowed[host]:
			return host, fmt.Sprintf("%s is not on your approved links. Add this exact host "+
				"under Compliance, in your DLT account first.", host)
		}
	}
	return "", ""
}
