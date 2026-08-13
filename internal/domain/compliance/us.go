package compliance

type unitedStates struct{}

func (unitedStates) Country() string  { return "US" }
func (unitedStates) Label() string    { return "United States (10DLC/TCR)" }
func (unitedStates) Currency() string { return "USD" }
func (unitedStates) Stub() bool       { return false }

func (r unitedStates) Object(key string) (RegistrationObject, bool) {
	return findObject(r.RegistrationObjects(), key)
}

func (unitedStates) RegistrationObjects() []RegistrationObject {
	return []RegistrationObject{
		{
			Key:   "tcr_brand",
			Label: "TCR brand",
			Tier:  TierEntity,
			Remediation: "The legal name and EIN must match IRS records exactly; " +
				"a mismatch is the most common cause of brand vetting failure.",
			Fields: []FieldSpec{
				{Key: "legalName", Label: "Legal company name", Type: FieldText, Required: true},
				{Key: "website", Label: "Company website", Type: FieldURL, Required: true},
				{Key: "supportEmail", Label: "Support email", Type: FieldEmail, Required: true},
			},
		},
		{
			Key:   "tcr_campaign",
			Label: "TCR campaign",
			Tier:  TierSender,
			// A campaign is filed against an approved brand. Encoding that here
			// rather than in a handler is what keeps the ordering rule portable
			// to the next country that needs one.
			DependsOn: "tcr_brand",
			Remediation: "Campaigns are reviewed against the sample message — it must " +
				"show real content, including your brand name and opt-out wording.",
			Fields: []FieldSpec{
				{Key: "useCase", Label: "Use case", Type: FieldSelect, Required: true, Options: []Option{
					{Value: "2fa", Label: "Two-factor authentication"},
					{Value: "account_notification", Label: "Account notification"},
					{Value: "customer_care", Label: "Customer care"},
					{Value: "delivery_notification", Label: "Delivery notification"},
					{Value: "marketing", Label: "Marketing"},
				}},
				{Key: "description", Label: "Campaign description", Type: FieldText, Required: true},
				{Key: "sampleMessage", Label: "Sample message", Type: FieldText, Required: true},
			},
		},
	}
}

// 10DLC carries no shortener prohibition, so the US regime validates only that
// the URL is well-formed. Inheriting India's rule here would be the exact
// "branches in shared code" failure this package exists to prevent.
func (unitedStates) ValidateCtaURL(rawURL string) ValidationResult {
	_, result := requireAbsoluteURL(rawURL)
	return result
}
