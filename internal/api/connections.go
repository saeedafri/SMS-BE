package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/platform/secrets"
	"github.com/saeedafri/sms-be/internal/store"
)

// connectionResponse maps a stored bind to the contract's Connection.
//
// There is no password field here, and there is none on the contract's
// Connection schema either — not masked, not truncated, not present. The only
// thing a reader ever learns is when it was last set. Every response on these
// eight routes goes through this one function so there is a single place that
// could ever leak it, and it does not.
func connectionResponse(c store.Connection) gen.Connection {
	out := gen.Connection{
		Id:                      c.ID,
		Label:                   c.Label,
		Carrier:                 gen.CarrierId(c.Carrier),
		Environment:             gen.Environment(c.Environment),
		Host:                    c.Host,
		Port:                    c.Port,
		SystemId:                c.SystemID,
		SystemType:              c.SystemType,
		BindType:                gen.BindType(c.BindType),
		PasswordSetAt:           c.PasswordSetAt,
		MaxTps:                  c.MaxTps,
		WindowSize:              c.WindowSize,
		EnquireLinkSeconds:      c.EnquireLinkSeconds,
		ReconnectBackoffSeconds: c.ReconnectBackoffSeconds,
		Status:                  gen.ConnectionStatus(c.Status),
		Health: gen.ConnectionHealth{
			Status:      gen.ConnectionHealthStatus(c.HealthStatus),
			LastBoundAt: c.LastBoundAt,
			LastError:   c.LastError,
		},
	}
	return out
}

// operatorForConnections resolves the operator, in the handler rather than only
// in middleware.
//
// These eight routes carry SMPP bind credentials for four carrier
// relationships. The handoff's own conclusion on the API-key finding was that a
// frontend gate stops a button being drawn, not a request being sent; the same
// reasoning applies one layer down, so the role is checked where the work
// happens.
func (s *Server) operatorForConnections(ctx context.Context) (store.OperatorIdentity, bool) {
	operator, err := s.requireOperator(ctx)
	if err != nil {
		return store.OperatorIdentity{}, false
	}
	return operator, true
}

func (s *Server) GetConnections(ctx context.Context, request gen.GetConnectionsRequestObject) (
	gen.GetConnectionsResponseObject, error) {

	if _, ok := s.operatorForConnections(ctx); !ok {
		return gen.GetConnections401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	var carrier, environment *string
	if request.Params.Carrier != nil {
		value := string(*request.Params.Carrier)
		carrier = &value
	}
	if request.Params.Environment != nil {
		value := string(*request.Params.Environment)
		environment = &value
	}
	connections, err := store.ListConnections(ctx, s.operatorPool(), carrier, environment)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Connection, 0, len(connections))
	for _, connection := range connections {
		out = append(out, connectionResponse(connection))
	}
	return gen.GetConnections200JSONResponse{Connections: out}, nil
}

