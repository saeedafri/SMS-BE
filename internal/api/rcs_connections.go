package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// RCSSecrets is what rcs_connections.secrets_sealed holds once opened. Each
// vendor uses its own fields.
type RCSSecrets struct {
	// AuthToken is Airtel's Basic blob.
	AuthToken string `json:"authToken,omitempty"`
	// ClientSecret is Vi's OAuth secret.
	ClientSecret string `json:"clientSecret,omitempty"`
	// ServiceAccountJSON is Google's service account key.
	ServiceAccountJSON string `json:"serviceAccountJson,omitempty"`
	// Assistants is Jio's secret per assistant id.
	Assistants map[string]string `json:"assistants,omitempty"`
}

// Jio's production hosts, from the integration guide §1.5.
const (
	JioTokenURL = "https://tgs.businessmessaging.jio.com"
	JioBaseURL  = "https://api.businessmessaging.jio.com"
)

// RCSSettingKeys is each vendor's settings, required ones first. Jio's two
// hosts default to production, and Google's to its Asia region.
var RCSSettingKeys = map[string][]string{
	"airtel": {"baseUrl", "customerId", "subAccountId"},
	"vi":     {"baseUrl", "tokenUrl", "clientId"},
	"jio":    {"tokenUrl", "baseUrl"},
	"google": {"baseUrl"},
}

var rcsSettingDefaults = map[string]map[string]string{
	"jio":    {"tokenUrl": JioTokenURL, "baseUrl": JioBaseURL},
	"google": {"baseUrl": "https://asia-rcsbusinessmessaging.googleapis.com"},
}

// RCSConnectionProblem is why an account could not send, empty when it could.
// A Jio account with no assistants yet is not a problem: it has nothing to
// send for until a brand launches, and adding the first is the next step.
func RCSConnectionProblem(vendor string, settings map[string]string, secrets RCSSecrets) string {
	keys, known := RCSSettingKeys[vendor]
	if !known {
		return fmt.Sprintf("unknown RCS vendor %q: use airtel, vi, jio or google", vendor)
	}
	for _, key := range keys {
		if strings.TrimSpace(settings[key]) == "" && rcsSettingDefaults[vendor][key] == "" {
			return "setting " + key + " is required for " + vendor
		}
	}
	switch {
	case vendor == "airtel" && secrets.AuthToken == "":
		return "airtel needs its auth token"
	case vendor == "vi" && secrets.ClientSecret == "":
		return "vi needs its client secret"
	case vendor == "google" && secrets.ServiceAccountJSON == "":
		return "google needs its service account key"
	}
	for assistant, secret := range secrets.Assistants {
		if strings.TrimSpace(assistant) == "" || secret == "" {
			return "every jio assistant needs an id and a secret key"
		}
	}
	return ""
}

func rcsSetting(c store.RCSConnection, key string) string {
	if value := strings.TrimSpace(c.Settings[key]); value != "" {
		return strings.TrimRight(value, "/")
	}
	return rcsSettingDefaults[c.Vendor][key]
}

// OpenRCSSecrets decrypts an account's secrets.
func (s *Server) OpenRCSSecrets(c store.RCSConnection) (RCSSecrets, error) {
	var secrets RCSSecrets
	if c.SecretsSealed == nil {
		return secrets, nil
	}
	if s.Secrets == nil {
		return secrets, errors.New("no CONNECTION_ENCRYPTION_KEY, so the stored secrets cannot be read")
	}
	plain, err := s.Secrets.Decrypt(*c.SecretsSealed)
	if err != nil {
		return secrets, errors.New("the stored secrets could not be decrypted")
	}
	if err := json.Unmarshal([]byte(plain), &secrets); err != nil {
		return secrets, errors.New("the stored secrets are not readable")
	}
	return secrets, nil
}

