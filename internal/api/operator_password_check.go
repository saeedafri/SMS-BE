package api

import (
	"context"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"
)

// seededOperatorPassword is the password internal/demoseed gives the fixture's
// operator, and it is published in this repository. That is correct for a demo
// box and indefensible anywhere else.
const seededOperatorPassword = "relay-ops-dev"

// WarnOnPublishedOperatorPassword says so, loudly, at boot when a staff account
// still accepts the password anyone can read in this repository.
//
// It cannot fix the problem: changing a password without being asked would lock
// out whoever is using it, and refusing to start would take the platform down to
// protect an account that may already be fine. So it reports — with the address,
// so the fix is one command away — and every start is a fresh reminder until
// somebody runs it.
//
// This exists because "rotate the seeded password" is the kind of task that
// stays open forever precisely because nothing ever mentions it again.
func WarnOnPublishedOperatorPassword(ctx context.Context, server *Server) {
	if server == nil || server.DB == nil || server.Logger == nil {
		return
	}
	emails, err := store.ListOperatorEmails(ctx, server.DB)
	if err != nil {
		return
	}
	published, unprotected := 0, 0
	for _, email := range emails {
		operator, hash, err := store.FindOperatorByEmail(ctx, server.DB, email)
		if err != nil {
			continue
		}
		if auth.VerifyPassword(hash, seededOperatorPassword) {
			published++
			server.Logger.Warn(
				"an operator account still uses the password published in this repository — "+
					"it sees every customer on the platform. Rotate it with: "+
					"operator-admin set-password "+email,
				"operator", email)
		}
		state, err := store.LoadOperatorForMfa(ctx, server.DB, operator.OperatorID)
		if err == nil && !state.Enabled {
			unprotected++
		}
	}

	// The console's exposure is the combination, not any one setting: an
	// allowlist makes a weak password survivable, and a second factor makes an
	// open network survivable. Neither, and one guessed password is every
	// customer's data.
	//
	// Reported at ERROR when both are missing, because a warning among warnings
	// is how this stayed open in the first place. It does not refuse to start:
	// taking the platform down to protect an account that may already be fine is
	// the wrong trade, and the operator cannot fix the config from a process
	// that will not boot.
	open := server.OperatorAllowlist == nil || len(server.OperatorAllowlist.networks) == 0
	if open && unprotected > 0 {
		server.Logger.Error(
			"the operator console is reachable from any address AND has staff without a "+
				"second factor. Set OPERATOR_IP_ALLOWLIST to your office or VPN range, or "+
				"have those accounts enrol at /admin/security.",
			"operators_without_mfa", unprotected,
			"accounts_with_published_password", published)
		return
	}
	if open {
		server.Logger.Warn(
			"the operator console is reachable from any address; every staff account has a " +
				"second factor. Set OPERATOR_IP_ALLOWLIST to narrow it further.")
	}
}
