package audience_test

import (
	"reflect"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/audience"
)

// The rule the frontend's src/lib/contacts/fields.ts applies to show a customer
// the message before they send it. If these two ever disagree, the customer is
// shown one message and a handset receives another.

func TestASlotResolvesThroughTheMappingAndNeverFallsBackBehindIt(t *testing.T) {
	t.Parallel()
	fields := map[string]string{"First Name": "Rahul", "firstName": "Wrong", "Order ID": "TX-1"}

	// The mapping wins.
	if got, ok := audience.ResolveField(fields, map[string]string{"firstName": "First Name"},
		"firstName"); !ok || got != "Rahul" {
		t.Errorf("mapped slot = %q %v, want Rahul true", got, ok)
	}
	// No mapping: a column spelled exactly like the slot, so a file whose
	// headers already match keeps working with no mapping at all.
	if got, ok := audience.ResolveField(fields, nil, "firstName"); !ok || got != "Wrong" {
		t.Errorf("unmapped slot = %q %v, want the same-named column", got, ok)
	}
	// A mapping naming a column that is NOT there resolves to nothing. It must
	// not fall back to the same-named column: that would substitute different
	// data than the mapping says it will, silently.
	if got, ok := audience.ResolveField(fields, map[string]string{"firstName": "Given Name"},
		"firstName"); ok {
		t.Errorf("a mapping to a missing column resolved to %q; it must not fall back", got)
	}
}

func TestAValueThatIsNotReallyThereIsTreatedAsNotThere(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"empty":     "",
		"one space": " ",
		"spaces":    "   ",
		"tab":       "\t",
		"newline":   "\n",
		"mixed":     " \t\n ",
	} {
		if got, ok := audience.ResolveField(map[string]string{"n": value}, nil, "n"); ok {
			t.Errorf("%s resolved to %q; a blank is not a value", name, got)
		}
	}
	// An absent key is the same answer as a blank one — the two used to differ,
	// producing "Dear {{firstName}}," for one and "Dear ," for the other.
	if _, ok := audience.ResolveField(map[string]string{}, nil, "firstName"); ok {
		t.Error("an absent column resolved")
	}
	if _, ok := audience.ResolveField(nil, nil, "firstName"); ok {
		t.Error("a nil field map resolved")
	}
	// The value is trimmed, and the trimmed value is what gets substituted:
	// the preview trims, so a send that did not would differ by whitespace —
	// and on a segment boundary that is a different price.
	if got, _ := audience.ResolveField(map[string]string{"n": "  Rahul  "}, nil, "n"); got != "Rahul" {
		t.Errorf("value = %q, want it trimmed", got)
	}
}

func TestFillNamesEverySlotItCouldNotResolveAndLeavesItRaw(t *testing.T) {
	t.Parallel()
	body := "Dear {{firstName}}, order {{orderId}} of {{amount}} ships. Bye {{firstName}}."
	got := audience.Fill(body,
		map[string]string{"First Name": "Rahul", "Order ID": "  TX-1  ", "amount": " "},
		map[string]string{"firstName": "First Name", "orderId": "Order ID"})

	// A slot with no value stays raw, so a preview shows the customer the
	// literal token their recipient would have read.
	want := "Dear Rahul, order TX-1 of {{amount}} ships. Bye Rahul."
	if got.Text != want {
		t.Errorf("text = %q, want %q", got.Text, want)
	}
	// Named once, in template order, even though firstName appears twice.
	if !reflect.DeepEqual(got.Missing, []string{"amount"}) {
		t.Errorf("missing = %v, want [amount]", got.Missing)
	}

	// The whole point: a body whose every slot resolves has nothing missing,
	// and one where none do names them all rather than sending holes.
	none := audience.Fill(body, map[string]string{}, nil)
	if !reflect.DeepEqual(none.Missing, []string{"firstName", "orderId", "amount"}) {
		t.Errorf("missing = %v, want every slot in template order", none.Missing)
	}
	if none.Text != body {
		t.Errorf("text = %q, want the body untouched", none.Text)
	}
}

// Walking the body rather than the contact's fields is the direction that
// matters: walking the fields can only replace slots that HAVE a value, so a
// slot with none is never visited and survives into the delivered text. That is
// how "Dear {{firstName}}," reached 999 handsets in 1,000.
func TestASlotWithNoMatchingFieldIsStillVisited(t *testing.T) {
	t.Parallel()
	got := audience.Fill("Dear {{firstName}},", map[string]string{"city": "Pune"}, nil)
	if len(got.Missing) != 1 || got.Missing[0] != "firstName" {
		t.Errorf("missing = %v, want [firstName] — the slot was never visited", got.Missing)
	}
}

func TestSplittingABodyKeepsWhatIsNotASlot(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body  string
		slots []string
		text  string
	}{
		"no slots":         {"Hello there", []string{}, "Hello there"},
		"one slot":         {"Hi {{a}}", []string{"a"}, "Hi {{a}}"},
		"adjacent":         {"{{a}}{{b}}", []string{"a", "b"}, "{{a}}{{b}}"},
		"repeated":         {"{{a}} and {{a}}", []string{"a"}, "{{a}} and {{a}}"},
		"padded name":      {"Hi {{ a }}", []string{"a"}, "Hi {{a}}"},
		"unclosed is text": {"Hi {{a", []string{}, "Hi {{a"},
		"empty name":       {"Hi {{}}", []string{}, "Hi {{}}"},
		"dlt tag is text":  {"Dear {#var#}", []string{}, "Dear {#var#}"},
	} {
		if got := audience.SlotsIn(tc.body); !reflect.DeepEqual(got, tc.slots) {
			t.Errorf("%s: slots = %v, want %v", name, got, tc.slots)
		}
		// Round trip: nothing resolves, so the body comes back as it went in.
		if got := audience.Fill(tc.body, nil, nil); got.Text != tc.text {
			t.Errorf("%s: fill = %q, want %q", name, got.Text, tc.text)
		}
	}
}
