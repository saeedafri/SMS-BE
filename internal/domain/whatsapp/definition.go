// Package whatsapp holds Meta's rules for a WhatsApp template written in its
// component model. The checks and their sentences mirror
// ../SMS-UI/src/lib/templates/wa-definition.ts, so the composer, the mock and
// this server never disagree about what Meta will accept.
package whatsapp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
)

// Limits from ../SMS-UI/src/lib/registries/whatsapp-limits.ts.
const (
	bodyMax          = 1024
	footerMax        = 60
	headerTextMax    = 60
	buttonTextMax    = 25
	urlButtonMax     = 2000
	copyCodeMax      = 20
	buttonsTotalMax  = 10
	nameMax          = 512
	authExpiryMin    = 1
	authExpiryMax    = 90
	TemplatesPerHour = 100
)

var (
	namePattern   = regexp.MustCompile(`^[a-z0-9_]+$`)
	languages     = []string{"en", "en_GB", "en_US", "hi", "gu", "mr", "ta", "te", "kn", "ml", "bn", "pa", "ur"}
	categories    = []string{"marketing", "utility", "authentication"}
	headerFormats = []string{"TEXT", "IMAGE", "VIDEO", "DOCUMENT", "LOCATION"}
	buttonLimits  = map[string]int{"QUICK_REPLY": 10, "URL": 2, "PHONE_NUMBER": 1, "COPY_CODE": 1, "OTP": 1}
)

type button struct {
	Type, Text, URL, PhoneNumber, Example string
}

type example struct{ Name, Value string }

type component struct {
	Type         string
	Format       string
	Text         *string
	MediaAssetID string `json:"mediaAssetId"`
	Examples     []example
	Buttons      *[]button
}

// Definition is the part of a WaTemplateDefinition these rules read.
type Definition struct {
	Name                  string
	Language              string
	Category              string
	ParameterFormat       string
	CodeExpirationMinutes *int
	Components            []component
}

func filled(value *string) bool { return value != nil && strings.TrimSpace(*value) != "" }

func (d Definition) byType(kind string) []component {
	var out []component
	for _, c := range d.Components {
		if c.Type == kind {
			out = append(out, c)
		}
	}
	return out
}

// Parse reads a stored or submitted definition.
func Parse(raw []byte) (Definition, error) {
	var d Definition
	err := json.Unmarshal(raw, &d)
	return d, err
}

// Body is the body text a recipient reads, empty for authentication.
func (d Definition) Body() string {
	for _, c := range d.byType("BODY") {
		if c.Text != nil {
			return *c.Text
		}
	}
	return ""
}