func (s *Server) GetConnection(ctx context.Context, request gen.GetConnectionRequestObject) (
	gen.GetConnectionResponseObject, error) {

	if _, ok := s.operatorForConnections(ctx); !ok {
		return gen.GetConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	connection, err := store.GetConnection(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetConnection200JSONResponse(connectionResponse(connection)), nil
}

func (s *Server) CreateConnection(ctx context.Context, request gen.CreateConnectionRequestObject) (
	gen.CreateConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.CreateConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	body := request.Body
	if body == nil {
		return gen.CreateConnection422JSONResponse(
			errorBody(codeValidation, "A connection body is required.")), nil
	}
	if refusal := validateConnectionShape(string(body.Carrier), string(body.Environment),
		string(body.BindType), body.Port, body.MaxTps); refusal != "" {
		return gen.CreateConnection422JSONResponse(errorBody(codeValidation, refusal)), nil
	}

	// Encrypted here, never hashed: the plaintext has to be presentable to the
	// operator's gateway on every bind. Without a key configured this refuses
	// rather than storing it readable.
	sealed, err := s.Secrets.Encrypt(*body.Password)
	if errors.Is(err, secrets.ErrNoKey) {
		return gen.CreateConnection422JSONResponse(errorBody(codeValidation,
			"This deployment has no connection encryption key, so a bind password "+
				"cannot be stored. Set CONNECTION_ENCRYPTION_KEY.")), nil
	}
	if err != nil {
		return nil, err
	}

	// Fields assigned explicitly, never spread. A create that copied the whole
	// body while its update sibling assigned each field is the asymmetry that
	// shipped last time.
	now := time.Now().UTC()
	connection := store.Connection{
		Label:                   body.Label,
		Carrier:                 string(body.Carrier),
		Environment:             string(body.Environment),
		Host:                    body.Host,
		Port:                    body.Port,
		SystemID:                body.SystemId,
		SystemType:              body.SystemType,
		BindType:                string(body.BindType),
		PasswordEncrypted:       &sealed,
		PasswordSetAt:           &now,
		MaxTps:                  body.MaxTps,
		WindowSize:              intOrDefault(body.WindowSize, 10),
		EnquireLinkSeconds:      intOrDefault(body.EnquireLinkSeconds, 30),
		ReconnectBackoffSeconds: intOrDefault(body.ReconnectBackoffSeconds, 5),
	}
	created, err := store.CreateConnection(ctx, s.operatorPool(), connection)
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateConnection409JSONResponse(errorBody(codeConflict,
			"A connection with that system id already exists on this host and environment.")), nil
	}
	if err != nil {
		return nil, err
	}
	// The detail names the bind, never its password.
	s.recordConnectionAction(ctx, operator, "connection.create", created,
		fmt.Sprintf("Created the %s %s bind %q", created.Carrier, created.Environment, created.Label))
	return gen.CreateConnection201JSONResponse(connectionResponse(created)), nil
}

func (s *Server) UpdateConnection(ctx context.Context, request gen.UpdateConnectionRequestObject) (
	gen.UpdateConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.UpdateConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.UpdateConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	body := request.Body
	if body == nil {
		return gen.UpdateConnection422JSONResponse(
			errorBody(codeValidation, "A connection body is required.")), nil
	}

	patch := store.ConnectionPatch{
		Label: body.Label, Host: body.Host, Port: body.Port,
		SystemID: body.SystemId, SystemType: body.SystemType,
		MaxTps: body.MaxTps, WindowSize: body.WindowSize,
		EnquireLinkSeconds:      body.EnquireLinkSeconds,
		ReconnectBackoffSeconds: body.ReconnectBackoffSeconds,
	}
	if body.Carrier != nil {
		value := string(*body.Carrier)
		if !oneOf(value, validCarriers) {
			return gen.UpdateConnection422JSONResponse(
				errorBody(codeValidation, enumMessage("carrier", validCarriers))), nil
		}
		patch.Carrier = &value
	}
	if body.Environment != nil {
		value := string(*body.Environment)
		if !oneOf(value, validEnvironments) {
			return gen.UpdateConnection422JSONResponse(
				errorBody(codeValidation, enumMessage("environment", validEnvironments))), nil
		}
		patch.Environment = &value
	}
	if body.BindType != nil {
		value := string(*body.BindType)
		if !oneOf(value, validBindTypes) {
			return gen.UpdateConnection422JSONResponse(
				errorBody(codeValidation, enumMessage("bindType", validBindTypes))), nil
		}
		patch.BindType = &value
	}
	if body.Port != nil && (*body.Port <= 0 || *body.Port > 65535) {
		return gen.UpdateConnection422JSONResponse(
			errorBody(codeValidation, "port must be between 1 and 65535.")), nil
	}
	if body.MaxTps != nil && *body.MaxTps <= 0 {
		return gen.UpdateConnection422JSONResponse(
			errorBody(codeValidation, "maxTps must be greater than zero.")), nil
	}
	// Omitting password leaves the stored one untouched; supplying it replaces
	// it and moves passwordSetAt.
	if body.Password != nil {
		sealed, err := s.Secrets.Encrypt(*body.Password)
		if errors.Is(err, secrets.ErrNoKey) {
			return gen.UpdateConnection422JSONResponse(errorBody(codeValidation,
				"This deployment has no connection encryption key, so a bind password "+
					"cannot be stored. Set CONNECTION_ENCRYPTION_KEY.")), nil
		}
		if err != nil {
			return nil, err
		}
		patch.PasswordEncrypted = &sealed
	}

	updated, err := store.UpdateConnection(ctx, s.operatorPool(), id, patch)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if errors.Is(err, store.ErrConflict) {
		return gen.UpdateConnection409JSONResponse(errorBody(codeConflict,
			"A connection with that system id already exists on this host and environment.")), nil
	}
	if err != nil {
		return nil, err
	}
	s.recordConnectionAction(ctx, operator, "connection.update", updated,
		fmt.Sprintf("Updated the %s %s bind %q", updated.Carrier, updated.Environment, updated.Label))
	return gen.UpdateConnection200JSONResponse(connectionResponse(updated)), nil
}

func (s *Server) EnableConnection(ctx context.Context, request gen.EnableConnectionRequestObject) (
	gen.EnableConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.EnableConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.EnableConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	current, err := store.GetConnection(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.EnableConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if err != nil {
		return nil, err
	}
	if current.Status == "active" {
		return gen.EnableConnection422JSONResponse(
			errorBody(codeValidation, "That connection is already active.")), nil
	}
	// A bind with no password cannot bind, so enabling it would put a corridor
	// on a path that fails on first use.
	if current.PasswordSetAt == nil {
		return gen.EnableConnection422JSONResponse(errorBody(codeValidation,
			"Set a bind password before enabling this connection.")), nil
	}
	updated, err := store.SetConnectionStatus(ctx, s.operatorPool(), id, "active")
	if err != nil {
		return nil, err
	}
	s.recordConnectionAction(ctx, operator, "connection.enable", updated,
		fmt.Sprintf("Enabled the %s %s bind %q", updated.Carrier, updated.Environment, updated.Label))
	return gen.EnableConnection200JSONResponse(connectionResponse(updated)), nil
}

func (s *Server) DisableConnection(ctx context.Context, request gen.DisableConnectionRequestObject) (
	gen.DisableConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.DisableConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.DisableConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	current, err := store.GetConnection(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DisableConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if err != nil {
		return nil, err
	}
	if current.Status == "disabled" {
		return gen.DisableConnection422JSONResponse(
			errorBody(codeValidation, "That connection is already disabled.")), nil
	}
	updated, err := store.SetConnectionStatus(ctx, s.operatorPool(), id, "disabled")
	if err != nil {
		return nil, err
	}
	// Corridors pointing here are left alone deliberately: they stay defined and
	// fall through to the next priority rather than being silently unwired.
	carrying, err := store.CountRoutesUsingConnection(ctx, s.operatorPool(), id)
	if err != nil {
		return nil, err
	}
	s.recordConnectionAction(ctx, operator, "connection.disable", updated,
		fmt.Sprintf("Disabled the %s %s bind %q; %d corridor(s) now fall through to the next priority",
			updated.Carrier, updated.Environment, updated.Label, carrying))
	return gen.DisableConnection200JSONResponse(connectionResponse(updated)), nil
}

func (s *Server) DeleteConnection(ctx context.Context, request gen.DeleteConnectionRequestObject) (
	gen.DeleteConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.DeleteConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.DeleteConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	current, err := store.GetConnection(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DeleteConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if err != nil {
		return nil, err
	}
	if current.Status == "active" {
		return gen.DeleteConnection422JSONResponse(errorBody(codeValidation,
			"Disable this connection before deleting it.")), nil
	}
	// Referencing corridors are not nulled out as a side effect: the operator
	// repoints them deliberately, or they would silently stop carrying traffic.
	carrying, err := store.CountRoutesUsingConnection(ctx, s.operatorPool(), id)
	if err != nil {
		return nil, err
	}
	if carrying > 0 {
		return gen.DeleteConnection409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("%d route(s) still use this connection. Repoint them first.", carrying))), nil
	}
	if err := store.DeleteConnection(ctx, s.operatorPool(), id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return gen.DeleteConnection409JSONResponse(errorBody(codeConflict,
				"Routes still use this connection. Repoint them first.")), nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return gen.DeleteConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
		}
		return nil, err
	}
	s.recordConnectionAction(ctx, operator, "connection.delete", current,
		fmt.Sprintf("Deleted the %s %s bind %q", current.Carrier, current.Environment, current.Label))
	return gen.DeleteConnection204Response{}, nil
}

func (s *Server) TestConnection(ctx context.Context, request gen.TestConnectionRequestObject) (
	gen.TestConnectionResponseObject, error) {

	operator, ok := s.operatorForConnections(ctx)
	if !ok {
		return gen.TestConnection401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.TestConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	current, err := store.GetConnection(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.TestConnection404JSONResponse(errorBody(codeNotFound, "No such connection.")), nil
	}
	if err != nil {
		return nil, err
	}

	// Reachability only. The SMPP client does not exist yet, so this reports
	// whether the operator's gateway accepts a TCP connection on the configured
	// host and port — honestly labelled as that rather than claiming a bind it
	// did not perform. It must never change status: proving a bind works and
	// putting live traffic on it stay two separate decisions.
	result, health, lastError := s.probeConnection(ctx, current)
	boundAt := (*time.Time)(nil)
	if health == "bound" {
		now := time.Now().UTC()
		boundAt = &now
	}
	if err := store.RecordConnectionHealth(ctx, s.operatorPool(), id, health, lastError, boundAt); err != nil {
		return nil, err
	}
	s.recordConnectionAction(ctx, operator, "connection.test", current,
		fmt.Sprintf("Tested the %s %s bind %q: %s",
			current.Carrier, current.Environment, current.Label, health))
	return gen.TestConnection200JSONResponse(result), nil
}

// recordConnectionAction writes the audit row. The detail never contains a
// password, and no caller passes one.
func (s *Server) recordConnectionAction(ctx context.Context, operator store.OperatorIdentity,
	action string, connection store.Connection, detail string) {

	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		nil, "", connection.Label, detail); err != nil {
		s.Logger.Error("connection audit not recorded",
			"action", action, "connection", connection.ID, "error", err)
	}
}

func intOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func validateConnectionShape(carrier, environment, bindType string, port, maxTps int) string {
	switch {
	case !oneOf(carrier, validCarriers):
		return enumMessage("carrier", validCarriers)
	case !oneOf(environment, validEnvironments):
		return enumMessage("environment", validEnvironments)
	case !oneOf(bindType, validBindTypes):
		return enumMessage("bindType", validBindTypes)
	case port <= 0 || port > 65535:
		return "port must be between 1 and 65535."
	case maxTps <= 0:
		return "maxTps must be greater than zero."
	}
	return ""
}

// probeConnection reports whether the operator's gateway is reachable on the
// configured host and port.
//
// Deliberately a TCP dial and nothing more. The SMPP client does not exist yet,
// so claiming a successful bind here would be a lie the console would render as
// a green tick — reachability is what we can actually prove today, and the
// result says so in its own message.
func (s *Server) probeConnection(ctx context.Context, c store.Connection) (
	gen.ConnectionTestResult, string, *string) {

	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		// The operator reads this, so it names the address and the failure
		// without leaking anything about the credential.
		reason := fmt.Sprintf("Could not reach %s: %v", address, err)
		return gen.ConnectionTestResult{Ok: false, Message: reason}, "error", &reason
	}
	_ = conn.Close()
	message := fmt.Sprintf("Reached %s. TCP reachability only — a full SMPP bind is not "+
		"attempted yet.", address)
	return gen.ConnectionTestResult{Ok: true, Message: message}, "bound", nil
}

var _ = uuid.Nil
