package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseAcceptsWhatIsStored(t *testing.T) {
	cases := map[string]struct {
		want Lang
		ok   bool
	}{
		"sv": {SV, true}, "en": {EN, true}, "  EN  ": {EN, true},
		"": {Default, false}, "de": {Default, false}, "svenska": {Default, false},
	}
	for in, want := range cases {
		got, ok := Parse(in)
		if got != want.want || ok != want.ok {
			t.Errorf("Parse(%q) = %q, %v; want %q, %v", in, got, ok, want.want, want.ok)
		}
	}
}

func TestTheOtherLanguageIsTheOneToSwitchTo(t *testing.T) {
	if SV.Other() != EN || EN.Other() != SV {
		t.Error("the switch has to move between the two languages")
	}
	if SV.Name() != "Svenska" || EN.Name() != "English" {
		t.Error("a language names itself in its own words")
	}
}

// A missing phrase must be loud on the page rather than an empty gap, so that
// it gets noticed and fixed.
func TestAMissingPhraseShowsItsKey(t *testing.T) {
	if got := T(SV, "no.such.phrase"); got != "⟦no.such.phrase⟧" {
		t.Errorf("T of an unknown key = %q", got)
	}
}

func TestArgumentsAreSubstituted(t *testing.T) {
	if got := T(SV, "error.slotgrid", 30); got != "Starttiden måste ligga på en 30-minutersgräns." {
		t.Errorf("= %q", got)
	}
	if got := T(EN, "error.slotgrid", 30); got != "A booking has to start on a 30-minute mark." {
		t.Errorf("= %q", got)
	}
}

func TestRequestLanguagePrefersTheReadersOwnChoice(t *testing.T) {
	req := func(header, cookie string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("Accept-Language", header)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: Cookie, Value: cookie})
		}
		return r
	}
	cases := []struct {
		header, cookie string
		fallback, want Lang
	}{
		{"", "", SV, SV},
		{"", "", EN, EN},                 // the deployment's own
		{"en-GB,en;q=0.9", "", SV, EN},   // the browser asks
		{"de-DE,de;q=0.9", "", SV, SV},   // a language we do not have
		{"de,en;q=0.8", "", SV, EN},      // the best one we do have
		{"en-GB,en;q=0.9", "sv", SV, SV}, // the reader has chosen
		{"sv", "en", SV, EN},             // and their choice wins
		{"", "klingon", SV, SV},          // a nonsense cookie
	}
	for _, c := range cases {
		if got := FromRequest(req(c.header, c.cookie), c.fallback); got != c.want {
			t.Errorf("Accept-Language %q, cookie %q, fallback %q = %q; want %q",
				c.header, c.cookie, c.fallback, got, c.want)
		}
	}
}

func TestCookieIsSetForAYear(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookie(rec, EN, true)
	c := rec.Result().Cookies()[0]
	if c.Name != Cookie || c.Value != "en" {
		t.Errorf("cookie = %+v", c)
	}
	if !c.Secure || !c.HttpOnly || c.MaxAge < 300*24*3600 {
		t.Errorf("cookie should be secure, http-only and long-lived: %+v", c)
	}
}

func TestDatesReadTheWayEachLanguageWritesThem(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	d := time.Date(2026, 8, 18, 13, 5, 0, 0, loc)

	cases := []struct{ sv, en string }{
		{Weekday(SV, d), "tisdag"}, {Weekday(EN, d), "Tuesday"},
		{Month(SV, d), "augusti"}, {Month(EN, d), "August"},
		{DateLong(SV, d), "tisdag 18 augusti"}, {DateLong(EN, d), "Tuesday 18 August"},
		{DateShort(SV, d), "18 aug"}, {DateShort(EN, d), "18 Aug"},
		{MonthYear(SV, d), "augusti 2026"}, {MonthYear(EN, d), "August 2026"},
		{Clock(d), "13:05"}, {ISODate(d), "2026-08-18"},
	}
	for _, c := range cases {
		if c.sv != c.en {
			t.Errorf("got %q, want %q", c.sv, c.en)
		}
	}
}

// An interval inside one day says the day once; one that spans days says both.
func TestIntervalIsAsShortAsItCanBe(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	start := time.Date(2026, 8, 18, 13, 0, 0, 0, loc)

	if got, want := Interval(SV, start, start.Add(2*time.Hour)), "tisdag 18 augusti 13:00–15:00"; got != want {
		t.Errorf("= %q, want %q", got, want)
	}
	if got, want := Interval(EN, start, start.Add(2*time.Hour)), "Tuesday 18 August 13:00–15:00"; got != want {
		t.Errorf("= %q, want %q", got, want)
	}
	across := time.Date(2026, 8, 21, 12, 0, 0, 0, loc)
	if got, want := Interval(SV, start, across), "18 aug 13:00 – 21 aug 12:00"; got != want {
		t.Errorf("= %q, want %q", got, want)
	}
}

