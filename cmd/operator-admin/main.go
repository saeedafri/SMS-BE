// Command operator-admin creates, lists, disables and re-passwords the platform
// staff who sign in at /operator/login.
//
// A CLI run on the server, deliberately, rather than an HTTP endpoint.
//
// An operator is not scoped to a tenant — the whole point of the role is that it
// sees every customer. So "create an operator" is the single most valuable
// request an attacker could make, and an endpoint offering it would have to be
// defended forever. There is no such endpoint, and this exists so that there
// never needs to be one: creating staff requires a shell on the box, which is
// already the strongest boundary in the deployment.
//
// Before this, operator_users was written in exactly ONE place in the entire
// backend — the demo fixture in internal/demoseed. That meant production had a
// single shared account whose password was a constant in the repository, with
// no way to add a colleague, rotate the password, or revoke anyone.
//
//	operator-admin list
//	operator-admin create ops@company.com "Ops Team" [--role admin]
//	operator-admin set-password ops@company.com
//	operator-admin disable ops@company.com
//	operator-admin credit-wallet owner@customer.com INR 500000.00 UTR123456789
//	operator-admin rcs-launch <agent-uuid> GOOGLE relay-test_abc_agent@rbm.goog
//
// The password is never taken as an argument. It is read from the terminal
// without echo, because an argument is visible in `ps`, in shell history, and
// in the audit log of whatever ran it.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/platform/config"
	"github.com/saeedafri/sms-be/internal/store"
)

const minOperatorPassword = 12

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, `operator-admin — manage platform staff accounts

  operator-admin list
  operator-admin create <email> <name> [--role admin|operator]
  operator-admin set-password <email>
  operator-admin disable <email>
  operator-admin enable <email>
  operator-admin credit-wallet <account-owner-email> <currency> <amount> <bank-reference>
  operator-admin rcs-launch <agent-uuid> <AIRTEL|VI|GOOGLE> <carrier-agent-id>

The password is prompted for, never passed as an argument.
`)
	return errors.New("no command given")
}

