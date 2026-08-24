package api

import (
	"net"
	"net/http"
	"strings"
)

// ipAllowlist restricts the operator console to known networks.
//
// An operator account is not scoped to a tenant — the whole point of the role is
// that it sees every customer — so /v1/operator is the most valuable surface on
// the deployment and it was reachable from the entire internet, protected by one
// password that had been a constant in the repository. Passwords get reused and
// phished; an address range does not.
//
// Empty means no restriction, which is right for development and wrong for a
// deployment on the public internet. Set OPERATOR_IP_ALLOWLIST to the office
// egress or the VPN range.
//
// The check runs before authentication, so a caller outside the allowlist cannot
// even attempt a login — no password guessing, no user enumeration, and nothing
// to rate limit.
type ipAllowlist struct {
	networks []*net.IPNet
}

// ParseIPAllowlist accepts addresses and CIDRs, comma or space separated:
//
//	203.0.113.7, 198.51.100.0/24, 2001:db8::/32
//
// A malformed entry is an error rather than a silent skip. A typo in an
// allowlist that quietly drops one range is how a control ends up looking
// enforced while a hole stays open.
//
// Named for what it is rather than for its first caller: the operator console
// was the first surface to need one, and the RCS carrier webhooks are the
// second — Airtel documents IP whitelisting in both directions, and a callback
// nobody signs is worth restricting to the networks it can legitimately come
// from.
func ParseIPAllowlist(raw string) (*ipAllowlist, error) {
	list := &ipAllowlist{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, err
			}
			list.networks = append(list.networks, network)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, &net.ParseError{Type: "IP address or CIDR", Text: entry}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		list.networks = append(list.networks,
			&net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return list, nil
}

func (a *ipAllowlist) permits(address string) bool {
	if a == nil || len(a.networks) == 0 {
		return true
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	for _, network := range a.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// restrictOperatorNetwork refuses /v1/operator from anywhere else.
//
// 404, not 403: a 403 confirms an operator console exists at this address,
// which is exactly the fact worth withholding from a scan. Everything else on
// the API is unaffected — customers reach it from wherever they are.
func (s *Server) restrictOperatorNetwork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/operator") &&
			!s.OperatorAllowlist.permits(clientIP(r)) {
			if s.Logger != nil {
				s.Logger.Warn("operator console refused off-network",
					"path", r.URL.Path, "ip", clientIP(r))
			}
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}
