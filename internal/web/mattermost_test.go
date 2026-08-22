package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBookingIsConfirmedByDirectMessage(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"4"},
		"medlem": {"anna.andersson"}, "note": {"Storhandling"},
	}
	if rec := h.do("POST", "/resurs/ellastcykel/boka", form, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d. Body:\n%s", rec.Code, rec.Body.String())
	}

	dm := h.chat.waitForDM(t, 1)
	if dm.Username != "anna.andersson" {
		t.Errorf("the confirmation went to %q, want anna.andersson", dm.Username)
	}
	for _, want := range []string{"Ellastcykeln", "Storhandling", "Cykelrummet", "/bokning/"} {
		if !strings.Contains(dm.Message, want) {
			t.Errorf("the confirmation is missing %q:\n%s", want, dm.Message)
		}
	}
	if len(dm.Files) != 1 || !strings.HasSuffix(dm.Files[0], ".ics") {
		t.Errorf("the confirmation should carry one calendar file, got %v", dm.Files)
	}
}

func TestCancellationIsAlsoSentAsADirectMessage(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	// Anna's Mattermost is Swedish, so her messages are; that a member's own
	// language is followed is TestTheBotWritesInTheMembersOwnLanguage's job.
	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"anna.andersson"},
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d", rec.Code)
	}
	h.chat.waitForDM(t, 1)

	id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")
	b, err := h.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if rec := h.do("POST", "/bokning/"+id+"/avboka", url.Values{"token": {b.CancelToken}}, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d", rec.Code)
	}

	dm := h.chat.waitForDM(t, 2)
	if dm.Username != "anna.andersson" {
		t.Errorf("the cancellation went to %q, want anna.andersson", dm.Username)
	}
	if !strings.Contains(dm.Message, "avbokad") {
		t.Errorf("the message should say the booking is cancelled:\n%s", dm.Message)
	}
}

// The booking is stored with the Mattermost account, not with whatever the
// browser typed: the account is what every per-member rule counts against.
func TestBookingStoresTheMattermostAccount(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"@Anna.Andersson"},
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d. Body:\n%s", rec.Code, rec.Body.String())
	}
	id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")
	b, err := h.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if b.MMUsername != "anna.andersson" {
		t.Errorf("stored username = %q, want anna.andersson", b.MMUsername)
	}
	if b.MMUserID != "u-anna" {
		t.Errorf("stored account id = %q, want u-anna", b.MMUserID)
	}
	// The name and address come from Mattermost, so nobody has to type them.
	if b.Name != "Anna Andersson" || b.Email != "anna@example.se" {
		t.Errorf("name/address = %q/%q, want them filled in from Mattermost", b.Name, b.Email)
	}
}

func TestOnlyAllowedMembersMayBook(t *testing.T) {
	h := newHarnessAllowing(t, "mikael.ostberg")
	member := h.login("husets-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"anna.andersson"},
	}
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for someone outside the allow list", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bara öppen för mikael.ostberg") {
		t.Errorf("the page should say who may book:\n%s", rec.Body.String())
	}
	if got := h.chat.messages(); len(got) != 0 {
		t.Errorf("a refused booking must not send anything, got %v", got)
	}

	// The one person on the list still gets through.
	form.Set("medlem", "mikael.ostberg")
	if rec := h.do("POST", "/resurs/ellastcykel/boka", form, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("the allowed member was refused: %d\n%s", rec.Code, rec.Body.String())
	}
	if dm := h.chat.waitForDM(t, 1); dm.Username != "mikael.ostberg" {
		t.Errorf("confirmation went to %q", dm.Username)
	}
}

