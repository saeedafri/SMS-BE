package api

import (
	"slices"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The contract's WebhookEventType grew ten events with email and WhatsApp while
// this list stayed at fourteen, so a customer could not subscribe to a bounce.
func TestEverySubscribableEventIsTheContractsList(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var contract []string
	for _, value := range spec.Components.Schemas["WebhookEventType"].Value.Enum {
		contract = append(contract, value.(string))
	}
	ours := slices.Clone(validWebhookEvents)
	slices.Sort(contract)
	slices.Sort(ours)
	if !slices.Equal(contract, ours) {
		t.Errorf("subscribable events = %v\ncontract = %v", ours, contract)
	}
}
