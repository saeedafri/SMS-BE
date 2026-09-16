package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/platform/secrets"
	"github.com/saeedafri/sms-be/internal/store"
)

// RCS operator accounts, managed here for the same reason the staff accounts
// are: the values are credentials, and a shell on the box is a stronger
// boundary than any endpoint. Nothing prints a secret back.

func rcsConnectionUsage() error {
	fmt.Fprint(os.Stderr, `operator-admin rcs-connection — RCS operator accounts

  rcs-connection list
  rcs-connection add <airtel|vi|jio|google> <label> [--environment live|test]
  rcs-connection add-assistant <connection-uuid> <assistant-id>   (jio)
  rcs-connection drop-assistant <connection-uuid> <assistant-id>  (jio)
  rcs-connection enable <connection-uuid>
  rcs-connection disable <connection-uuid>
  rcs-connection test <connection-uuid> <assistant-or-agent-id> <+91number>

Secrets are prompted for, never passed as arguments, and never printed.
`)
	return errors.New("no rcs-connection command given")
}

func runRCSConnection(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, args []string) error {
	if len(args) == 0 {
		return rcsConnectionUsage()
	}
	server := &api.Server{Secrets: box}
	switch args[0] {
	case "list":
		return listRCSConnections(ctx, pool, server)
	case "add":
		if len(args) < 3 {
			return rcsConnectionUsage()
		}
		return addRCSConnection(ctx, pool, box, args[1], args[2], flagValue(args[3:], "--environment", "live"))
	case "add-assistant", "drop-assistant":
		if len(args) < 3 {
			return rcsConnectionUsage()
		}
		return changeJioAssistant(ctx, pool, server, box, args[1], args[2], args[0] == "add-assistant")
	case "enable", "disable":
		if len(args) < 2 {
			return rcsConnectionUsage()
		}
		return setRCSConnectionStatus(ctx, pool, server, args[1], args[0])
	case "test":
		if len(args) < 4 {
			return rcsConnectionUsage()
		}
		return testRCSConnection(ctx, pool, server, args[1], args[2], args[3])
	default:
		return rcsConnectionUsage()
	}
}

func listRCSConnections(ctx context.Context, pool *pgxpool.Pool, server *api.Server) error {
	connections, err := store.ListRCSConnections(ctx, pool, nil)
	if err != nil {
		return err
	}
	if len(connections) == 0 {
		fmt.Println("no RCS operator accounts yet — add one with: operator-admin rcs-connection add jio \"Jio JBM\"")
		return nil
	}
	for _, c := range connections {
		detail := ""
		if problem := rcsProblem(server, c); problem != "" {
			detail = "  (" + problem + ")"
		}
		if c.LastError != nil && *c.LastError != "" {
			detail = "  (" + *c.LastError + ")"
		}
		fmt.Printf("%s  %-8s %-6s %-9s %-9s %s%s\n", c.ID, c.Vendor, c.Environment,
			c.Status, c.HealthStatus, c.Label, detail)
		if c.Vendor == "jio" {
			if assistants := jioAssistants(server, c); len(assistants) > 0 {
				fmt.Printf("    assistants: %s\n", strings.Join(assistants, ", "))
			} else {
				fmt.Println("    assistants: none yet — add one with rcs-connection add-assistant")
			}
		}
	}
	return nil
}

// rcsProblem is why the account cannot send yet, read without printing
// anything it holds.
func rcsProblem(server *api.Server, c store.RCSConnection) string {
	held, err := server.OpenRCSSecrets(c)
	if err != nil {
		return err.Error()
	}
	return api.RCSConnectionProblem(c.Vendor, c.Settings, held)
}

