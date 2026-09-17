package compliance

import (
	"regexp"
	"strings"
	"time"
)

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

// Every DLT tier carries an identifier the customer is issued on their operator
// portal: the principal-entity id, the header id, and the content-template id
// that must travel with every submit.
func (india) RequiresRegistrationID(Tier) bool { return true }

func (india) RequiresRegisteredTemplate() bool { return true }

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
		// A principal entity cannot deliver SMS by itself: a registered
		// telemarketer hands it to the operator, and the customer attaches
		// Textify's TM id to their PE on the DLT portal. We cannot see the
		// portal, so this is self-attested — the confirmation is the submission
		// — and an operator approves it like any other registration.
		//
		// After the PE, never before it: the demo seed's fixture check takes the
		// first entity-tier object as the entity.
		{
			Key:       "tm_mapping",
			Label:     "Telemarketer mapping",
			Tier:      TierEntity,
			DependsOn: "pe_rtm_entity",
			Remediation: "Open your DLT account, check Textify's TM id is still attached and your " +
				"headers are assigned to it, then confirm again here.",
			Fields: []FieldSpec{},
		},
		{
			Key:       "dlt_header",
			DependsOn: "tm_mapping",
			Label:     "DLT header (sender ID)",
			Tier:      TierSender,
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

// DLT headers are exactly six alphanumeric characters. Case is not enforced
// here — the operator portal issues uppercase and we uppercase on submit — but
// length and character set are, because a header outside that shape cannot
// correspond to anything DLT ever approved.
var indiaHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9]{6}$`)

func (india) ValidateHeader(header string) ValidationResult {
	if !indiaHeaderPattern.MatchString(strings.TrimSpace(header)) {
		return invalid("An India DLT header is exactly six letters or digits, " +
			"matching the header approved on your DLT account.")
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

// indiaTime is IST. A fixed zone rather than LoadLocation, so a server without
// tzdata cannot silently fall back to UTC and move the window by five and a
// half hours.
var indiaTime = time.FixedZone("IST", 5*3600+1800)

// PromotionalAllowedAt reports whether promotional traffic to country may be
// sent at t. India permits it only 10:00–21:00 IST; operators drop anything
// outside that. Countries without the rule always allow it.
func PromotionalAllowedAt(country string, t time.Time) bool {
	if country != "IN" {
		return true
	}
	hour := t.In(indiaTime).Hour()
	return hour >= 10 && hour < 21
}

// NextPromotionalOpening is when promotional traffic to country may next be
// sent, t itself when it already may.
func NextPromotionalOpening(country string, t time.Time) time.Time {
	if PromotionalAllowedAt(country, t) {
		return t
	}
	local := t.In(indiaTime)
	opening := time.Date(local.Year(), local.Month(), local.Day(), 10, 0, 0, 0, indiaTime)
	if !opening.After(local) {
		opening = opening.AddDate(0, 0, 1)
	}
	return opening.UTC()
}
