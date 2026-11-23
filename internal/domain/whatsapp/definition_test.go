package whatsapp

import "testing"

func TestValidateInMetasOrder(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"valid", `{"name":"order_ready","language":"en","category":"utility","parameterFormat":"named",
			"components":[{"type":"BODY","text":"Hi {{first_name}}","examples":[{"name":"first_name","value":"Asha"}]}]}`, ""},
		// A bad name is reported before a bad body: Meta's order.
		{"name first", `{"name":"Order Ready","language":"en","category":"utility","parameterFormat":"named",
			"components":[]}`, "A template name can use lowercase letters, digits and underscores only."},
		{"language", `{"name":"a","language":"fr","category":"utility","parameterFormat":"named","components":[]}`,
			"Choose one of the supported languages."},
		{"one body", `{"name":"a","language":"en","category":"utility","parameterFormat":"named","components":[]}`,
			"A template needs exactly one body."},
		{"example", `{"name":"a","language":"en","category":"utility","parameterFormat":"named",
			"components":[{"type":"BODY","text":"Hi {{first_name}}"}]}`,
			"Give an example value for {{first_name}}: WhatsApp reviews the template with it."},
		{"auth buttons", `{"name":"a","language":"en","category":"authentication","parameterFormat":"named",
			"components":[{"type":"BODY"},{"type":"BUTTONS","buttons":[{"type":"URL","text":"Go","url":"https://x"}]}]}`,
			"An authentication template may carry only a copy-code button."},
		{"two urls", `{"name":"a","language":"en","category":"marketing","parameterFormat":"named",
			"components":[{"type":"BODY","text":"x"},{"type":"BUTTONS","buttons":[
			{"type":"URL","text":"a","url":"https://a"},{"type":"URL","text":"b","url":"https://b"},
			{"type":"URL","text":"c","url":"https://c"}]}]}`, "A template can have at most 2 url buttons."},
	}
	for _, c := range cases {
		d, err := Parse([]byte(c.raw))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := d.Validate(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVariablesSkipMediaHeaders(t *testing.T) {
	d, _ := Parse([]byte(`{"components":[{"type":"HEADER","format":"TEXT","text":"{{a}}"},
		{"type":"BODY","text":"{{b}} {{a}}"},{"type":"FOOTER","text":"{{c}}"}]}`))
	got := d.Variables()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("variables = %v, want [a b]", got)
	}
}