func jioAssistants(server *api.Server, c store.RCSConnection) []string {
	held, err := server.OpenRCSSecrets(c)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(held.Assistants))
	for id := range held.Assistants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func addRCSConnection(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box,
	vendor, label, environment string) error {

	vendor = strings.ToLower(strings.TrimSpace(vendor))
	keys, known := api.RCSSettingKeys[vendor]
	if !known {
		return errors.New("vendor must be airtel, vi, jio or google")
	}
	if environment != "live" && environment != "test" {
		return errors.New("environment must be live or test")
	}
	if box == nil {
		return errors.New("no CONNECTION_ENCRYPTION_KEY is set, so operator secrets cannot be stored")
	}

	settings := map[string]string{}
	for _, key := range keys {
		value := ask(fmt.Sprintf("%s (blank keeps the default)", key))
		if value != "" {
			settings[key] = value
		}
	}
	held := api.RCSSecrets{}
	switch vendor {
	case "airtel":
		token, err := askSecret("airtel auth token (the base64 blob Airtel issued)")
		if err != nil {
			return err
		}
		held.AuthToken = token
	case "vi":
		secret, err := askSecret("vi client secret")
		if err != nil {
			return err
		}
		held.ClientSecret = secret
	case "google":
		path := ask("path to the service account json file")
		key, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		held.ServiceAccountJSON = string(key)
	case "jio":
		// Jio's secrets are per assistant and arrive when a brand launches, so
		// an account starts with none.
		held.Assistants = map[string]string{}
	}
	if problem := api.RCSConnectionProblem(vendor, settings, held); problem != "" {
		return errors.New(problem)
	}

	sealed, err := sealRCSSecrets(box, held)
	if err != nil {
		return err
	}
	created, err := store.CreateRCSConnection(ctx, pool, store.RCSConnection{
		Label: label, Vendor: vendor, Environment: environment,
		Settings: settings, SecretsSealed: &sealed,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created %s (%s, %s), disabled\n", created.ID, vendor, environment)
	if vendor == "jio" {
		fmt.Printf("next: operator-admin rcs-connection add-assistant %s <assistant-id>\n", created.ID)
	} else {
		fmt.Printf("next: operator-admin rcs-connection enable %s\n", created.ID)
	}
	return nil
}

// changeJioAssistant adds or removes one assistant's secret.
func changeJioAssistant(ctx context.Context, pool *pgxpool.Pool, server *api.Server,
	box *secrets.Box, id, assistant string, add bool) error {

	connection, err := rcsConnectionByID(ctx, pool, id)
	if err != nil {
		return err
	}
	if connection.Vendor != "jio" {
		return errors.New("only a jio account holds per-assistant secrets")
	}
	held, err := server.OpenRCSSecrets(connection)
	if err != nil {
		return err
	}
	if held.Assistants == nil {
		held.Assistants = map[string]string{}
	}
	assistant = strings.TrimSpace(assistant)
	if add {
		secret, err := askSecret("secret key for assistant " + assistant)
		if err != nil {
			return err
		}
		held.Assistants[assistant] = secret
	} else {
		if _, found := held.Assistants[assistant]; !found {
			return fmt.Errorf("this account holds no secret for %s", assistant)
		}
		delete(held.Assistants, assistant)
	}
	sealed, err := sealRCSSecrets(box, held)
	if err != nil {
		return err
	}
	if err := store.SetRCSConnectionSecrets(ctx, pool, connection.ID, sealed); err != nil {
		return err
	}
	if add {
		fmt.Printf("assistant %s stored; sends under it pick it up within a minute\n", assistant)
		fmt.Printf("next: launch the brand's agent on Jio — operator-admin rcs-launch <agent-uuid> JIO %s\n", assistant)
		return nil
	}
	fmt.Printf("assistant %s removed\n", assistant)
	return nil
}

func setRCSConnectionStatus(ctx context.Context, pool *pgxpool.Pool, server *api.Server,
	id, action string) error {

	connection, err := rcsConnectionByID(ctx, pool, id)
	if err != nil {
		return err
	}
	status := "disabled"
	if action == "enable" {
		status = "active"
		if problem := rcsProblem(server, connection); problem != "" {
			return errors.New("this account cannot send yet: " + problem)
		}
		if _, err := server.RCSGateway(connection); err != nil {
			return err
		}
	}
	updated, err := store.SetRCSConnectionStatus(ctx, pool, connection.ID, status)
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("another %s account is already active in %s; disable it first",
			connection.Vendor, connection.Environment)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s is now %s; the API picks it up within a minute\n", updated.Label, updated.Status)
	if status == "active" {
		fmt.Printf("point %s's webhook at /v1/carrier-webhooks/rcs/%s/<RCS_WEBHOOK_TOKEN>\n",
			updated.Vendor, updated.Vendor)
	}
	return nil
}

// testRCSConnection asks the operator whether one handset can receive RCS.
// A real call with the stored credentials, and it changes no status.
func testRCSConnection(ctx context.Context, pool *pgxpool.Pool, server *api.Server,
	id, agentID, msisdn string) error {

	connection, err := rcsConnectionByID(ctx, pool, id)
	if err != nil {
		return err
	}
	gateway, err := server.RCSGateway(connection)
	if err != nil {
		return err
	}
	capability, err := gateway.Capability(ctx, agentID, msisdn)
	health, detail := "ok", ""
	if err != nil {
		health, detail = "error", err.Error()
	}
	var lastError *string
	if detail != "" {
		lastError = &detail
	}
	if writeErr := store.RecordRCSConnectionHealth(ctx, pool, connection.ID, health, lastError); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s answered: reachable=%t features=%v\n",
		connection.Vendor, capability.Reachable, capability.Features)
	return nil
}

func rcsConnectionByID(ctx context.Context, pool *pgxpool.Pool, id string) (store.RCSConnection, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return store.RCSConnection{}, errors.New("give the connection's uuid — operator-admin rcs-connection list")
	}
	connection, err := store.GetRCSConnection(ctx, pool, parsed)
	if errors.Is(err, store.ErrNotFound) {
		return store.RCSConnection{}, fmt.Errorf("no RCS connection %s", parsed)
	}
	return connection, err
}

func sealRCSSecrets(box *secrets.Box, held api.RCSSecrets) (string, error) {
	if box == nil {
		return "", errors.New("no CONNECTION_ENCRYPTION_KEY is set, so operator secrets cannot be stored")
	}
	plain, err := json.Marshal(held)
	if err != nil {
		return "", err
	}
	return box.Encrypt(string(plain))
}

// rcsLaunchCarriers is every operator an agent can be launched on, from the
// adapters this build holds.
func rcsLaunchCarriers() []string {
	carriers := make([]string, 0, len(connector.RCSIntegrations))
	for _, carrier := range connector.RCSIntegrations {
		carriers = append(carriers, carrier)
	}
	sort.Strings(carriers)
	return carriers
}

func ask(question string) string {
	fmt.Fprint(os.Stderr, question+": ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// askSecret reads a credential without echoing it, once: unlike a password
// being chosen, this one is copied from an operator's portal and a mistyped
// value fails visibly on the next test rather than silently.
func askSecret(question string) (string, error) {
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		return "", errors.New("refusing to read a secret from a pipe — run this on a terminal")
	}
	fmt.Fprint(os.Stderr, question+": ")
	value, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(value))) == 0 {
		return "", errors.New("that was empty")
	}
	return strings.TrimSpace(string(value)), nil
}

func flagValue(args []string, name, fallback string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return fallback
}