func TestMemberSearchOffersOnlyPeopleWhoMayBook(t *testing.T) {
	h := newHarnessAllowing(t, "mikael.ostberg")
	member := h.login("husets-losenord")

	suggest := func(term string) []memberSuggestion {
		t.Helper()
		body := h.do("GET", "/medlemmar?q="+url.QueryEscape(term), nil, member).Body.String()
		var out struct {
			Users []memberSuggestion `json:"users"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
		return out.Users
	}

	// Anna is in the directory but not on the allow list, so the picker must
	// not offer her: the form would only refuse her afterwards.
	if got := suggest("andersson"); len(got) != 0 {
		t.Errorf("search offered %+v, want nobody outside the allow list", got)
	}

	got := suggest("ostberg")
	if len(got) != 1 || got[0].Username != "mikael.ostberg" {
		t.Fatalf("search offered %+v, want mikael.ostberg", got)
	}
	if got[0].Name != "Mikael Östberg" {
		t.Errorf("name = %q, want the real name from Mattermost", got[0].Name)
	}
}

// A server-side search needs something to search for; a single letter would
// return most of the house.
func TestMemberSearchNeedsSomethingToSearchFor(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("GET", "/medlemmar?q=a", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"users":[]`) {
		t.Errorf("a one-letter search should return nobody, got %s", rec.Body.String())
	}
}

// The picker holds the whole list of people who may book and searches it in
// the browser, so the page asks for the directory rather than for a query.
func TestMemberDirectoryIsServedWholeAndSorted(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("GET", "/medlemmar", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out memberList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if out.Truncated {
		t.Error("a house this size fits in one list")
	}
	var got []string
	for _, u := range out.Users {
		got = append(got, u.Username)
	}
	// Sorted by the name a member reads, and the deactivated account is gone.
	want := []string{"anna.a", "anna.andersson", "bo.bengtsson", "cecilia.dahl", "mikael.ostberg"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("directory = %v, want %v", got, want)
	}
	for _, u := range out.Users {
		if u.Name == "" {
			t.Errorf("%s has no name to search by", u.Username)
		}
	}
}

// The list is the same for everyone, so it is fetched once and reused.
func TestMemberDirectoryIsCached(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	for i := 0; i < 3; i++ {
		if rec := h.do("GET", "/medlemmar", nil, member); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i+1, rec.Code)
		}
	}
	if n := h.chat.requests("users"); n != 1 {
		t.Errorf("Mattermost was asked for the directory %d times, want 1", n)
	}
}

// With an allow list there is nothing to page through: the names are known, so
// they are looked up directly.
func TestMemberDirectoryWithAnAllowListSkipsTheListing(t *testing.T) {
	h := newHarnessAllowing(t, "mikael.ostberg")
	member := h.login("husets-losenord")

	rec := h.do("GET", "/medlemmar", nil, member)
	var out memberList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(out.Users) != 1 || out.Users[0].Username != "mikael.ostberg" {
		t.Errorf("directory = %+v, want only mikael.ostberg", out.Users)
	}
	if n := h.chat.requests("users"); n != 0 {
		t.Errorf("the whole server was listed %d times; the allow list makes that unnecessary", n)
	}
}

// Someone who types a full name instead of a username must still get through:
// the picker fills in the username, but the field is plain text underneath.
func TestBookingAcceptsAFullName(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"Mikael Östberg"},
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d. Body:\n%s", rec.Code, rec.Body.String())
	}
	id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")
	b, err := h.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if b.MMUsername != "mikael.ostberg" || b.MMUserID != "u-mikael" {
		t.Errorf("stored %q/%q, want the account behind the name", b.MMUsername, b.MMUserID)
	}
}

// Two people can share a name. Guessing which one booked would be worse than
// asking.
func TestBookingRefusesAnAmbiguousName(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"Anna Andersson"},
	}, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Flera personer heter") {
		t.Errorf("the page should say the name is ambiguous:\n%s", body)
	}
	for _, want := range []string{"@anna.andersson", "@anna.a"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should offer %s to choose from", want)
		}
	}
	if got := h.chat.messages(); len(got) != 0 {
		t.Errorf("nothing should have been sent, got %v", got)
	}
}

// A name that matches nobody exactly must not book as whoever came closest.
func TestBookingNeverGuessesBetweenPeople(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"Anders"}, // brushes past both Anderssons
	}, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	// A half-typed name that fits several people is a choice, not a dead end:
	// the page says who they are so the next keystroke settles it.
	if !strings.Contains(body, "Flera personer matchar") {
		t.Errorf("the page should name the candidates:\n%s", body)
	}
	for _, want := range []string{"@anna.andersson", "@anna.a"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should offer %s to choose from", want)
		}
	}
	if got := h.chat.messages(); len(got) != 0 {
		t.Errorf("nothing should have been booked or sent, got %v", got)
	}
}