// RCSGateway builds the gateway an account describes.
func (s *Server) RCSGateway(c store.RCSConnection) (connector.RCSGateway, error) {
	secrets, err := s.OpenRCSSecrets(c)
	if err != nil {
		return nil, err
	}
	if problem := RCSConnectionProblem(c.Vendor, c.Settings, secrets); problem != "" {
		return nil, errors.New(problem)
	}
	switch c.Vendor {
	case "airtel":
		return &connector.AirtelRCS{BaseURL: rcsSetting(c, "baseUrl"), AuthToken: secrets.AuthToken,
			CustomerID: rcsSetting(c, "customerId"), SubAccountID: rcsSetting(c, "subAccountId")}, nil
	case "vi":
		return &connector.ViRCS{BaseURL: rcsSetting(c, "baseUrl"), TokenURL: rcsSetting(c, "tokenUrl"),
			ClientID: rcsSetting(c, "clientId"), ClientSecret: secrets.ClientSecret}, nil
	case "jio":
		return &connector.JioRCS{TokenURL: rcsSetting(c, "tokenUrl"), BaseURL: rcsSetting(c, "baseUrl"),
			Assistants: secrets.Assistants}, nil
	default:
		google := &connector.GoogleRBM{BaseURL: rcsSetting(c, "baseUrl"),
			ServiceAccountJSON: []byte(secrets.ServiceAccountJSON)}
		if health := google.Health(context.Background()); !health.Healthy {
			return nil, errors.New(health.Detail)
		}
		return google, nil
	}
}

// ReloadRCSConnections brings the RCS router in line with rcs_connections:
// every active account in this deployment's environment is held, anything else
// is dropped. Run at boot and then every minute, like ReloadSMPPBinds.
func (s *Server) ReloadRCSConnections(ctx context.Context) error {
	if s.RCS == nil {
		return nil
	}
	environment := s.SMPPEnvironment
	connections, err := store.ListRCSConnections(ctx, s.operatorPool(), &environment)
	if err != nil {
		return err
	}
	var accounts []connector.RCSAccount
	active := map[string]store.RCSConnection{}
	for _, c := range connections {
		if c.Status != "active" {
			continue
		}
		c := c
		carrier := connector.RCSIntegrations[c.Vendor]
		active[carrier] = c
		accounts = append(accounts, connector.RCSAccount{Carrier: carrier,
			Version: c.ID.String() + "@" + c.UpdatedAt.String(),
			Build:   func() (connector.RCSGateway, error) { return s.RCSGateway(c) }})
	}
	failures := s.RCS.Sync(accounts)

	for carrier, c := range active {
		health, detail := "ok", ""
		if err := failures[carrier]; err != nil {
			health, detail = "error", err.Error()
		} else if gateway, ok := s.RCS.For(carrier); ok {
			if h := gateway.Health(ctx); !h.Healthy {
				health, detail = "error", h.Detail
			}
		}
		if health == c.HealthStatus && (c.LastError == nil) == (detail == "") {
			continue
		}
		var lastError *string
		if detail != "" {
			lastError = &detail
		}
		if err := store.RecordRCSConnectionHealth(ctx, s.operatorPool(), c.ID, health, lastError); err != nil {
			return err
		}
	}
	return nil
}

// BootRCS runs the first reload before the server takes traffic and returns
// the line to log.
func (s *Server) BootRCS(ctx context.Context) string {
	if s.RCS == nil {
		return "rcs connections: not used, the environment names the RCS carrier"
	}
	if err := s.ReloadRCSConnections(ctx); err != nil {
		return fmt.Sprintf("rcs connections at boot: could not be read: %v", err)
	}
	carriers := s.RCSOperators()
	line := fmt.Sprintf("rcs connections at boot: operators=%v", carriers)
	if len(carriers) > 0 && s.CarrierWebhookToken == "" {
		line += " — RCS_WEBHOOK_TOKEN is not set, so no delivery reports will be accepted"
	}
	return line
}
