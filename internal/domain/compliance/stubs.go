package compliance

// stub is a regime we recognise but have not yet implemented registration
// objects for. It exists to prove the pattern generalises — and, practically,
// so that a tenant in GB or AE gets "no registrations are required here yet"
// rather than "we do not operate in your country", which are different facts.
type stub struct {
	country  string
	label    string
	currency string
}

func (s stub) Country() string  { return s.country }
func (s stub) Label() string    { return s.label }
func (s stub) Currency() string { return s.currency }

// A stub registers nothing, so it requires nothing.
func (stub) RequiresRegistrationID(Tier) bool { return false }

// A stub has no registration rules to check a header against.
func (stub) ValidateHeader(string) ValidationResult { return valid() }
func (stub) Stub() bool                             { return true }

func (stub) RegistrationObjects() []RegistrationObject { return nil }

func (stub) Object(string) (RegistrationObject, bool) {
	return RegistrationObject{}, false
}

// Until a stub's real regime lands, the only rule is that a CTA URL be a valid
// absolute URL.
func (stub) ValidateCtaURL(rawURL string) ValidationResult {
	_, result := requireAbsoluteURL(rawURL)
	return result
}
