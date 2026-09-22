package demoseed

import "testing"

// India's operators do not judge whether a message looks reasonable: they match
// it against a content template registered on DLT, by id. An approved India
// template with no content-template id cannot send at all — SMPPRouter.Submit
// refuses it as DLT_IDS_MISSING rather than letting the operator scrub it — and
// that is exactly the state the seeder left the demo tenant's flagship SMS
// template in, because the insert never named external_id.
//
// No database: this is a property of the fixture table, and it is the table
// that was wrong.
func TestEveryIndiaTemplateFixtureCarriesWhatDLTRequires(t *testing.T) {
	for _, fixture := range demoTemplates {
		if fixture.channel != "SMS" && fixture.channel != "RCS" {
			continue
		}
		if fixture.dltCategory == "" {
			t.Errorf("%q is an India %s template with no DLT category",
				fixture.name, fixture.channel)
		}
		// Only the approved ones. A template still in review legitimately has
		// no id yet, because DLT issues one when it approves the words.
		if fixture.status == "approved" && fixture.registrationID == "" {
			t.Errorf("%q is approved with no DLT content-template id, so it cannot send",
				fixture.name)
		}
	}
}

// SMS and RCS declare no Meta category — in India their taxonomy is DLT's, and
// a second classification beside it is one no operator reads.
func TestNoIndiaSMSOrRCSFixtureCarriesAMetaCategory(t *testing.T) {
	for _, fixture := range demoTemplates {
		if fixture.channel != "SMS" && fixture.channel != "RCS" {
			continue
		}
		if fixture.category != "" {
			t.Errorf("%q is %s and carries the Meta category %q, which PATCH refuses",
				fixture.name, fixture.channel, fixture.category)
		}
	}
}
