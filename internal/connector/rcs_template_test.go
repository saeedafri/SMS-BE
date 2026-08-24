package connector

import "testing"

// The rendering and the send path read the SAME ordered variable list. If they
// ever disagree every message goes out with its variables shuffled and nothing
// errors, so these tests pin the contract between them.

func TestNamedTokensBecomeAirtelPositionsInTheDeclaredOrder(t *testing.T) {
	rendered := RenderAirtelText(
		"Hi {{first_name}}, your {{product}} order {{order_id}} has shipped.",
		[]string{"first_name", "product", "order_id"})

	want := "Hi {{1}}, your {{2}} order {{3}} has shipped."
	if rendered != want {
		t.Errorf("rendered = %q, want %q", rendered, want)
	}
}

// Position comes from the declared order, NOT from where the token appears in
// the text. The send path fills values in declared order too.
func TestPositionFollowsTheDeclaredOrderNotTheTextOrder(t *testing.T) {
	rendered := RenderAirtelText("{{code}} is your code, {{name}}.",
		[]string{"name", "code"})

	if rendered != "{{2}} is your code, {{1}}." {
		t.Errorf("rendered = %q — position must follow the declared order", rendered)
	}
}

// Rewriting an undeclared token would invent a slot the send path never fills,
// and Airtel refuses a send with fewer values than the template declares — so
// the template would be approved and permanently unusable.
func TestAnUndeclaredTokenIsLeftAlone(t *testing.T) {
	rendered := RenderAirtelText("Hi {{first_name}}, ref {{mystery}}.",
		[]string{"first_name"})

	if rendered != "Hi {{1}}, ref {{mystery}}." {
		t.Errorf("rendered = %q", rendered)
	}
}

func TestAirtelTemplateLimitsAreCheckedBeforeTheTwentyFourHourReview(t *testing.T) {
	valid := RCSTemplateSpec{
		Name: "Order update", UseCase: "TRANSACTIONAL",
		Text:      "Hi {{first_name}}, your order shipped.",
		Variables: []string{"first_name"}, SubmittedBy: "ops@acme.test",
	}
	if err := ValidateAirtelTemplate(valid); err != nil {
		t.Fatalf("a valid template was rejected: %v", err)
	}

	tooManyNames := make([]string, 16)
	body := ""
	for i := range tooManyNames {
		tooManyNames[i] = string(rune('a' + i))
		body += "{{" + tooManyNames[i] + "}} x "
	}

	cases := []struct {
		name string
		spec RCSTemplateSpec
	}{
		{"no name", RCSTemplateSpec{UseCase: "OTP", Text: "hi"}},
		{"name over 60 characters", RCSTemplateSpec{
			Name: string(make([]byte, 61)), UseCase: "OTP", Text: "hi"}},
		{"empty body", RCSTemplateSpec{Name: "n", UseCase: "OTP", Text: "   "}},
		{"body over 2500 characters", RCSTemplateSpec{
			Name: "n", UseCase: "OTP", Text: string(make([]byte, 2501))}},
		{"more than 15 variables", RCSTemplateSpec{
			Name: "n", UseCase: "OTP", Text: body, Variables: tooManyNames}},
		{"no use case", RCSTemplateSpec{Name: "n", Text: "hi"}},
	}
	for _, testCase := range cases {
		if err := ValidateAirtelTemplate(testCase.spec); err == nil {
			t.Errorf("%s was accepted", testCase.name)
		}
	}
}

// Airtel requires whitespace between variables and allows at most three in a
// row. Both are checked on the RENDERED text, which is what they actually see.
func TestAirtelVariableSpacingRulesAreEnforced(t *testing.T) {
	adjacent := RCSTemplateSpec{
		Name: "n", UseCase: "OTP", Text: "{{a}}{{b}} hello",
		Variables: []string{"a", "b"},
	}
	err := ValidateAirtelTemplate(adjacent)
	if err == nil {
		t.Fatal("two variables with nothing between them were accepted")
	}
	if !contains(err.Error(), "space between variables") {
		t.Errorf("error = %q, want it to name the spacing rule", err)
	}

	fourInARow := RCSTemplateSpec{
		Name: "n", UseCase: "OTP", Text: "{{a}} {{b}} {{c}} {{d}} end",
		Variables: []string{"a", "b", "c", "d"},
	}
	if err := ValidateAirtelTemplate(fourInARow); err == nil {
		t.Error("four consecutive variables were accepted; Airtel allows three")
	}

	threeInARow := RCSTemplateSpec{
		Name: "n", UseCase: "OTP", Text: "{{a}} {{b}} {{c}} end",
		Variables: []string{"a", "b", "c"},
	}
	if err := ValidateAirtelTemplate(threeInARow); err != nil {
		t.Errorf("three consecutive variables were rejected: %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Sequential per-variable replacement rewrites its own output. With variables
// ["x", "1"], substituting {{x}} produces {{1}}, and the pass for the variable
// literally named "1" then turns that into {{2}} — both slots read {{2}}, every
// message goes out with the wrong values, and nothing errors.
func TestAVariableNamedLikeAPositionDoesNotCollideWithOne(t *testing.T) {
	rendered := RenderAirtelText("{{x}} and {{1}}", []string{"x", "1"})

	if rendered != "{{1}} and {{2}}" {
		t.Errorf("rendered = %q, want %q", rendered, "{{1}} and {{2}}")
	}
}

// The same hazard in the other direction: a substitution must not be re-read by
// a later one whatever the declared order.
func TestSubstitutionsAreNotRewrittenByLaterVariables(t *testing.T) {
	rendered := RenderAirtelText("{{2}} {{a}} {{b}}", []string{"2", "a", "b"})

	if rendered != "{{1}} {{2}} {{3}}" {
		t.Errorf("rendered = %q, want %q", rendered, "{{1}} {{2}} {{3}}")
	}
}

func TestWhitespaceInsideATokenIsTolerated(t *testing.T) {
	rendered := RenderAirtelText("Hi {{ first_name }}!", []string{"first_name"})

	if rendered != "Hi {{1}}!" {
		t.Errorf("rendered = %q", rendered)
	}
}