// Half a name that fits exactly one person is that person. Refusing it would
// mean knowing the answer and making the member type it out anyway.
func TestBookingResolvesHalfATypedName(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	for _, typed := range []string{"Cecilia", "Dahl", "cecilia dahl", "CECILIA DAHL"} {
		day := h.date(1)
		rec := h.do("POST", "/resurs/gastrum-1/boka", url.Values{
			"fran": {day}, "till": {h.date(2)}, "medlem": {typed},
		}, member)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%q was refused: %d\n%s", typed, rec.Code, rec.Body.String())
		}
		id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")
		b, err := h.store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("read booking: %v", err)
		}
		if b.MMUsername != "cecilia.dahl" {
			t.Errorf("%q booked as %q, want cecilia.dahl", typed, b.MMUsername)
		}
		// The receipt has to name who it booked, since the field was vague.
		page := h.do("GET", "/bokning/"+id, nil, member).Body.String()
		if !strings.Contains(page, "Cecilia Dahl") || !strings.Contains(page, "@cecilia.dahl") {
			t.Errorf("the confirmation should name the person it resolved to:\n%s", page)
		}
		if err := h.store.Cancel(context.Background(), id, "", true, h.now); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	}
}

// --- Looking up someone's bookings ------------------------------------------

// The lookup field is the same field as on the booking form, so it has to
// understand the same things. Typing a name used to find nobody, silently.
func TestMyBookingsFindsThePersonByName(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"cecilia.dahl"},
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("seed: %d\n%s", rec.Code, rec.Body.String())
	}

	for _, typed := range []string{"cecilia.dahl", "@Cecilia.Dahl", "Cecilia Dahl", "Cecilia", "dahl"} {
		body := h.do("GET", "/mina?medlem="+url.QueryEscape(typed), nil, member).Body.String()
		if !strings.Contains(body, "Ellastcykeln") {
			t.Errorf("looking up %q found no bookings:\n%s", typed, body)
		}
		// The field comes back holding the account it resolved to, so the next
		// look-up is exact and the member can see who they are looking at.
		if !strings.Contains(body, `value="cecilia.dahl"`) {
			t.Errorf("looking up %q left %q in the field, want the resolved username", typed, "")
		}
	}
}

func TestMyBookingsSaysSoWhenNobodyMatches(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/mina?medlem=Ingrid", nil, member).Body.String()
	if !strings.Contains(body, "Hittar ingen i husets Mattermost") {
		t.Errorf("an unknown name should be explained, not answered with an empty list:\n%s", body)
	}
	// And what they typed is still there to correct.
	if !strings.Contains(body, `value="Ingrid"`) {
		t.Error("the field should keep what was typed")
	}
}

func TestMyBookingsNamesTheCandidatesWhenAmbiguous(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/mina?medlem="+url.QueryEscape("Anna Andersson"), nil, member).Body.String()
	if !strings.Contains(body, "Flera personer heter") {
		t.Errorf("two people share that name, so the page should say so:\n%s", body)
	}
}

// Looking up bookings is not booking: it works for anyone in the house, not
// only the people the allow list lets book.
func TestMyBookingsIsNotLimitedByTheBookingAllowList(t *testing.T) {
	h := newHarnessAllowing(t, "mikael.ostberg")
	member := h.login("husets-losenord")

	body := h.do("GET", "/mina?medlem=cecilia.dahl", nil, member).Body.String()
	if strings.Contains(body, "bara öppen för") {
		t.Errorf("the allow list should not block looking someone up:\n%s", body)
	}
	if !strings.Contains(body, "Inga bokningar för Cecilia Dahl") {
		t.Errorf("the page should name who it looked up:\n%s", body)
	}
}

func TestMemberSearchIsBehindThePassword(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/medlemmar?q=anna", nil, nil)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("the directory must not be readable without a session, got %d", rec.Code)
	}
}

// Deactivated accounts are not people who can be booked for.
func TestMemberSearchSkipsDeactivatedAccounts(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	body := h.do("GET", "/medlemmar?q=granne", nil, member).Body.String()
	if strings.Contains(body, "gammal.granne") {
		t.Errorf("a deactivated account should not be offered: %s", body)
	}
}

// The per-resource cap counts the Mattermost account, so booking twice as the
// same person is what trips it — not two different spellings of a name.
func TestPerMemberCapCountsTheMattermostAccount(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	for i, day := range []int{1, 2} {
		rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
			"datum": {h.date(day)}, "start": {"10:00"}, "langd": {"2"},
			"medlem": {"anna.andersson"},
		}, member)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("booking %d = %d\n%s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The bike allows two at a time; a third must be refused even though the
	// name is spelled differently.
	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(3)}, "start": {"10:00"}, "langd": {"2"},
		"name": {"Annika"}, "medlem": {"Anna.Andersson"},
	}, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("third booking = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aktiva bokningar") {
		t.Errorf("the page should explain the cap:\n%s", rec.Body.String())
	}
}