// Variables are the named variables in header text and body, first seen first.
func (d Definition) Variables() []string {
	out := []string{}
	for _, c := range d.Components {
		if c.Text == nil || !(c.Type == "BODY" || (c.Type == "HEADER" && c.Format == "TEXT")) {
			continue
		}
		for _, name := range compliance.ParseVariables(*c.Text) {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// Validate checks a definition in Meta's order: name, language, category and
// parameter format, components, then examples. It returns the first problem
// in words, or "".
func (d Definition) Validate() string {
	switch {
	case strings.TrimSpace(d.Name) == "":
		return "Give the template a name."
	case len(d.Name) > nameMax:
		return fmt.Sprintf("A template name can be at most %d characters.", nameMax)
	case !namePattern.MatchString(d.Name):
		return "A template name can use lowercase letters, digits and underscores only."
	case !slices.Contains(languages, d.Language):
		return "Choose one of the supported languages."
	case !slices.Contains(categories, d.Category):
		return "Choose a category: marketing, utility or authentication."
	case d.ParameterFormat != "named" && d.ParameterFormat != "positional":
		return "The parameter format must be named or positional."
	case d.Components == nil:
		return "A template needs components."
	}
	authentication := d.Category == "authentication"
	bodies := d.byType("BODY")
	if len(bodies) != 1 {
		return "A template needs exactly one body."
	}
	body := bodies[0]
	if !authentication {
		if !filled(body.Text) {
			return "The message body is required."
		}
		if len(*body.Text) > bodyMax {
			return fmt.Sprintf("The body can be at most %d characters.", bodyMax)
		}
	}
	headers := d.byType("HEADER")
	if len(headers) > 1 {
		return "A template can have only one header."
	}
	if len(headers) == 1 {
		header := headers[0]
		switch {
		case authentication:
			return "An authentication template has no header."
		case !slices.Contains(headerFormats, header.Format):
			return "Choose a header type."
		case header.Format == "TEXT" && !filled(header.Text):
			return "The header text is required."
		case header.Format == "TEXT" && len(*header.Text) > headerTextMax:
			return fmt.Sprintf("The header can be at most %d characters.", headerTextMax)
		case header.Format != "TEXT" && header.Format != "LOCATION" && strings.TrimSpace(header.MediaAssetID) == "":
			return "Upload the header media."
		}
	}
	footers := d.byType("FOOTER")
	if len(footers) > 1 {
		return "A template can have only one footer."
	}
	if len(footers) == 1 {
		if !filled(footers[0].Text) {
			return "The footer text is required."
		}
		if len(*footers[0].Text) > footerMax {
			return fmt.Sprintf("The footer can be at most %d characters.", footerMax)
		}
	}
	blocks := d.byType("BUTTONS")
	if len(blocks) > 1 {
		return "A template can have only one buttons section."
	}
	if len(blocks) == 1 {
		if problem := validateButtons(blocks[0].Buttons, authentication); problem != "" {
			return problem
		}
	}
	if minutes := d.CodeExpirationMinutes; authentication && minutes != nil &&
		(*minutes < authExpiryMin || *minutes > authExpiryMax) {
		return fmt.Sprintf("The code expiry must be between %d and %d minutes.", authExpiryMin, authExpiryMax)
	}
	if authentication {
		return ""
	}
	for _, c := range append(headers, body) {
		if c.Text == nil {
			continue
		}
		for _, name := range compliance.ParseVariables(*c.Text) {
			if !slices.ContainsFunc(c.Examples, func(e example) bool {
				return e.Name == name && strings.TrimSpace(e.Value) != ""
			}) {
				return fmt.Sprintf("Give an example value for {{%s}}: WhatsApp reviews the template with it.", name)
			}
		}
	}
	return ""
}

func validateButtons(buttons *[]button, authentication bool) string {
	if buttons == nil || len(*buttons) == 0 {
		return "Add at least one button, or remove the buttons section."
	}
	if len(*buttons) > buttonsTotalMax {
		return fmt.Sprintf("A template can have at most %d buttons.", buttonsTotalMax)
	}
	seen := map[string]int{}
	for _, b := range *buttons {
		limit, known := buttonLimits[b.Type]
		switch {
		case !known:
			return "A button has a type WhatsApp does not offer."
		case authentication && b.Type != "OTP" && b.Type != "COPY_CODE":
			return "An authentication template may carry only a copy-code button."
		case strings.TrimSpace(b.Text) == "":
			return "Each button needs a label."
		case len(b.Text) > buttonTextMax:
			return fmt.Sprintf("A button label can be at most %d characters.", buttonTextMax)
		}
		seen[b.Type]++
		if seen[b.Type] > limit {
			plural := "s"
			if limit == 1 {
				plural = ""
			}
			return fmt.Sprintf("A template can have at most %d %s button%s.", limit,
				strings.Replace(strings.ToLower(b.Type), "_", " ", 1), plural)
		}
		switch {
		case b.Type == "URL" && strings.TrimSpace(b.URL) == "":
			return "A URL button needs a link."
		case b.Type == "URL" && len(b.URL) > urlButtonMax:
			return fmt.Sprintf("A button link can be at most %d characters.", urlButtonMax)
		case b.Type == "PHONE_NUMBER" && strings.TrimSpace(b.PhoneNumber) == "":
			return "A call button needs a phone number."
		case b.Type == "COPY_CODE" && strings.TrimSpace(b.Example) == "":
			return "A copy-code button needs an example code."
		case b.Type == "COPY_CODE" && len(b.Example) > copyCodeMax:
			return fmt.Sprintf("The example code can be at most %d characters.", copyCodeMax)
		}
	}
	return ""
}
