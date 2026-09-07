package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/mattermost"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

const testConfig = `
site:
  title: Rudbeckia bokning
  timezone: Europe/Stockholm
categories:
  - id: cyklar
    name: Cyklar
    description: Husets gemensamma cyklar.
    link: https://chat.example.com/channels/cykelpoolen
    link_text: "#cykelpoolen"
  - id: gastrum
    name: Gästrum
resources:
  - id: ellastcykel
    category: cyklar
    name: Ellastcykeln
    location: Cykelrummet
    info_url: https://example.com/huset/cykelrummet/
    booking:
      mode: hours
      durations: [1, 2, 4, 8]
      custom_duration: true
      min_duration_minutes: 30
      max_duration_minutes: 600
      slot_step_minutes: 30
      buffer_minutes: 15
      open_from: "06:00"
      open_to: "22:00"
      max_advance_days: 30
      max_active_per_user: 2
  - id: elcykel
    category: cyklar
    name: Elcykeln
    booking:
      mode: hours
      durations: [1, 2, 4]
      slot_step_minutes: 30
      open_from: "06:00"
      open_to: "22:00"
      max_advance_days: 30
  - id: tvattstugan
    category: cyklar
    name: Tvättstugan
    booking:
      mode: hours
      durations: [2]
      slot_step_minutes: 120
      open_from: "06:00"
      open_to: "22:00"
      max_advance_days: 30
  - id: gastrum-1
    category: gastrum
    name: Gästrum 1
    booking:
      mode: days
      min_days: 1
      max_days: 7
      max_advance_days: 180
  - id: avstangd
    category: cyklar
    name: Avstängd cykel
    enabled: false
    booking:
      mode: hours
`

// harness is a fully wired server with a frozen clock, so tests can talk about
// concrete dates.
type harness struct {
	*testing.T
	server *Server
	mux    http.Handler
	store  *store.Store
	chat   *fakeMattermost
	now    time.Time
	loc    *time.Location
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessAllowing(t)
}

