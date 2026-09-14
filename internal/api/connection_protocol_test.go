package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var platformChain = []string{"1702000000000000001", "1702000000000000099"}

type protocolRead struct {
	DltEntityTlv         int      `json:"dltEntityTlv"`
	DltTemplateTlv       int      `json:"dltTemplateTlv"`
	DltChainTlv          int      `json:"dltChainTlv"`
	SourceTon            int      `json:"sourceTon"`
	SourceNpi            int      `json:"sourceNpi"`
	DestTon              int      `json:"destTon"`
	DestNpi              int      `json:"destNpi"`
	RegisteredDelivery   int      `json:"registeredDelivery"`
	DltTelemarketerChain []string `json:"dltTelemarketerChain"`
}

func (h *harness) connectionProtocol(operator, id string) protocolRead {
	h.t.Helper()
	var read struct {
		Protocol *protocolRead `json:"protocol"`
	}
	h.do(http.MethodGet, "/v1/operator/connections/"+id, operator, nil).decode(h.t, &read)
	if read.Protocol == nil {
		h.t.Fatal("the connection has no protocol")
	}
	return *read.Protocol
}

func (h *harness) protocolConnection(operator string, protocol map[string]any) string {
	h.t.Helper()
	overrides := map[string]any{"systemId": "proto-" + uuid.NewString(), "host": "127.0.0.1", "port": 1}
	if protocol != nil {
		overrides["protocol"] = protocol
	}
	created := createConnection(h.t, h, operator, overrides)
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}
	var connection struct {
		ID string `json:"id"`
	}
	created.decode(h.t, &connection)
	h.t.Cleanup(func() { h.do(http.MethodDelete, "/v1/operator/connections/"+connection.ID, operator, nil) })
	return connection.ID
}

// Ask 34 A1 §3.1. A required response field that is never filled compiles and
// serves zeros. The defaults must be what a connection with no overrides reads.
func TestAConnectionWithNoOverridesReadsTheDefaults(t *testing.T) {
	h := newHarness(t)
	h.server.DLTChain = platformChain
	operator := h.operatorToken()
	got := h.connectionProtocol(operator, h.protocolConnection(operator, nil))
	want := protocolRead{5120, 5121, 5122, 5, 0, 1, 1, 1, platformChain}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protocol = %+v, want %+v", got, want)
	}
}

// Ask 34 A1 §3.3. Every rule refuses with 422 naming the field, and a refused
// request changes nothing, including the collision only the merged result shows.
func TestAProtocolRefusalChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.server.DLTChain = platformChain
	operator := h.operatorToken()
	id := h.protocolConnection(operator, map[string]any{"dltEntityTlv": 5200, "dltTemplateTlv": 5120})
	before := h.connectionProtocol(operator, id)

	cases := []struct {
		name  string
		patch string
		field string
	}{
		{"entity tag below range", `{"dltEntityTlv": 5119}`, "dltEntityTlv"},
		{"template tag above range", `{"dltTemplateTlv": 16384}`, "dltTemplateTlv"},
		{"chain tag collides", `{"dltChainTlv": 5200}`, "dltChainTlv"},
		{"merged collision on reset", `{"dltEntityTlv": null}`, "dltEntityTlv"},
		{"source ton", `{"sourceTon": 7}`, "sourceTon"},
		{"dest npi", `{"destNpi": 2}`, "destNpi"},
		{"registered delivery", `{"registeredDelivery": 32}`, "registeredDelivery"},
		{"chain too long", `{"dltTelemarketerChain": ["1702000000000000001","1702000000000000001","1702000000000000001","1702000000000000001","1702000000000000001","1702000000000000099"]}`, "dltTelemarketerChain"},
		{"chain id malformed", `{"dltTelemarketerChain": ["1702", "1702000000000000099"]}`, "dltTelemarketerChain"},
		{"chain not ending in the platform", `{"dltTelemarketerChain": ["1702000000000000099", "1702000000000000001"]}`, "dltTelemarketerChain"},
		{"undeclared key", `{"sourceTon": 1, "tlvOrder": "swap"}`, "tlvOrder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var protocol json.RawMessage = []byte(tc.patch)
			res := h.do(http.MethodPatch, "/v1/operator/connections/"+id, operator,
				map[string]any{"protocol": protocol})
			if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), tc.field) {
				t.Errorf("%s = %d %s, want 422 naming %s", tc.patch, res.Code, res.Body, tc.field)
			}
			if after := h.connectionProtocol(operator, id); !reflect.DeepEqual(after, before) {
				t.Errorf("%s changed the stored protocol: %+v -> %+v", tc.patch, before, after)
			}
		})
	}

	// And the accepted shapes: one key, then a whole reset.
	if res := h.do(http.MethodPatch, "/v1/operator/connections/"+id, operator,
		map[string]any{"protocol": map[string]any{"registeredDelivery": 0}}); res.Code != http.StatusOK {
		t.Fatalf("one-key patch = %d %s", res.Code, res.Body)
	}
	oneKey := before
	oneKey.RegisteredDelivery = 0
	if after := h.connectionProtocol(operator, id); !reflect.DeepEqual(after, oneKey) {
		t.Errorf("one-key patch: %+v, want only registeredDelivery changed: %+v", after, oneKey)
	}
	if res := h.do(http.MethodPatch, "/v1/operator/connections/"+id, operator,
		map[string]any{"protocol": nil}); res.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", res.Code, res.Body)
	}
	defaults := protocolRead{5120, 5121, 5122, 5, 0, 1, 1, 1, platformChain}
	if after := h.connectionProtocol(operator, id); !reflect.DeepEqual(after, defaults) {
		t.Errorf("after protocol: null = %+v, want the defaults", after)
	}
}
