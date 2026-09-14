package api_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linxGnu/gosmpp/pdu"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saeedafri/sms-be/internal/connector"
)

// operatorSMSC accepts binds and answers keep-alives, and can be switched off
// to play an operator whose SMSC went down.
type operatorSMSC struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
}

func startOperatorSMSC(t *testing.T) *operatorSMSC {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &operatorSMSC{listener: listener}
	t.Cleanup(s.stop)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go serveBindsOnly(conn)
		}
	}()
	return s
}

func (s *operatorSMSC) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *operatorSMSC) stop() {
	_ = s.listener.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
}

func serveBindsOnly(conn net.Conn) {
	defer conn.Close()
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		raw := make([]byte, binary.BigEndian.Uint32(header))
		copy(raw, header)
		if _, err := io.ReadFull(conn, raw[4:]); err != nil {
			return
		}
		p, err := pdu.Parse(bytes.NewReader(raw))
		if err != nil {
			return
		}
		var reply pdu.PDU
		switch v := p.(type) {
		case *pdu.BindRequest:
			reply = v.GetResponse()
		case *pdu.EnquireLink:
			reply = v.GetResponse()
		case *pdu.Unbind:
			reply = v.GetResponse()
		}
		if reply != nil {
			buf := pdu.NewBuffer(make([]byte, 0, 64))
			reply.Marshal(buf)
			_, _ = conn.Write(buf.Bytes())
		}
	}
}

type countingSandbox struct {
	connector.Connector
	submitted atomic.Int32
}

func (c *countingSandbox) Submit(ctx context.Context, s []connector.Submission) ([]connector.Receipt, error) {
	c.submitted.Add(int32(len(s)))
	return c.Connector.Submit(ctx, s)
}

// withSMPP gives the harness a router over a counting sandbox, binding the
// given environment's connections.
func (h *harness) withSMPP(environment string) *countingSandbox {
	sandbox := &countingSandbox{Connector: connector.NewSandbox(0)}
	h.server.SMPP = &connector.SMPPRouter{Fallback: sandbox}
	h.server.Carriers = connector.Registry{Default: h.server.SMPP}
	h.server.SMPPEnvironment = environment
	h.t.Cleanup(func() { h.server.SMPP.Sync(nil, connector.SMPPEvents{}, false) })
	return sandbox
}

func (h *harness) connectionHealth(operator, id string) string {
	h.t.Helper()
	var reread struct {
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
	}
	h.do(http.MethodGet, "/v1/operator/connections/"+id, operator, nil).decode(h.t, &reread)
	return reread.Health.Status
}

func (h *harness) seedConnection(operator, environment string, port int, enable bool) string {
	h.t.Helper()
	created := createConnection(h.t, h, operator, map[string]any{
		"environment": environment, "host": "127.0.0.1", "port": port,
		"systemId": "health-" + uuid.NewString(), "carrier": "AIRTEL",
	})
	var connection struct {
		Id string `json:"id"`
	}
	created.decode(h.t, &connection)
	h.t.Cleanup(func() {
		h.do(http.MethodDelete, "/v1/operator/connections/"+connection.Id, operator, nil)
	})
	if enable {
		if res := h.do(http.MethodPost, "/v1/operator/connections/"+connection.Id+"/enable", operator, nil); res.Code != http.StatusOK {
			h.t.Fatalf("enable = %d\n%s", res.Code, res.Body)
		}
	}
	return connection.Id
}

// Ask 35 §2.6. The Connections screen's main status column must say what the
// bind is doing now: bound while it is, error once it drops, unbound once the
// operator disables it.
func TestConnectionHealthFollowsTheBind(t *testing.T) {
	h := newHarness(t)
	h.withSMPP("test")
	operator := h.operatorToken()
	smsc := startOperatorSMSC(t)
	id := h.seedConnection(operator, "test", smsc.port(), true)
	ctx := context.Background()

	if err := h.server.ReloadSMPPBinds(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.connectionHealth(operator, id); got != "bound" {
		t.Fatalf("health after binding = %q, want bound", got)
	}

	smsc.stop()
	time.Sleep(500 * time.Millisecond)
	if err := h.server.ReloadSMPPBinds(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.connectionHealth(operator, id); got != "error" {
		t.Errorf("health after the operator's SMSC went away = %q, want error", got)
	}

	h.do(http.MethodPost, "/v1/operator/connections/"+id+"/disable", operator, nil)
	if err := h.server.ReloadSMPPBinds(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.connectionHealth(operator, id); got != "unbound" {
		t.Errorf("health after disabling = %q, want unbound", got)
	}
}

// Ask 37. A reload that cannot read the connections table leaves the router as
// it was. It must never decide the deployment has no operators.
func TestReloadFailureKeepsTheLastState(t *testing.T) {
	h := newHarness(t)
	sandbox := h.withSMPP("live")
	operator := h.operatorToken()
	h.seedConnection(operator, "live", 1, false) // configured, disabled
	ctx := context.Background()
	if err := h.server.ReloadSMPPBinds(ctx); err != nil {
		t.Fatal(err)
	}

	original := h.server.OperatorDB
	h.server.OperatorDB = unreadablePool(t)
	if err := h.server.ReloadSMPPBinds(ctx); err == nil {
		t.Fatal("a reload against an unreadable database reported success")
	}
	h.server.OperatorDB = original

	receipts, _ := h.server.SMPP.Submit(ctx, []connector.Submission{{MessageID: "m", Channel: "SMS",
		Carrier: "AIRTEL", Country: "IN", Msisdn: "+919820000030", Sender: "ACMERT", Body: "hi",
		DLTEntityID: "PE", DLTTemplateID: "TPL"}})
	if sandbox.submitted.Load() != 0 || len(receipts) != 1 || receipts[0].ErrorCode != "NO_OPERATOR_BIND" {
		t.Fatalf("after a failed reload the sandbox got %d and the receipt is %+v, want NO_OPERATOR_BIND",
			sandbox.submitted.Load(), receipts)
	}
}

func (h *harness) sendOneSMS() (status, code string, cost int64) {
	h.t.Helper()
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876543210", "body": "Your order has shipped.",
	})
	var result struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
		CostMinor int64   `json:"costMinor"`
	}
	res.decode(h.t, &result)
	if result.ErrorCode != nil {
		code = *result.ErrorCode
	}
	return result.Status, code, result.CostMinor
}

// unreadablePool is a database handle every query on fails, the way a
// connections table that cannot be read looks to a reload.
func unreadablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	return pool
}