func run() error {
	if len(os.Args) < 2 {
		return usage()
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The admin role, for the same reason seed-demo uses it: operator_users is
	// not tenant-scoped, so the application role has no path to it.
	url := os.Getenv("DATABASE_ADMIN_URL")
	if url == "" {
		url = cfg.DatabaseURL
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "list":
		return list(ctx, pool)
	case "create":
		if len(os.Args) < 4 {
			return usage()
		}
		return create(ctx, pool, os.Args[2], os.Args[3], roleFlag(os.Args[4:]))
	case "set-password":
		if len(os.Args) < 3 {
			return usage()
		}
		return setPassword(ctx, pool, os.Args[2])
	case "disable":
		if len(os.Args) < 3 {
			return usage()
		}
		return setEnabled(ctx, pool, os.Args[2], false)
	case "enable":
		if len(os.Args) < 3 {
			return usage()
		}
		return setEnabled(ctx, pool, os.Args[2], true)
	case "rcs-launch":
		if len(os.Args) < 5 {
			return usage()
		}
		return launchRCSAgent(ctx, pool, os.Args[2], os.Args[3], os.Args[4])
	case "credit-wallet":
		if len(os.Args) < 6 {
			return usage()
		}
		return creditWallet(ctx, pool, os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	default:
		return usage()
	}
}

// creditWallet books a bank transfer into a customer's wallet.
//
// The only way money enters a wallet until a payment provider is configured.
// The bank reference is written into the ledger entry and a reference already
// booked is refused, so a transfer run twice by mistake cannot be credited
// twice. The operator confirms the tenant and amount on a terminal first.
func creditWallet(ctx context.Context, pool *pgxpool.Pool,
	email, currency, amount, reference string) error {

	currency = strings.ToUpper(strings.TrimSpace(currency))
	reference = strings.TrimSpace(reference)
	minor, err := parseAmount(amount)
	if err != nil {
		return err
	}
	if len(reference) < 6 {
		return errors.New("give the bank's transfer reference (UTR), at least 6 characters")
	}

	var tenantID uuid.UUID
	var tenantName string
	if err := pool.QueryRow(ctx, `
		SELECT t.id, t.name FROM users u
		JOIN tenant_users tu ON tu.user_id = u.id AND tu.role = 'owner'
		JOIN tenants t ON t.id = tu.tenant_id
		WHERE u.email = $1`, strings.TrimSpace(email)).Scan(&tenantID, &tenantName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%s owns no account", email)
		}
		return err
	}

	description := "Bank transfer " + reference
	var booked bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wallet_ledger
		WHERE tenant_id = $1 AND description = $2)`, tenantID, description).Scan(&booked); err != nil {
		return err
	}
	if booked {
		return fmt.Errorf("reference %s is already credited to %s", reference, tenantName)
	}

	if !confirm(fmt.Sprintf("Credit %s %s to %q (%s), reference %s?",
		currency, amount, tenantName, tenantID, reference)) {
		return errors.New("not confirmed; nothing credited")
	}
	entry, err := store.AppendLedgerEntry(ctx, pool, store.Identity{TenantID: tenantID},
		store.LedgerEntry{Currency: currency, Type: "topup", AmountMinor: minor,
			Description: description})
	if err != nil {
		return err
	}
	fmt.Printf("credited; %s balance is now %d minor units (entry %s)\n",
		currency, entry.BalanceAfterMinor, entry.ID)
	return nil
}

// launchRCSAgent records that a carrier admitted a customer's agent, with the
// id that carrier issued for it — what every RCS send on that carrier goes out
// under. Nothing else writes an approved launch: a carrier's approval arrives
// by email or in its portal, and for a Google RBM test agent the id is shown
// in Google's console.
func launchRCSAgent(ctx context.Context, pool *pgxpool.Pool,
	agent, carrier, carrierAgentID string) error {

	agentID, err := uuid.Parse(strings.TrimSpace(agent))
	if err != nil {
		return errors.New("agent must be the Relay agent's uuid")
	}
	carrier = strings.ToUpper(strings.TrimSpace(carrier))
	if !slices.Contains([]string{"AIRTEL", "VI", "GOOGLE"}, carrier) {
		return errors.New("carrier must be AIRTEL, VI or GOOGLE")
	}
	carrierAgentID = strings.TrimSpace(carrierAgentID)
	if carrierAgentID == "" {
		return errors.New("give the id the carrier issued for this agent")
	}

	var tenantID uuid.UUID
	var name, status, verification string
	if err := pool.QueryRow(ctx, `SELECT tenant_id, display_name, status, verification_status
		FROM rcs_agents WHERE id = $1`, agentID).Scan(&tenantID, &name, &status, &verification); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no agent %s", agentID)
		}
		return err
	}
	if verification != "approved" || status == "suspended" || status == "archived" {
		return fmt.Errorf("%q cannot launch: verification is %s and status is %s", name, verification, status)
	}
	if !confirm(fmt.Sprintf("Mark %q (%s) live on %s as %s?", name, agentID, carrier, carrierAgentID)) {
		return errors.New("not confirmed; nothing changed")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO rcs_agent_carrier_launches
		    (agent_id, tenant_id, carrier, status, carrier_agent_id, submitted_at, updated_at)
		VALUES ($1, $2, $3, 'approved', $4, now(), now())
		ON CONFLICT (agent_id, carrier) DO UPDATE
		    SET status = 'approved', carrier_agent_id = EXCLUDED.carrier_agent_id,
		        rejection_reason = NULL, updated_at = now()`,
		agentID, tenantID, carrier, carrierAgentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE rcs_agents SET status = 'live', updated_at = now()
		WHERE id = $1`, agentID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("%q is live on %s as %s (sends pick it up within seconds)\n", name, carrier, carrierAgentID)
	return nil
}

// parseAmount reads "5000" or "5000.50" into minor units, refusing anything
// that would round.
func parseAmount(amount string) (int64, error) {
	whole, fraction, _ := strings.Cut(strings.TrimSpace(amount), ".")
	if len(fraction) > 2 {
		return 0, errors.New("amount has more than two decimal places")
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	var minor int64
	for _, r := range whole + fraction {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("amount %q is not a number", amount)
		}
		minor = minor*10 + int64(r-'0')
		if minor > 1_000_000_000_00 {
			return 0, errors.New("amount is implausibly large")
		}
	}
	if minor <= 0 {
		return 0, errors.New("amount must be positive")
	}
	return minor, nil
}

func confirm(question string) bool {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return false
	}
	fmt.Fprint(os.Stderr, question+" Type yes: ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	return answer == "yes"
}

func roleFlag(args []string) string {
	for i, a := range args {
		if a == "--role" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "admin"
}

func list(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx,
		`SELECT email, name, role, created_at FROM operator_users ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Printf("%-34s %-22s %-10s %s\n", "EMAIL", "NAME", "ROLE", "CREATED")
	for rows.Next() {
		var email, name, role string
		var created any
		if err := rows.Scan(&email, &name, &role, &created); err != nil {
			return err
		}
		fmt.Printf("%-34s %-22s %-10s %v\n", email, name, role, created)
	}
	return rows.Err()
}

