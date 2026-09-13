package api_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linxGnu/gosmpp/pdu"
)

// acceptingSMSC answers binds and unbinds and nothing else: enough for a test.
func acceptingSMSC(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
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
					buf := pdu.NewBuffer(make([]byte, 0, 64))
					p.GetResponse().Marshal(buf)
					_, _ = conn.Write(buf.Bytes())
					if _, unbind := p.(*pdu.Unbind); unbind {
						return
					}
				}
			}(conn)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return host, number
}

// Ask 34 A3. Both outcomes of a connection test carry a real health status and
// the time of the test, as ConnectionTestResult requires.
func TestAConnectionTestResultIsInContractOnBothPaths(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	host, port := acceptingSMSC(t)

	for _, tc := range []struct {
		name, host string
		port       int
		ok         bool
		status     string
	}{
		{"refused", "127.0.0.1", 9, false, "error"},
		{"bound", host, port, true, "bound"},
	} {
		created := createConnection(t, h, operator, map[string]any{
			"systemId": "result-" + uuid.NewString(), "host": tc.host, "port": tc.port,
		})
		var connection struct {
			Id string `json:"id"`
		}
		created.decode(t, &connection)

		before := time.Now().Add(-time.Second)
		res := h.do(http.MethodPost,
			fmt.Sprintf("/v1/operator/connections/%s/test", connection.Id), operator, nil)
		var result struct {
			Ok       bool      `json:"ok"`
			Status   string    `json:"status"`
			TestedAt time.Time `json:"testedAt"`
		}
		res.decode(t, &result)
		if result.Ok != tc.ok || result.Status != tc.status {
			t.Errorf("%s: ok=%v status=%q, want ok=%v status=%q\n%s",
				tc.name, result.Ok, result.Status, tc.ok, tc.status, res.Body)
		}
		if result.TestedAt.Before(before) {
			t.Errorf("%s: testedAt = %s, want the time of the test", tc.name, result.TestedAt)
		}
	}
}