// newHarnessAllowing builds a server where only the named Mattermost users may
// book. With no names, everyone in the directory may.
func newHarnessAllowing(t *testing.T, allow ...string) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	chat := newFakeMattermost(t)
	rt := config.Runtime{
		BaseURL:    "https://booking.rudbeckia.nu",
		TrustProxy: true,
		Mattermost: config.MattermostSettings{
			URL:   chat.URL,
			Token: "test-token",
			Allow: allow,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	guard := auth.New("husets-losenord", "admin-losenord", []byte("secret-for-tests"), time.Hour, false)
	bot := mattermost.New(rt.Mattermost.URL, rt.Mattermost.Token, log)
	srv, err := New(cfg, rt, st, guard, bot, log)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	loc := cfg.Location()
	// A Monday morning, well clear of any DST boundary.
	now := time.Date(2026, 5, 4, 8, 0, 0, 0, loc)
	srv.now = func() time.Time { return now }

	return &harness{T: t, server: srv, mux: srv.Handler(), store: st, chat: chat, now: now, loc: loc}
}

// login returns the session cookies for a password.
func (h *harness) login(password string) []*http.Cookie {
	h.Helper()
	form := url.Values{"password": {password}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		h.Fatalf("login as %q: status %d", password, rec.Code)
	}
	return rec.Result().Cookies()
}

func (h *harness) do(method, target string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	h.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func (h *harness) date(offsetDays int) string {
	return h.now.AddDate(0, 0, offsetDays).Format("2006-01-02")
}

func TestEverythingIsBehindThePassword(t *testing.T) {
	h := newHarness(t)
	protected := []string{"/", "/resurs/ellastcykel", "/mina", "/admin",
		"/bokning/whatever", "/kalender/ellastcykel/flode.ics"}

	for _, path := range protected {
		rec := h.do("GET", path, nil, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s without a session = %d, want a redirect to /login", path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("GET %s redirected to %q, want /login", path, loc)
		}
	}

	// The login page and health check stay open.
	for _, path := range []string{"/login", "/healthz"} {
		if rec := h.do("GET", path, nil, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestLoginRejectsTheWrongPassword(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/login", url.Values{"password": {"gissning"}}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Fel lösenord") {
		t.Error("the page should say the password was wrong")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a failed login must not set a session cookie")
	}
}

func TestLoginRedirectsOnlyWithinTheSite(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"/mina":               "/mina",
		"":                    "/",
		"https://example.com": "/",
		"//example.com":       "/",
	}
	for next, want := range cases {
		rec := h.do("POST", "/login", url.Values{"password": {"husets-losenord"}, "next": {next}}, nil)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("next=%q redirected to %q, want %q", next, got, want)
		}
	}
}

func TestMemberSeesTheResourcesButNotTheAdminView(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/", nil, member).Body.String()
	for _, want := range []string{"Ellastcykeln", "Gästrum 1", "Cyklar"} {
		if !strings.Contains(body, want) {
			t.Errorf("the start page is missing %q", want)
		}
	}
	if strings.Contains(body, "Avstängd cykel") {
		t.Error("a disabled resource must not be offered")
	}
	if strings.Contains(body, "Alla bokningar") {
		t.Error("members should not see the admin link")
	}

	if rec := h.do("GET", "/admin", nil, member); rec.Code != http.StatusForbidden {
		t.Errorf("member reaching /admin = %d, want 403", rec.Code)
	}
}

func TestDisabledResourcePageIsNotFound(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	if rec := h.do("GET", "/resurs/avstangd", nil, member); rec.Code != http.StatusNotFound {
		t.Errorf("disabled resource = %d, want 404", rec.Code)
	}
	if rec := h.do("GET", "/resurs/finns-inte", nil, member); rec.Code != http.StatusNotFound {
		t.Errorf("unknown resource = %d, want 404", rec.Code)
	}
}

// The whole path a member walks: pick a slot, book it, get a confirmation,
// see the slot disappear, then cancel it and see it come back.
func TestBookingLifecycle(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	tomorrow := h.date(1)

	page := h.do("GET", "/resurs/ellastcykel?datum="+tomorrow+"&langd=4", nil, member).Body.String()
	if !strings.Contains(page, ">10:00<") {
		t.Fatal("10:00 should be offered on an empty day")
	}

	form := url.Values{
		"datum": {tomorrow}, "start": {"10:00"}, "langd": {"4"},
		"name": {"Anna Andersson"}, "apartment": {"1403"},
		"medlem": {"anna.andersson"}, "note": {"Storhandling"}, "remember": {"1"},
	}
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d, want a redirect. Body:\n%s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/bokning/") {
		t.Fatalf("redirected to %q", location)
	}
	// The member's details are remembered for next time.
	session := append(member, rec.Result().Cookies()...)

	confirm := h.do("GET", location, nil, session).Body.String()
	for _, want := range []string{"Klart", "Ellastcykeln", "10:00", "14:00", "Storhandling",
		"calendar.google.com", "Cykelrummet"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("the confirmation page is missing %q", want)
		}
	}

	// The slot is gone, and the buffer around it too.
	page = h.do("GET", "/resurs/ellastcykel?datum="+tomorrow+"&langd=1", nil, session).Body.String()
	if strings.Count(page, "Din bokning") == 0 {
		t.Error("the member's own booking should be labelled on the grid")
	}

	id := strings.TrimSuffix(strings.TrimPrefix(location, "/bokning/"), "?ny=1")

	// It shows up under "my bookings" thanks to the remembered address.
	mine := h.do("GET", "/mina", nil, session).Body.String()
	if !strings.Contains(mine, id) {
		t.Error("the booking is missing from /mina")
	}

	// The .ics download carries the right times.
	ics := h.do("GET", "/bokning/"+id+"/kalender.ics", nil, session)
	if ct := ics.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("ics content type = %q", ct)
	}
	if !strings.Contains(ics.Body.String(), "DTSTART:20260505T080000Z") {
		t.Errorf("ics start time is wrong:\n%s", ics.Body.String())
	}

	// Cancelling needs the token from the confirmation page.
	token := between(confirm, `name="token" value="`, `"`)
	if token == "" {
		t.Fatal("no cancel token on the confirmation page")
	}
	if rec := h.do("POST", "/bokning/"+id+"/avboka", url.Values{"token": {"fel"}}, h.login("husets-losenord")); rec.Code != http.StatusForbidden {
		t.Errorf("cancelling with a bad token = %d, want 403", rec.Code)
	}
	if rec := h.do("POST", "/bokning/"+id+"/avboka", url.Values{"token": {token}}, session); rec.Code != http.StatusSeeOther {
		t.Errorf("cancelling = %d, want a redirect", rec.Code)
	}

	// And the slot is bookable again.
	page = h.do("GET", "/resurs/ellastcykel?datum="+tomorrow+"&langd=4", nil, session).Body.String()
	if !strings.Contains(page, ">10:00<") {
		t.Error("the cancelled slot should be free again")
	}
}

func TestDoubleBookingIsRefused(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	tomorrow := h.date(1)

	form := url.Values{
		"datum": {tomorrow}, "start": {"10:00"}, "langd": {"4"},
		"name": {"Anna"}, "medlem": {"anna.andersson"},
	}
	if rec := h.do("POST", "/resurs/ellastcykel/boka", form, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("first booking = %d", rec.Code)
	}

	form.Set("name", "Bo")
	form.Set("medlem", "bo.bengtsson")
	form.Set("start", "12:00")
	form.Set("langd", "1")
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("overlapping booking = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bokad") {
		t.Error("the page should explain that the time is taken")
	}
}