func TestRelativeDayTalksLikeANeighbour(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, loc)
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }

	cases := []struct {
		lang Lang
		days int
		want string
	}{
		{SV, 0, "idag"}, {EN, 0, "today"},
		{SV, 1, "imorgon"}, {EN, 1, "tomorrow"},
		{SV, 2, "i övermorgon"}, {EN, 2, "the day after tomorrow"},
		{SV, 3, "fredag"}, {EN, 3, "Friday"},
	}
	for _, c := range cases {
		if got := RelativeDay(c.lang, day(c.days), now); got != c.want {
			t.Errorf("%s in %d days = %q, want %q", c.lang, c.days, got, c.want)
		}
	}
}

func TestCountsAgreeWithTheirNoun(t *testing.T) {
	cases := []struct {
		lang Lang
		unit string
		n    int
		want string
	}{
		{SV, "night", 1, "1 natt"}, {SV, "night", 3, "3 nätter"},
		{EN, "night", 1, "1 night"}, {EN, "night", 3, "3 nights"},
		{SV, "booking", 1, "1 bokning"}, {EN, "booking", 2, "2 bookings"},
	}
	for _, c := range cases {
		if got := Count(c.lang, c.unit, c.n); got != c.want {
			t.Errorf("Count(%s, %s, %d) = %q, want %q", c.lang, c.unit, c.n, got, c.want)
		}
	}
}

// A booking horizon reads in the largest unit that comes out even: 90 days is
// three months to a member, not ninety days.
func TestDaysReadsAsMonthsAndYearsWhenItCan(t *testing.T) {
	cases := []struct {
		days int
		sv   string
		en   string
	}{
		{1, "1 dag", "1 day"},
		{45, "45 dagar", "45 days"},
		{30, "1 månad", "1 month"},
		{90, "3 månader", "3 months"},
		{365, "1 år", "1 year"},
		{730, "2 år", "2 years"},
	}
	for _, c := range cases {
		if got := Days(SV, c.days); got != c.sv {
			t.Errorf("Days(sv, %d) = %q, want %q", c.days, got, c.sv)
		}
		if got := Days(EN, c.days); got != c.en {
			t.Errorf("Days(en, %d) = %q, want %q", c.days, got, c.en)
		}
	}
}

func TestDurationsReadTheWayTheHouseTalks(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Minute: "30 min",
		time.Hour:        "1 h",
		4 * time.Hour:    "4 h",
		90 * time.Minute: "1 h 30 min",
	}
	for in, want := range cases {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
	if got, want := DurationList(SV, []float64{1, 2, 4, 8}), "1 h, 2 h, 4 h eller 8 h"; got != want {
		t.Errorf("DurationList = %q, want %q", got, want)
	}
	if got, want := DurationList(EN, []float64{1, 2, 4, 8}), "1 h, 2 h, 4 h or 8 h"; got != want {
		t.Errorf("DurationList = %q, want %q", got, want)
	}
	if got, want := DurationList(SV, []float64{2}), "2 h"; got != want {
		t.Errorf("a single length needs no joining word, got %q", got)
	}
}

// Swedish weekdays are lower case, so a sentence that starts with one has to
// be lifted. English ones are already capitalised and must not change.
func TestTitleCaseLiftsTheFirstLetterOnly(t *testing.T) {
	if got := TitleCase("tisdag 18 augusti"); got != "Tisdag 18 augusti" {
		t.Errorf("= %q", got)
	}
	if got := TitleCase("Tuesday 18 August"); got != "Tuesday 18 August" {
		t.Errorf("= %q", got)
	}
	if got := TitleCase("över"); got != "Över" {
		t.Errorf("a multi-byte first letter should still lift: %q", got)
	}
	if TitleCase("") != "" {
		t.Error("an empty string has no first letter")
	}
}

func TestWeekdayInitialsStartOnMonday(t *testing.T) {
	sv := WeekdayInitials(SV)
	if len(sv) != 7 || sv[0] != "mån" || sv[6] != "sön" {
		t.Errorf("the calendar's columns are %v, want Monday first", sv)
	}
	en := WeekdayInitials(EN)
	if en[0] != "Mon" || en[6] != "Sun" {
		t.Errorf("= %v", en)
	}
}
