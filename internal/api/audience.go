package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func contactListResponse(l store.ContactList) gen.ContactList {
	counts := l.ConsentedCounts
	if counts == nil {
		counts = map[string]int{}
	}
	return gen.ContactList{
		Id:              l.ID,
		Name:            l.Name,
		ContactCount:    l.ContactCount,
		ConsentedCounts: counts,
		CreatedAt:       l.CreatedAt,
	}
}

func (s *Server) ListContactLists(ctx context.Context, _ gen.ListContactListsRequestObject) (gen.ListContactListsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListContactLists401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	lists, err := store.ListContactLists(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.ContactList, 0, len(lists))
	for _, list := range lists {
		out = append(out, contactListResponse(list))
	}
	return gen.ListContactLists200JSONResponse(out), nil
}

func (s *Server) CreateContactList(ctx context.Context, request gen.CreateContactListRequestObject) (gen.CreateContactListResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.CreateContactList401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return gen.CreateContactList422JSONResponse(
			errorBody(codeValidation, "A list name is required.")), nil
	}
	list, err := store.CreateContactList(ctx, s.DB, identity, name)
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateContactList422JSONResponse(
			errorBody(codeValidation, "A list with that name already exists.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateContactList201JSONResponse(contactListResponse(list)), nil
}

func (s *Server) GetContactList(ctx context.Context, request gen.GetContactListRequestObject) (gen.GetContactListResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetContactList401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	listID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.GetContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	list, err := store.GetContactList(ctx, s.DB, identity, listID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetContactList200JSONResponse(contactListResponse(list)), nil
}

func (s *Server) RenameContactList(ctx context.Context, request gen.RenameContactListRequestObject) (gen.RenameContactListResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RenameContactList401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return gen.RenameContactList404JSONResponse(
			errorBody(codeValidation, "A list name is required.")), nil
	}
	listID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.RenameContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	list, err := store.RenameContactList(ctx, s.DB, identity, listID, name)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
		return gen.RenameContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RenameContactList200JSONResponse(contactListResponse(list)), nil
}

func (s *Server) DeleteContactList(ctx context.Context, request gen.DeleteContactListRequestObject) (gen.DeleteContactListResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.DeleteContactList401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	listID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.DeleteContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	err = store.DeleteContactList(ctx, s.DB, identity, listID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DeleteContactList404JSONResponse(errorBody(codeNotFound, "No such list.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteContactList204Response{}, nil
}

func (s *Server) RemoveContactListMember(ctx context.Context, request gen.RemoveContactListMemberRequestObject) (gen.RemoveContactListMemberResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RemoveContactListMember401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	listID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.RemoveContactListMember404JSONResponse(
			errorBody(codeNotFound, "That contact is not on this list.")), nil
	}
	contactID, err := uuid.Parse(request.ContactId)
	if err != nil {
		return gen.RemoveContactListMember404JSONResponse(
			errorBody(codeNotFound, "That contact is not on this list.")), nil
	}
	err = store.RemoveContactListMember(ctx, s.DB, identity, listID, contactID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RemoveContactListMember404JSONResponse(
			errorBody(codeNotFound, "That contact is not on this list.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RemoveContactListMember204Response{}, nil
}

func contactResponse(c store.Contact) gen.Contact {
	contact := gen.Contact{
		Id:        c.ID,
		Msisdn:    c.Msisdn,
		Email:     c.Email,
		Country:   gen.CountryCode(c.Country),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
		Consent:   map[string]gen.ConsentState{},
	}
	for channel, state := range c.Consent {
		contact.Consent[channel] = gen.ConsentState(state)
	}
	if first, ok := c.Fields["firstName"]; ok && first != "" {
		contact.Fields.FirstName = &first
	}
	if last, ok := c.Fields["lastName"]; ok && last != "" {
		contact.Fields.LastName = &last
	}
	if city, ok := c.Fields["city"]; ok && city != "" {
		contact.Fields.City = &city
	}
	return contact
}

func (s *Server) ListContacts(ctx context.Context, request gen.ListContactsRequestObject) (gen.ListContactsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListContacts401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}

	var listID *uuid.UUID
	if request.Params.ListId != nil && *request.Params.ListId != "" {
		parsed, err := uuid.Parse(*request.Params.ListId)
		if err != nil {
			return gen.ListContacts401JSONResponse(
				errorBody(codeValidation, "That list id is not valid.")), nil
		}
		listID = &parsed
	}
	cursor, limit := "", 50
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
		// Bound user input here rather than trusting the store's ceiling: the
		// store allows larger pages so internal fan-out can batch properly, and
		// that headroom must not be reachable from a query string.
		if limit > 200 {
			limit = 200
		}
	}

	contacts, total, next, err := store.ListContacts(ctx, s.DB, identity, listID, cursor, limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.ListContacts401JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
	if err != nil {
		return nil, err
	}

	out := make([]gen.Contact, 0, len(contacts))
	for _, contact := range contacts {
		out = append(out, contactResponse(contact))
	}
	page := gen.ContactPage{Contacts: out, Total: total}
	if next != "" {
		page.NextCursor = &next
	}
	return gen.ListContacts200JSONResponse(page), nil
}

// ImportContacts upserts a batch of rows into a list.
//
// Idempotency is a correctness control here, not a convenience: a resubmitted
// import would duplicate contacts, and duplicate contacts mean sending the same
// person the same message twice and billing for both.
func (s *Server) ImportContacts(ctx context.Context, request gen.ImportContactsRequestObject) (gen.ImportContactsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ImportContacts401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}

	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(*request.Params.IdempotencyKey)
	}
	if idempotencyKey != "" {
		stored, found, err := store.FindIdempotentResponse(ctx, s.DB, identity,
			"contacts.import", idempotencyKey)
		if err != nil {
			return nil, err
		}
		if found {
			var summary gen.ImportSummary
			if err := json.Unmarshal(stored, &summary); err == nil {
				return gen.ImportContacts200JSONResponse(summary), nil
			}
		}
	}

	country := string(request.Body.DefaultCountry)
	if _, known := audience.NormaliseMsisdn("9999999999", country); !known && country != "AE" {
		// A country with no number plan cannot normalise anything, so every row
		// would be invalid — better to say so once than 10,000 times.
		return gen.ImportContacts422JSONResponse(errorBody(codeValidation,
			"We cannot normalise phone numbers for that country yet.")), nil
	}

	// Resolve the destination list: an existing one, or a new one by name.
	var listID uuid.UUID
	switch {
	case request.Body.TargetListId != nil && *request.Body.TargetListId != "":
		parsed, err := uuid.Parse(*request.Body.TargetListId)
		if err != nil {
			return gen.ImportContacts422JSONResponse(
				errorBody(codeValidation, "That list does not exist.")), nil
		}
		if _, err := store.GetContactList(ctx, s.DB, identity, parsed); err != nil {
			return gen.ImportContacts422JSONResponse(
				errorBody(codeValidation, "That list does not exist.")), nil
		}
		listID = parsed
	case request.Body.NewListName != nil && strings.TrimSpace(*request.Body.NewListName) != "":
		created, err := store.CreateContactList(ctx, s.DB, identity,
			strings.TrimSpace(*request.Body.NewListName))
		if errors.Is(err, store.ErrConflict) {
			return gen.ImportContacts422JSONResponse(
				errorBody(codeValidation, "A list with that name already exists.")), nil
		}
		if err != nil {
			return nil, err
		}
		listID = created.ID
	default:
		return gen.ImportContacts422JSONResponse(errorBody(codeValidation,
			"Choose an existing list or name a new one.")), nil
	}

	consent := map[string]string{}
	for channel, state := range request.Body.ConsentBasis {
		consent[channel] = string(state)
	}

	rows := make([]store.ImportRow, 0, len(request.Body.Rows))
	conflicts := []store.ImportConflict{}
	invalid := 0
	skipped := 0
	seen := map[string]bool{}

	for _, row := range request.Body.Rows {
		// Provenance is threaded through from the client's preview. An absent
		// or out-of-range line is reported as unknown rather than guessed from
		// array position — the contract is explicit about this, because the
		// client may have compacted the array after filtering.
		var line *int
		if row.Line != nil && *row.Line >= 2 {
			value := *row.Line
			line = &value
		}

		msisdn, valid := audience.NormaliseMsisdn(row.Msisdn, country)
		if !valid {
			invalid++
			conflicts = append(conflicts, store.ImportConflict{
				Line: line, Msisdn: row.Msisdn, Reason: "invalid_msisdn",
			})
			continue
		}

		// A number repeated within one file is skipped rather than applied
		// twice; the last write would otherwise silently win.
		if seen[msisdn] {
			skipped++
			conflicts = append(conflicts, store.ImportConflict{
				Line: line, Msisdn: msisdn, Reason: "duplicate_in_file",
			})
			continue
		}
		seen[msisdn] = true

		// A suppressed contact is never imported into a sendable list. This is
		// the product's core promise: someone who opted out stays out.
		if suppressed, err := store.IsSuppressed(ctx, s.DB, identity, msisdn); err != nil {
			return nil, err
		} else if suppressed {
			skipped++
			conflicts = append(conflicts, store.ImportConflict{
				Line: line, Msisdn: msisdn, Reason: "suppressed",
			})
			continue
		}

		imported := store.ImportRow{Msisdn: msisdn, Line: line}
		if row.FirstName != nil {
			imported.FirstName = *row.FirstName
		}
		if row.LastName != nil {
			imported.LastName = *row.LastName
		}
		if row.City != nil {
			imported.City = *row.City
		}
		if row.Email != nil {
			if normalised, ok := audience.NormaliseEmail(*row.Email); ok {
				imported.Email = &normalised
			}
		}
		rows = append(rows, imported)
	}

	outcome, err := store.ImportContacts(ctx, s.DB, identity, listID, country, consent, rows)
	if err != nil {
		return nil, err
	}
	outcome.Invalid = invalid
	outcome.Skipped = skipped
	outcome.Conflicts = conflicts

	summary := importSummaryResponse(outcome)
	if idempotencyKey != "" {
		if encoded, err := json.Marshal(summary); err == nil {
			if err := store.SaveIdempotentResponse(ctx, s.DB, identity,
				"contacts.import", idempotencyKey, encoded); err != nil {
				return nil, err
			}
		}
	}
	return gen.ImportContacts200JSONResponse(summary), nil
}

func importSummaryResponse(o store.ImportOutcome) gen.ImportSummary {
	summary := gen.ImportSummary{
		Created: o.Created, Updated: o.Updated,
		Skipped: o.Skipped, Invalid: o.Invalid, ListId: o.ListID,
	}
	summary.Conflicts = make([]struct {
		Email  string `json:"email"`
		Line   *int   `json:"line"`
		Msisdn string `json:"msisdn"`
		Reason string `json:"reason"`
	}, 0, len(o.Conflicts))

	for _, conflict := range o.Conflicts {
		email := ""
		if conflict.Email != nil {
			email = *conflict.Email
		}
		summary.Conflicts = append(summary.Conflicts, struct {
			Email  string `json:"email"`
			Line   *int   `json:"line"`
			Msisdn string `json:"msisdn"`
			Reason string `json:"reason"`
		}{Email: email, Line: conflict.Line, Msisdn: conflict.Msisdn, Reason: conflict.Reason})
	}
	return summary
}

func (s *Server) ListSuppressions(ctx context.Context, request gen.ListSuppressionsRequestObject) (gen.ListSuppressionsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	cursor, limit := "", 50
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
		// Bound user input here rather than trusting the store's ceiling: the
		// store allows larger pages so internal fan-out can batch properly, and
		// that headroom must not be reachable from a query string.
		if limit > 200 {
			limit = 200
		}
	}

	suppressions, next, err := store.ListSuppressions(ctx, s.DB, identity, cursor, limit)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Suppression, 0, len(suppressions))
	for _, suppression := range suppressions {
		out = append(out, gen.Suppression{
			Msisdn:    suppression.Msisdn,
			Email:     suppression.Email,
			Reason:    gen.SuppressionReason(suppression.Reason),
			Note:      &suppression.Note,
			CreatedAt: suppression.CreatedAt,
		})
	}
	page := gen.SuppressionPage{Suppressions: out}
	if next != "" {
		page.NextCursor = &next
	}
	return gen.ListSuppressions200JSONResponse(page), nil
}

func (s *Server) AddSuppressions(ctx context.Context, request gen.AddSuppressionsRequestObject) (gen.AddSuppressionsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}

	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	reason := string(request.Body.Reason)

	created, skipped, invalid := 0, 0, 0

	if request.Body.Msisdns != nil {
		for _, raw := range *request.Body.Msisdns {
			// The suppression list spans countries, so this uses the
			// country-agnostic E.164 rule rather than a number plan.
			msisdn, ok := audience.NormaliseE164(raw)
			if !ok {
				invalid++
				continue
			}
			value := msisdn
			added, err := store.AddSuppression(ctx, s.DB, identity, store.Suppression{
				Identity: msisdn, Msisdn: &value, Reason: reason, Note: note,
			})
			if err != nil {
				return nil, err
			}
			if added {
				created++
			} else {
				skipped++
			}
		}
	}

	if request.Body.Emails != nil {
		for _, raw := range *request.Body.Emails {
			email, ok := audience.NormaliseEmail(raw)
			if !ok {
				invalid++
				continue
			}
			value := email
			added, err := store.AddSuppression(ctx, s.DB, identity, store.Suppression{
				Identity: email, Email: &value, Reason: reason, Note: note,
			})
			if err != nil {
				return nil, err
			}
			if added {
				created++
			} else {
				skipped++
			}
		}
	}

	return gen.AddSuppressions200JSONResponse{
		Created: created, Skipped: skipped, Invalid: invalid,
	}, nil
}

func (s *Server) RemoveSuppression(ctx context.Context, request gen.RemoveSuppressionRequestObject) (gen.RemoveSuppressionResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// Normalise the same way the entry was stored, so a caller passing an
	// unformatted number still removes the right row.
	identityValue := strings.TrimSpace(request.Identity)
	if normalised, ok := audience.NormaliseE164(identityValue); ok {
		identityValue = normalised
	} else if normalised, ok := audience.NormaliseEmail(identityValue); ok {
		identityValue = normalised
	}

	if err := store.RemoveSuppression(ctx, s.DB, identity, identityValue); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	// Removing something already absent is a success: the desired end state
	// holds either way, and the contract offers no 404 here.
	return gen.RemoveSuppression204Response{}, nil
}