func TestInvalidBookingKeepsTheFormFilledIn(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"4"},
		"name": {"Anna Andersson"}, "apartment": {"1403"}, "medlem": {"finns.inte"},
	}
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hittar ingen i husets Mattermost") {
		t.Error("the validation message is missing")
	}
	for _, want := range []string{`value="Anna Andersson"`, `value="1403"`, `value="finns.inte"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lost %s, so the member would have to retype it", want)
		}
	}
}

func TestGuestRoomBookingUsesNights(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"fran": {h.date(10)}, "till": {h.date(13)},
		"name": {"Anna"}, "medlem": {"anna.andersson"},
	}
	rec := h.do("POST", "/resurs/gastrum-1/boka", form, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d. Body:\n%s", rec.Code, rec.Body.String())
	}
	confirm := h.do("GET", rec.Header().Get("Location"), nil, member).Body.String()
	if !strings.Contains(confirm, "3 nätter") {
		t.Error("the confirmation should say three nights")
	}
	if !strings.Contains(confirm, "15:00") || !strings.Contains(confirm, "12:00") {
		t.Error("check-in and check-out times are missing from the confirmation")
	}

	// Those nights are now blocked on the calendar.
	page := h.do("GET", "/resurs/gastrum-1", nil, member).Body.String()
	if strings.Count(page, "cal-day is-taken") != 3 {
		t.Errorf("expected exactly 3 booked nights on the calendar, got %d",
			strings.Count(page, "cal-day is-taken"))
	}
}

func TestAdminSeesEveryBookingAndCanCancel(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	admin := h.login("admin-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"4"},
		"name": {"Anna Andersson"}, "medlem": {"anna.andersson"}, "apartment": {"1403"},
	}
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")

	body := h.do("GET", "/admin", nil, admin).Body.String()
	for _, want := range []string{"Anna Andersson", "anna@example.se", "1403", "Ellastcykeln", "bekräftad"} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin view is missing %q", want)
		}
	}

	csv := h.do("GET", "/admin/export.csv?period=all", nil, admin)
	if ct := csv.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("csv content type = %q", ct)
	}
	if !strings.Contains(csv.Body.String(), "anna@example.se") {
		t.Error("the export is missing the booking")
	}

	// The admin can cancel without holding anyone's token.
	if rec := h.do("POST", "/admin/avboka/"+id, nil, admin); rec.Code != http.StatusSeeOther {
		t.Errorf("admin cancel = %d, want a redirect", rec.Code)
	}
	body = h.do("GET", "/admin?period=all", nil, admin).Body.String()
	if !strings.Contains(body, "avbokad") {
		t.Error("the cancelled booking should be listed as cancelled")
	}
}

func TestAdminFilters(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	admin := h.login("admin-losenord")

	h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"name": {"Anna Andersson"}, "medlem": {"anna.andersson"},
	}, member)
	h.do("POST", "/resurs/gastrum-1/boka", url.Values{
		"fran": {h.date(10)}, "till": {h.date(12)},
		"name": {"Bo Bengtsson"}, "medlem": {"bo.bengtsson"},
	}, member)

	byResource := h.do("GET", "/admin?resurs=gastrum-1", nil, admin).Body.String()
	if !strings.Contains(byResource, "Bo Bengtsson") || strings.Contains(byResource, "Anna Andersson") {
		t.Error("filtering by resource did not narrow the list")
	}

	bySearch := h.do("GET", "/admin?sok=andersson", nil, admin).Body.String()
	if !strings.Contains(bySearch, "Anna Andersson") || strings.Contains(bySearch, "Bo Bengtsson") {
		t.Error("the free-text search did not narrow the list")
	}
}

func TestResourceFeedListsBookings(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"name": {"Anna"}, "medlem": {"anna.andersson"},
	}, member)

	rec := h.do("GET", "/kalender/ellastcykel/flode.ics", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Count(body, "BEGIN:VEVENT") != 1 {
		t.Errorf("feed has %d events, want 1", strings.Count(body, "BEGIN:VEVENT"))
	}
	if !strings.Contains(body, "X-WR-CALNAME:Rudbeckia bokning – Ellastcykeln") {
		t.Error("the feed should be named after the resource")
	}
}

func TestSecurityHeadersAndNoIndexing(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/login", nil, nil)
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if !strings.Contains(rec.Body.String(), `name="robots" content="noindex`) {
		t.Error("pages should ask search engines to stay away")
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	rec := h.do("POST", "/logout", nil, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", rec.Code)
	}
	cleared := rec.Result().Cookies()
	if len(cleared) == 0 || cleared[0].MaxAge >= 0 {
		t.Error("logout should expire the session cookie")
	}
	if rec := h.do("GET", "/", nil, cleared); rec.Code != http.StatusSeeOther {
		t.Error("the cleared cookie should no longer grant access")
	}
}

// directoryUsername maps "Anna Andersson" to the username the fake directory
// knows her by.
func directoryUsername(name string) string {
	parts := strings.Fields(strings.ToLower(name))
	return strings.Join(parts, ".")
}

// between returns the text between two markers, or "".
func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return s[:j]
}

