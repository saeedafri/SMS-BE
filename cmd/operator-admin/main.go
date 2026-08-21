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
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/platform/config"
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
	default:
		return usage()
	}
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
