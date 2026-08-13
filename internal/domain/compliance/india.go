package compliance

import "strings"

// shortenerHosts is not exhaustive and is not meant to be a complete defence —
// DLT's real control is the CTA whitelist held by the operator. This catches
// the common cases early so a tenant learns at template-creation time rather
// than at rejection time, days later.
//
// Extend before production: rb.gy, shorturl.at, cutt.ly and friends.
var shortenerHosts = []string{"bit.ly", "tinyurl.com", "t.co", "goo.gl", "ow.ly"}

type india struct{}

func (india) Country() string  { return "IN" }
func (india) Label() string    { return "India (DLT)" }
func (india) Currency() string { return "INR" }
func (india) Stub() bool       { return false }

func (r india) Object(key string) (RegistrationObject, bool) {
	return findObject(r.RegistrationObjects(), key)
}

func (india) RegistrationObjects() []RegistrationObject {
	return []RegistrationObject{
		{
			Key:   "pe_rtm_entity",
			Label: "Principal entity (PE/RTM)",
			Tier:  TierEntity,
			Remediation: "Confirm the legal entity name matches your PAN exactly, " +
				"and re-enter the DLT-registered PAN.",
			Fields: []FieldSpec{
				{Key: "legalName", Label: "Registered legal name", Type: FieldText, Required: true},
				{Key: "pan", Label: "PAN", Type: FieldText, Required: true},
				{Key: "entityType", Label: "Entity type", Type: FieldSelect, Required: true, Options: []Option{
					{Value: "private_ltd", Label: "Private limited"},
					{Value: "public_ltd", Label: "Public limited"},
					{Value: "llp", Label: "LLP"},
					{Value: "proprietorship", Label: "Proprietorship"},
				}},
				{Key: "contactEmail", Label: "Compliance contact email", Type: FieldEmail, Required: true},
			},
		},
		{
			Key:   "dlt_header",
			Label: "DLT header (sender ID)",
			Tier:  TierSender,
			Remediation: "Headers are six alphanumeric characters and must already be " +
				"registered against your principal entity on the DLT portal.",
			Fields: []FieldSpec{
				{Key: "header", Label: "Header", Type: FieldText, Required: true},
				{Key: "headerType", Label: "Header type", Type: FieldSelect, Required: true, Options: []Option{
					{Value: "transactional", Label: "Transactional"},
					{Value: "promotional", Label: "Promotional"},
					{Value: "service_implicit", Label: "Service (implicit consent)"},
					{Value: "service_explicit", Label: "Service (explicit consent)"},
				}},
			},
		},
		{
			Key:   "dlt_template",
			Label: "DLT content template",
			Tier:  TierTemplate,
			Remediation: "The template body must match the DLT-approved text exactly, " +
				"including punctuation and variable placeholders.",
			Fields: []FieldSpec{
				{Key: "templateName", Label: "Template name", Type: FieldText, Required: true},
				{Key: "dltTemplateId", Label: "DLT template id", Type: FieldText, Required: true},
			},
		},
	}
}

// ValidateCtaURL enforces India's no-shortener rule. Since October 2024 DLT
// requires the full URL, whitelisted on the portal — a shortened link cannot
// be matched against the whitelist, so it is rejected at source.
func (india) ValidateCtaURL(rawURL string) ValidationResult {
	parsed, result := requireAbsoluteURL(rawURL)
	if !result.OK {
		return result
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	for _, shortener := range shortenerHosts {
		if host == shortener || strings.HasSuffix(host, "."+shortener) {
			return invalid("URL shorteners are not allowed under DLT — use the full URL.")
		}
	}
	return valid()
}

func findObject(objects []RegistrationObject, key string) (RegistrationObject, bool) {
	for _, object := range objects {
		if object.Key == key {
			return object, true
		}
	}
	return RegistrationObject{}, false
}