// Landing on a resource whose day is already fully booked should jump to the
// next day that has something free, not show an empty grid.
func TestResourcePageSkipsToTheFirstFreeDay(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	// Fill tomorrow completely with back-to-back bookings.
	day := h.now.AddDate(0, 0, 1)
	for hour := 6; hour < 22; hour += 2 {
		b := storeBooking(h, day, hour, 2)
		if err := h.store.Create(context.Background(), b, b.Start, b.End); err != nil {
			t.Fatalf("seed %d: %v", hour, err)
		}
	}

	// Today is already past its opening hours in this harness? No: the clock is
	// 08:00, so today has free slots and should be chosen.
	body := h.do("GET", "/resurs/ellastcykel", nil, member).Body.String()
	if !strings.Contains(body, h.now.Format("2006-01-02")) {
		t.Error("with free slots today, the page should open on today")
	}

	// Now block today as well; the page must skip forward past tomorrow.
	today := h.now
	for hour := 8; hour < 22; hour += 2 {
		b := storeBooking(h, today, hour, 2)
		if err := h.store.Create(context.Background(), b, b.Start, b.End); err != nil {
			t.Fatalf("seed today %d: %v", hour, err)
		}
	}
	body = h.do("GET", "/resurs/ellastcykel", nil, member).Body.String()
	dayAfter := h.date(2)
	if !strings.Contains(body, "Lediga starttider") {
		t.Fatal("the page should still render a slot list")
	}
	if !strings.Contains(body, dayAfter) {
		t.Errorf("expected the page to skip to %s, which is the first free day", dayAfter)
	}
	if strings.Contains(body, "Inget ledigt den här dagen") {
		t.Error("the page landed on a day with nothing free")
	}
}

func storeBooking(h *harness, day time.Time, hour, length int) store.Booking {
	start := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, h.loc)
	return store.Booking{
		ID:         start.Format("150405.000") + "-" + day.Format("0102"),
		ResourceID: "ellastcykel",
		Start:      start,
		End:        start.Add(time.Duration(length) * time.Hour),
		Mode:       "hours",
		Name:       "Någon Annan",
		MMUsername: "nagon.annan",
		Status:     store.StatusConfirmed,
		CreatedAt:  h.now,
	}
}

// --- Typed-in booking lengths ----------------------------------------------

func TestCustomDurationOfferedOnlyWhereAllowed(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	with := h.do("GET", "/resurs/ellastcykel", nil, member).Body.String()
	if !strings.Contains(with, "Egen längd") {
		t.Error("the bike allows custom lengths, so the button should be there")
	}

	without := h.do("GET", "/resurs/elcykel", nil, member).Body.String()
	if strings.Contains(without, "Egen längd") {
		t.Error("elcykel has custom lengths off; the button must not appear")
	}
}

// The field is behind the "egen längd" button, the same way the fixed lengths
// are behind their buttons.
func TestCustomDurationFieldIsHiddenUntilChosen(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	day := h.date(1)

	closed := h.do("GET", "/resurs/ellastcykel?datum="+day, nil, member).Body.String()
	if !strings.Contains(closed, "Egen längd") {
		t.Fatal("the button should be in the row")
	}
	if strings.Contains(closed, `id="egen-langd"`) || strings.Contains(closed, `name="langd" type="text"`) {
		t.Error("the field must stay hidden until the button is clicked")
	}
	if !strings.Contains(closed, `aria-expanded="false"`) {
		t.Error("the closed button should report aria-expanded=false")
	}

	opened := h.do("GET", "/resurs/ellastcykel?datum="+day+"&egen=1", nil, member).Body.String()
	if !strings.Contains(opened, `id="egen-langd"`) {
		t.Error("clicking the button should reveal the field")
	}
	if !strings.Contains(opened, `aria-expanded="true"`) {
		t.Error("the open button should report aria-expanded=true")
	}
	if !strings.Contains(opened, `pill pill-custom is-selected`) {
		t.Error("the button should look selected once chosen")
	}
	// The field starts from the length already on screen, not empty.
	if !strings.Contains(opened, `value="1"`) {
		t.Error("the field should be seeded with the current length")
	}
}

// Picking a fixed length again closes the field.
func TestChoosingAPresetClosesTheCustomField(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	day := h.date(1)

	body := h.do("GET", "/resurs/ellastcykel?datum="+day+"&langd=2", nil, member).Body.String()
	if strings.Contains(body, `id="egen-langd"`) {
		t.Error("a preset length should leave the custom field closed")
	}
	if !strings.Contains(body, `class="pill is-selected`) {
		t.Error("the chosen preset should look selected")
	}
}

// A typed length that breaks a rule keeps the field open with the complaint.
func TestBadTypedLengthKeepsTheFieldOpen(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1)+"&egen=1&langd=99", nil, member).Body.String()
	if !strings.Contains(body, `id="egen-langd"`) {
		t.Error("the field should stay open so the member can correct it")
	}
	if !strings.Contains(body, "Längsta") {
		t.Error("the complaint should be shown")
	}
	if !strings.Contains(body, `value="99"`) {
		t.Error("what they typed should stay in the field")
	}
}

