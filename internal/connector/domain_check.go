package connector

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"time"
)

// DomainChecker reads what the world says about a tenant's link domain: its
// DNS records, and whether it serves HTTPS under its own name.
type DomainChecker interface {
	CNAME(ctx context.Context, host string) (string, error)
	TXT(ctx context.Context, host string) ([]string, error)
	ServesHTTPS(ctx context.Context, host string) bool
}

// NetDomainChecker asks the system resolver and dials the host.
type NetDomainChecker struct{}

func (NetDomainChecker) CNAME(ctx context.Context, host string) (string, error) {
	cname, err := net.DefaultResolver.LookupCNAME(ctx, host)
	return strings.TrimSuffix(strings.ToLower(cname), "."), err
}

func (NetDomainChecker) TXT(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, host)
}

// ServesHTTPS is a TLS handshake that verifies the certificate names host.
func (NetDomainChecker) ServesHTTPS(ctx context.Context, host string) bool {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