func create(ctx context.Context, pool *pgxpool.Pool, email, name, role string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return errors.New("that does not look like an email address")
	}
	// The values the operator_users CHECK constraint actually allows. Read from
	// the migration rather than assumed — the first draft of this file guessed
	// "readonly", which the constraint would have rejected at runtime.
	if role != "admin" && role != "operator" {
		return errors.New("role must be admin or operator")
	}

	// Refuse rather than silently reset. "create" quietly changing an existing
	// colleague's password is how someone loses access without being told.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT true FROM operator_users WHERE email = $1`, email).Scan(&exists); err == nil {
		return fmt.Errorf("%s already exists — use set-password to change it", email)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	password, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operator_users (id, email, name, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)`,
		uuid.New(), email, name, hash, role); err != nil {
		return err
	}
	fmt.Printf("created operator %s (%s)\n", email, role)
	return nil
}

func setPassword(ctx context.Context, pool *pgxpool.Pool, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	password, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx,
		`UPDATE operator_users SET password_hash = $2 WHERE email = $1`, email, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no operator with email %s", email)
	}
	// Every existing session is revoked. Someone changing a password because
	// they think it leaked expects whoever had it to be signed out.
	if _, err := pool.Exec(ctx, `
		DELETE FROM operator_sessions
		 WHERE operator_id = (SELECT id FROM operator_users WHERE email = $1)`, email); err != nil {
		return err
	}
	fmt.Printf("password changed for %s; existing sessions revoked\n", email)
	return nil
}

func setEnabled(ctx context.Context, pool *pgxpool.Pool, email string, enabled bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if enabled {
		return errors.New("re-enable by running set-password, which sets a new credential")
	}
	// Disabling scrambles the hash rather than deleting the row: the audit log
	// references the operator, and a deleted row would orphan the record of what
	// they did. A hash of random bytes matches no password.
	random := uuid.New().String() + uuid.New().String()
	hash, err := auth.HashPassword(random)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx,
		`UPDATE operator_users SET password_hash = $2 WHERE email = $1`, email, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no operator with email %s", email)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM operator_sessions
		 WHERE operator_id = (SELECT id FROM operator_users WHERE email = $1)`, email); err != nil {
		return err
	}
	fmt.Printf("disabled %s; sessions revoked, audit history kept\n", email)
	return nil
}

// readPassword prompts twice without echoing.
func readPassword() (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		return "", errors.New("refusing to read a password from a pipe — run this on a terminal")
	}
	fmt.Fprint(os.Stderr, "password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "again: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("those did not match")
	}
	if len(first) < minOperatorPassword {
		return "", fmt.Errorf("an operator password must be at least %d characters — this account sees every tenant",
			minOperatorPassword)
	}
	return string(first), nil
}