func TestResourcePageAcceptsATypedLength(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	day := h.date(1)

	body := h.do("GET", "/resurs/ellastcykel?datum="+day+"&langd=3,5", nil, member).Body.String()
	if !strings.Contains(body, "3 h 30 min") {
		t.Error("the slot list should be built for the typed 3.5 hour length")
	}
	// The value stays in the field, in Swedish form...
	if !strings.Contains(body, `value="3,5"`) {
		t.Error("the typed value should stay in the field")
	}
	// ...the "egen längd" button is the active one...
	if !strings.Contains(body, `pill pill-custom is-selected`) {
		t.Error("the custom length button should look selected")
	}
	// ...and no preset button is.
	if strings.Contains(body, `class="pill is-selected`) {
		t.Error("no preset button should look selected for a custom length")
	}
}

func TestTypedLengthOutsideTheRulesExplainsItself(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	day := h.date(1)

	cases := map[string]string{
		"0,25": "Kortaste", // under min_duration_minutes
		"12":   "Längsta",  // over max_duration_minutes
		"1,1":  "jämnt ut", // off the 30 minute grid
		"abc":  "inte ett tal",
	}
	for value, want := range cases {
		body := h.do("GET", "/resurs/ellastcykel?datum="+day+"&langd="+url.QueryEscape(value), nil, member).Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("langd=%q should explain %q", value, want)
		}
		// A bad length must not take the page down or lose the slot list.
		if !strings.Contains(body, "Lediga starttider") {
			t.Errorf("langd=%q broke the page", value)
		}
	}
}

func TestBookingWithATypedLength(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"3.5"},
		"name": {"Anna Andersson"}, "medlem": {"anna.andersson"},
	}
	rec := h.do("POST", "/resurs/ellastcykel/boka", form, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d, want a redirect. Body:\n%s", rec.Code, rec.Body.String())
	}
	confirm := h.do("GET", rec.Header().Get("Location"), nil, member).Body.String()
	if !strings.Contains(confirm, "3 h 30 min") {
		t.Error("the confirmation should show a three and a half hour booking")
	}
	if !strings.Contains(confirm, "10:00") || !strings.Contains(confirm, "13:30") {
		t.Error("the confirmation should run 10:00 to 13:30")
	}
}

func TestTypedLengthIsRefusedWhereNotAllowed(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"3"},
		"name": {"Anna"}, "medlem": {"anna.andersson"},
	}
	rec := h.do("POST", "/resurs/elcykel/boka", form, member)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tillåtna längderna") {
		t.Error("the page should list the allowed lengths")
	}
}

// --- Upcoming bookings page -------------------------------------------------

func TestUpcomingListsBookingsInOrderFromNow(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	// Two bookings tomorrow, one the day after, and one that has already
	// finished. Created out of order on purpose.
	seed := []struct {
		day, start string
		hours      string
		who        string
	}{
		{h.date(2), "09:00", "2", "Cecilia Dahl"},
		{h.date(1), "16:00", "1", "Bo Bengtsson"},
		{h.date(1), "08:00", "2", "Anna Andersson"},
	}
	for _, b := range seed {
		form := url.Values{
			"datum": {b.day}, "start": {b.start}, "langd": {b.hours},
			"name": {b.who}, "medlem": {directoryUsername(b.who)},
		}
		if rec := h.do("POST", "/resurs/ellastcykel/boka", form, member); rec.Code != http.StatusSeeOther {
			t.Fatalf("seed %s: %d\n%s", b.who, rec.Code, rec.Body.String())
		}
	}
	// A booking that finished before "now".
	past := storeBooking(h, h.now.AddDate(0, 0, -2), 10, 2)
	past.Name = "Gammal Bokning"
	if err := h.store.Create(context.Background(), past, past.Start, past.End); err != nil {
		t.Fatalf("seed past: %v", err)
	}

	body := h.do("GET", "/resurs/ellastcykel/bokningar", nil, member).Body.String()

	if strings.Contains(body, "Gammal Bokning") {
		t.Error("bookings that already finished must not be listed")
	}
	for _, who := range []string{"Anna Andersson", "Bo Bengtsson", "Cecilia Dahl"} {
		if !strings.Contains(body, who) {
			t.Errorf("%s is missing from the upcoming list", who)
		}
	}

	// Chronological: Anna 08:00, then Bo 16:00, then Cecilia the next day.
	iAnna := strings.Index(body, "Anna Andersson")
	iBo := strings.Index(body, "Bo Bengtsson")
	iCecilia := strings.Index(body, "Cecilia Dahl")
	if !(iAnna < iBo && iBo < iCecilia) {
		t.Errorf("out of order: Anna at %d, Bo at %d, Cecilia at %d", iAnna, iBo, iCecilia)
	}

	if !strings.Contains(body, "3 bokningar") {
		t.Error("the heading should count the upcoming bookings")
	}
}

func TestUpcomingGroupsByDayAndShowsGuestRoomNights(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	form := url.Values{
		"fran": {h.date(5)}, "till": {h.date(8)},
		"name": {"Anna Andersson"}, "medlem": {"anna.andersson"},
	}
	if rec := h.do("POST", "/resurs/gastrum-1/boka", form, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed: %d", rec.Code)
	}

	body := h.do("GET", "/resurs/gastrum-1/bokningar", nil, member).Body.String()
	if !strings.Contains(body, "3 nätter") {
		t.Error("a guest room stay should be shown in nights")
	}
	if !strings.Contains(body, "agenda-day") {
		t.Error("bookings should be grouped by day")
	}
}

func TestUpcomingIsEmptyAndFriendlyWithNoBookings(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/resurs/ellastcykel/bokningar", nil, member).Body.String()
	if !strings.Contains(body, "Inget är bokat än") {
		t.Error("an empty resource should say so plainly")
	}
}

func TestUpcomingNeedsALoginAndARealResource(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/resurs/ellastcykel/bokningar", nil, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("logged out = %d, want a redirect to /login", rec.Code)
	}
	member := h.login("husets-losenord")
	if rec := h.do("GET", "/resurs/finns-inte/bokningar", nil, member); rec.Code != http.StatusNotFound {
		t.Errorf("unknown resource = %d, want 404", rec.Code)
	}
}

func TestResourcePageLinksToUpcoming(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	for _, path := range []string{"/", "/resurs/ellastcykel", "/resurs/gastrum-1"} {
		body := h.do("GET", path, nil, member).Body.String()
		if !strings.Contains(body, "/resurs/ellastcykel/bokningar") &&
			!strings.Contains(body, "/resurs/gastrum-1/bokningar") {
			t.Errorf("%s should link to the upcoming bookings page", path)
		}
	}
}

// The live-update script swaps these two regions. If a template change drops
// an id, typing would quietly stop updating the times.
func TestCustomPanelHasTheHooksTheScriptNeeds(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1)+"&egen=1", nil, member).Body.String()
	for _, hook := range []string{
		`data-live-duration`,   // the form the script binds to
		`id="slot-area"`,       // replaced when a new length is accepted
		`id="custom-feedback"`, // replaced always, and carries any complaint
		`name="langd"`,
		`name="datum"`,
		`name="egen"`,
	} {
		if !strings.Contains(body, hook) {
			t.Errorf("the custom length panel is missing %s, which the live update needs", hook)
		}
	}

	// The complaint must land inside the feedback box, since the script uses
	// its presence there to decide whether to keep the times on screen.
	bad := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1)+"&egen=1&langd=99", nil, member).Body.String()
	start := strings.Index(bad, `id="custom-feedback"`)
	if start < 0 {
		t.Fatal("no feedback box")
	}
	end := strings.Index(bad[start:], "</div>")
	if end < 0 || !strings.Contains(bad[start:start+end], "alert") {
		t.Error("a rejected length should put its alert inside #custom-feedback")
	}
}

// Without JavaScript the form still has to work on its own.
func TestCustomLengthWorksAsAPlainFormSubmit(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	// Exactly what the browser sends when the form is submitted normally.
	body := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1)+"&egen=1&langd=2%2C5", nil, member).Body.String()
	if !strings.Contains(body, "2 h 30 min") {
		t.Error("a plain form submit should produce the 2.5 hour slot list")
	}
	if !strings.Contains(body, `class="button primary small"`) {
		t.Error("the submit button must be in the HTML for people without JavaScript")
	}
}

// A category can link to the Mattermost channel where the house talks about
// that kind of thing, and a resource can link to its page on rudbeckia.nu.
// Both are config that used to be declared and never rendered.
func TestCategoryAndResourceLinksAreRendered(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	start := h.do("GET", "/", nil, member).Body.String()
	if !strings.Contains(start, `href="https://chat.example.com/channels/cykelpoolen"`) {
		t.Error("the category link is missing from the start page")
	}
	if !strings.Contains(start, "#cykelpoolen") {
		t.Error("the category link should use its configured text")
	}

	page := h.do("GET", "/resurs/ellastcykel", nil, member).Body.String()
	if !strings.Contains(page, `href="https://example.com/huset/cykelrummet/"`) {
		t.Error("info_url is not rendered on the resource page")
	}

	// A category without a link must not render an empty anchor.
	if strings.Contains(start, `href=""`) {
		t.Error("a category with no link produced an empty href")
	}
}

// A release must reach the browsers. The static files are cached hard, so
// their addresses carry a hash of what they contain.
func TestStaticFilesAreFingerprintedAndCachedHard(t *testing.T) {
	h := newHarness(t)
	page := h.do("GET", "/login", nil, nil).Body.String()

	for _, name := range []string{"app.css", "app.js", "members.js"} {
		sum, ok := h.server.assets[name]
		if !ok || len(sum) != 10 {
			t.Fatalf("%s has no content hash: %q", name, sum)
		}
		want := "/static/" + name + "?v=" + sum
		if !strings.Contains(page, want) {
			t.Errorf("the page does not ask for %s", want)
		}

		// The hashed address may be kept forever; a bare one may not.
		hashed := h.do("GET", want, nil, nil)
		if hashed.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", want, hashed.Code)
		}
		if cc := hashed.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s cache-control = %q, want immutable", want, cc)
		}
		bare := h.do("GET", "/static/"+name, nil, nil)
		if cc := bare.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=300") {
			t.Errorf("an unstamped %s may only be cached briefly, got %q", name, cc)
		}
	}
}

// The browser-side tests live next to the file they test, but they are not
// part of the site.
func TestTestFilesAreNotShipped(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/static/members_test.mjs", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /static/members_test.mjs = %d, want 404", rec.Code)
	}
	if _, ok := h.server.assets["members_test.mjs"]; ok {
		t.Error("the test file was embedded in the binary")
	}
	// The real assets are still there.
	for _, name := range []string{"app.css", "app.js", "members.js", "rudbeckia.png"} {
		if _, ok := h.server.assets[name]; !ok {
			t.Errorf("%s is missing from the binary", name)
		}
	}
}

// The hash has to follow the content, or it is worse than no hash at all.
func TestAssetHashesDifferPerFile(t *testing.T) {
	h := newHarness(t)
	if h.server.assets["app.js"] == h.server.assets["members.js"] {
		t.Error("two different files got the same hash")
	}
	if got := h.server.asset("finns-inte.js"); got != "/static/finns-inte.js" {
		t.Errorf("an unknown file should be left alone, got %q", got)
	}
}

// --- The quick "book right away" button -------------------------------------

// seed puts a confirmed booking straight into the database, so a test can say
// "this thing is already out" without going through the form.
func (h *harness) seed(resource string, start time.Time, length time.Duration) {
	h.Helper()
	b := store.Booking{
		ID:         resource + "-" + start.Format("0102-1504"),
		ResourceID: resource,
		Start:      start,
		End:        start.Add(length),
		Mode:       "hours",
		Name:       "Någon Annan",
		MMUsername: "nagon.annan",
		Status:     store.StatusConfirmed,
		CreatedAt:  h.now,
	}
	if err := h.store.Create(context.Background(), b, b.Start, b.End); err != nil {
		h.Fatalf("seed %s at %s: %v", resource, start, err)
	}
}

func (h *harness) at(day int, hour, minute int) time.Time {
	d := h.now.AddDate(0, 0, day)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, h.loc)
}

// The button is the whole point of the feature: one click from the card.
func TestTheCardOffersTheQuickButtonWhenSomethingIsFree(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	body := h.do("GET", "/", nil, member).Body.String()
	if !strings.Contains(body, `href="/resurs/ellastcykel/nu"`) {
		t.Error("the bike is free, so its card should offer the quick button")
	}
	if !strings.Contains(body, "Boka direkt") {
		t.Error("the quick button should say what it does")
	}
}

// Free right now: straight to the confirmation form for that time, with the
// day, the length and the start already chosen.
func TestQuickBookGoesStraightThroughWhenTheTimeIsNow(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("GET", "/resurs/ellastcykel/nu", nil, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	// 08:00 is the harness's own clock, so the soonest start is right now.
	want := "/resurs/ellastcykel?datum=" + h.date(0) + "&langd=1&start=08:00#boka"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	// And that address really does open the form on that time. The fragment
	// is the browser's business and never reaches the server, so it goes.
	body := h.do("GET", strings.TrimSuffix(want, "#boka"), nil, member).Body.String()
	if !strings.Contains(body, "Bekräfta bokningen") {
		t.Error("the confirmation form should be open")
	}
	if !strings.Contains(body, `name="start" value="08:00"`) {
		t.Error("the form should have the start time filled in")
	}
}

// Not free until later: stop, say so, and offer the time instead of taking it.
func TestQuickBookHaltsAndSuggestsWhenTheTimeIsNotNow(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	// The bike is out 08:00–10:00. With the 15 minute buffer the next free
	// hour starts at 10:30.
	h.seed("ellastcykel", h.at(0, 8, 0), 2*time.Hour)

	rec := h.do("GET", "/resurs/ellastcykel/nu", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the member has to be asked first", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Det går inte att boka direkt") {
		t.Error("the page should say why it stopped")
	}
	if !strings.Contains(body, "Närmast lediga tid är idag 10:30") {
		t.Errorf("the page should suggest the soonest free time:\n%s", body)
	}
	if !strings.Contains(body, "start=10:30") {
		t.Error("the suggestion should be a link that books that time")
	}
	// Stopping means stopping: nothing is booked and no form is open yet.
	if strings.Contains(body, "Bekräfta bokningen") {
		t.Error("the confirmation form must not be open — the halt would be pointless")
	}
	list, err := h.store.InRange(context.Background(), "ellastcykel", h.now, h.now.AddDate(0, 0, 2))
	if err != nil {
		t.Fatalf("read bookings: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("the halt booked something: %d bookings, want the seeded 1", len(list))
	}
}

// A halt has to land on the day it is talking about, or the suggestion is the
// only thing on the page that knows about it.
func TestTheHaltShowsTheDayItSuggests(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	// Today is out altogether; the next free hour is 06:00 tomorrow.
	h.seed("ellastcykel", h.at(0, 6, 0), 16*time.Hour)

	body := h.do("GET", "/resurs/ellastcykel/nu", nil, member).Body.String()
	if !strings.Contains(body, "Närmast lediga tid är imorgon 06:00") {
		t.Errorf("the suggestion should reach into tomorrow:\n%s", body)
	}
	if !strings.Contains(body, "Lediga starttider imorgon") {
		t.Error("the slot list should be on tomorrow, the day being suggested")
	}
}

// A guest room is had by the night, so tonight is as immediate as it gets.
func TestQuickBookOfARoomTakesTonightAndOffersTheNextNight(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("GET", "/resurs/gastrum-1/nu", nil, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	want := "/resurs/gastrum-1?manad=" + h.now.Format("2006-01") +
		"&fran=" + h.date(0) + "&till=" + h.date(1) + "#boka"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	form := h.do("GET", strings.TrimSuffix(want, "#boka"), nil, member).Body.String()
	if !strings.Contains(form, `action="/resurs/gastrum-1/boka"`) {
		t.Error("the form should be open on tonight")
	}
	if !strings.Contains(form, `name="fran" value="`+h.date(0)+`"`) {
		t.Error("the form should have tonight's check-in filled in")
	}

	// With tonight taken, the soonest is tomorrow — and that is a decision the
	// member gets to make, not one the button makes.
	h.seed("gastrum-1", h.at(0, 15, 0), 21*time.Hour)
	rec = h.do("GET", "/resurs/gastrum-1/nu", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Närmast lediga tid är imorgon.") {
		t.Errorf("a room is booked by the night, so no clock time:\n%s", body)
	}
	if !strings.Contains(body, "fran="+h.date(1)) {
		t.Error("the suggestion should be a link that books tomorrow night")
	}
	if strings.Contains(body, `action="/resurs/gastrum-1/boka"`) {
		t.Error("the form must not be open — the halt would be pointless")
	}
}

// Nothing free anywhere ahead: there is no time to offer, so say that rather
// than suggesting a blank.
func TestQuickBookSaysSoWhenThereIsNothingToOffer(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	// elcykel has no buffer and opens 06:00–22:00. Fill every day the scan
	// reaches, and a couple past it for good measure.
	for d := 0; d <= 16; d++ {
		h.seed("elcykel", h.at(d, 6, 0), 16*time.Hour)
	}

	rec := h.do("GET", "/resurs/elcykel/nu", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Det går inte att boka direkt") {
		t.Error("the page should still say it stopped")
	}
	if !strings.Contains(body, "Ingenting är ledigt inom den närmaste tiden") {
		t.Errorf("with nothing free, the page should say so:\n%s", body)
	}
	if strings.Contains(body, "Närmast lediga tid är") {
		t.Error("there is no time to suggest, so it must not pretend there is")
	}

	// The card agrees, and does not offer a button that could only disappoint.
	index := h.do("GET", "/", nil, member).Body.String()
	if strings.Contains(index, `href="/resurs/elcykel/nu"`) {
		t.Error("a card with nothing free should not offer the quick button")
	}
}

// On a coarse grid the next slot can start soon in clock terms and still be
// somebody else's turn first. Skipping over a booked slot is a wait however
// few minutes it lasts, so the button asks.
func TestQuickBookCountsASkippedSlotAsAWaitHoweverShort(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	// The laundry runs in two hour blocks from 06:00, so at 08:00 the member's
	// turn is now — until somebody takes it.
	if rec := h.do("GET", "/resurs/tvattstugan/nu", nil, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("with 08:00 free the button should book it, got %d", rec.Code)
	}
	h.seed("tvattstugan", h.at(0, 8, 0), 2*time.Hour)

	// 10:00 is only two hours out — one step — but it is not this member's
	// turn any more, and the old rule waved that through.
	rec := h.do("GET", "/resurs/tvattstugan/nu", nil, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — 10:00 is a wait, not now", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Närmast lediga tid är idag 10:00") {
		t.Errorf("the page should offer 10:00 rather than booking it:\n%s", body)
	}
}

// The quick button is behind the password like everything else, and it does
// not invent resources.
func TestQuickBookIsGuardedLikeTheRestOfTheSite(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/resurs/ellastcykel/nu", nil, nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("without a session = %d to %q, want a redirect to /login", rec.Code, rec.Header().Get("Location"))
	}
	member := h.login("husets-losenord")
	for _, path := range []string{"/resurs/finns-inte/nu", "/resurs/avstangd/nu"} {
		if rec := h.do("GET", path, nil, member); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}
