package web

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
)

// A phrase the templates ask for but the catalogue does not have renders as
// ⟦key⟧ on the page. That is loud, but it should never get as far as a person:
// these tests read the templates and the catalogue and fail the build instead.

var (
	// {{t "some.key"}} and {{t "some.key" arg}}, including inside an argument
	// list such as (t "x") or dict "Zero" (t "y").
	tCall = regexp.MustCompile(`\bt\s+"([a-z0-9._]+)"`)
	// count "night" 3 / plural "booking" 1 — the unit names catalogue rows too.
	unitCall = regexp.MustCompile(`\b(?:count|plural|nights)\s+"([a-z0-9._]+)"`)
)

func templateSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no templates found")
	}
	return out
}

func TestEveryPhraseTheTemplatesAskForExists(t *testing.T) {
	var missing []string
	for path, src := range templateSources(t) {
		for _, m := range tCall.FindAllStringSubmatch(src, -1) {
			if !i18n.Has(m[1]) {
				missing = append(missing, path+": "+m[1])
			}
		}
		for _, m := range unitCall.FindAllStringSubmatch(src, -1) {
			for _, suffix := range []string{".one", ".many"} {
				key := "unit." + m[1] + suffix
				if !i18n.Has(key) {
					missing = append(missing, path+": "+key)
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("phrase is missing from the catalogue — %s", m)
	}
}

// The same for the phrases the Go code asks for. A validation message only
// appears when something goes wrong, which is the worst moment to find out
// that its key was mistyped.
func TestEveryPhraseTheCodeAsksForExists(t *testing.T) {
	call := regexp.MustCompile(`i18n\.T\(\s*[a-zA-Z0-9_.]+,\s*"([a-z0-9._]+)"`)
	unit := regexp.MustCompile(`i18n\.(?:Count|Plural)\(\s*[a-zA-Z0-9_.]+,\s*"([a-z0-9._]+)"`)
	// The tests run in the package directory, so ".." is internal/: every
	// package that says anything to a member is under it.
	sources := os.DirFS("..")
	var missing []string
	err := fs.WalkDir(sources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := fs.ReadFile(sources, path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			if !i18n.Has(m[1]) {
				missing = append(missing, path+": "+m[1])
			}
		}
		for _, m := range unit.FindAllStringSubmatch(string(b), -1) {
			for _, suffix := range []string{".one", ".many"} {
				if key := "unit." + m[1] + suffix; !i18n.Has(key) {
					missing = append(missing, path+": "+key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("phrase is missing from the catalogue — %s", m)
	}
}

// Both columns of the catalogue have to be filled in. A blank translation
// would render as nothing at all, which is worse than the wrong language.
func TestEveryPhraseHasBothLanguages(t *testing.T) {
	for _, key := range i18n.Keys() {
		sv, en, ok := i18n.Entry(key)
		if !ok {
			t.Fatalf("%s vanished between Keys and Entry", key)
		}
		if strings.TrimSpace(sv) == "" {
			t.Errorf("%s has no Swedish", key)
		}
		if strings.TrimSpace(en) == "" {
			t.Errorf("%s has no English", key)
		}
	}
}

// A phrase with a %s in one language and none in the other would render with a
// stray "%!s(MISSING)" or silently drop its argument.
func TestPlaceholdersMatchBetweenLanguages(t *testing.T) {
	verbs := regexp.MustCompile(`%[a-zA-Z]|%\.[0-9][a-zA-Z]`)
	for _, key := range i18n.Keys() {
		sv, en, _ := i18n.Entry(key)
		gotSV, gotEN := verbs.FindAllString(sv, -1), verbs.FindAllString(en, -1)
		if len(gotSV) != len(gotEN) {
			t.Errorf("%s takes %v in Swedish but %v in English", key, gotSV, gotEN)
			continue
		}
		// The order matters too: Sprintf fills them positionally.
		for i := range gotSV {
			if gotSV[i] != gotEN[i] {
				t.Errorf("%s: placeholder %d is %s in Swedish and %s in English",
					key, i+1, gotSV[i], gotEN[i])
			}
		}
	}
}

// Nothing in the templates should still be a Swedish sentence sitting outside
// the catalogue. This looks for the letters only Swedish uses, which is a crude
// but effective net for prose that was never keyed.
func TestNoUntranslatedSwedishLeftInTheTemplates(t *testing.T) {
	for path, src := range templateSources(t) {
		for i, line := range strings.Split(src, "\n") {
			// Comments explain the template to whoever maintains it and are
			// never rendered, so they are allowed to say anything.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "{{/*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// A catalogue lookup naturally contains the key, not prose.
			stripped := tCall.ReplaceAllString(line, "")
			if strings.ContainsAny(stripped, "åäöÅÄÖ") {
				t.Errorf("%s:%d looks like untranslated Swedish: %s", path, i+1, trimmed)
			}
		}
	}
}

// ---------------------------------------------------------- choosing one ---

func TestTheSwitchRemembersTheLanguageAndComesBack(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	if body := h.do("GET", "/", nil, member).Body.String(); !strings.Contains(body, "Boka husets gemensamma") {
		t.Fatal("the site should start out in Swedish")
	}

	rec := h.do("POST", "/sprak", url.Values{"lang": {"en"}, "next": {"/mina"}}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("switching = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/mina" {
		t.Errorf("came back to %q, want /mina", got)
	}
	var lang *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == i18n.Cookie {
			lang = c
		}
	}
	if lang == nil || lang.Value != "en" {
		t.Fatalf("language cookie = %+v, want en", lang)
	}

	english := append(member, lang)
	body := h.do("GET", "/", nil, english).Body.String()
	if !strings.Contains(body, "shared things") {
		t.Error("the page should now be in English")
	}
	if strings.Contains(body, "Boka husets gemensamma") {
		t.Error("the page is still partly Swedish")
	}
	// And the switch now offers the way back.
	if !strings.Contains(body, `value="sv"`) {
		t.Error("the switch should offer Swedish again")
	}
	if !strings.Contains(body, `<html lang="en"`) {
		t.Error("the page should tell the browser which language it is in")
	}
}

func TestTheSwitchRefusesALanguageWeDoNotHave(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/sprak", url.Values{"lang": {"de"}, "next": {"/"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("= %d, want 400", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == i18n.Cookie {
			t.Error("no language cookie should have been set")
		}
	}
}

// A redirect target out of a form is not to be trusted here either.
func TestTheSwitchOnlyComesBackToThisSite(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/sprak", url.Values{"lang": {"en"}, "next": {"https://evil.example.com"}}, nil)
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

// Before anybody chooses, the browser's own preference is a better guess than
// the default.
func TestAcceptLanguageIsUsedUntilSomebodyChooses(t *testing.T) {
	h := newHarness(t)

	page := func(header string, cookies ...*http.Cookie) string {
		req := httptest.NewRequest("GET", "/login", nil)
		if header != "" {
			req.Header.Set("Accept-Language", header)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if !strings.Contains(page("en-GB,en;q=0.9"), "The house password") {
		t.Error("an English browser should get the English login page")
	}
	// A language we do not have falls through to the deployment's own.
	if !strings.Contains(page("de-DE,de;q=0.9"), "Husets lösenord") {
		t.Error("a German browser should get the configured default, Swedish")
	}
	// A browser that would rather have English than German gets English.
	if !strings.Contains(page("de-DE,de;q=0.9,en;q=0.8"), "The house password") {
		t.Error("the highest-weighted language we have should win")
	}
	// An explicit choice beats the browser.
	if !strings.Contains(page("en-GB,en;q=0.9", &http.Cookie{Name: i18n.Cookie, Value: "sv"}), "Husets lösenord") {
		t.Error("the cookie should win over Accept-Language")
	}
}

// The whole page has to switch, dates included — a Swedish weekday inside an
// English sentence is the tell-tale of a half-done translation.
func TestDatesAndRulesFollowTheLanguage(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")
	english := append(h.login("husets-losenord"), &http.Cookie{Name: i18n.Cookie, Value: "en"})

	sv := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1), nil, member).Body.String()
	if !strings.Contains(sv, "tisdag 5 maj") {
		t.Error("the Swedish page should date the day in Swedish")
	}
	if !strings.Contains(sv, "1 månad") {
		t.Error("the Swedish page should say how far ahead you can book, in Swedish")
	}

	en := h.do("GET", "/resurs/ellastcykel?datum="+h.date(1), nil, english).Body.String()
	if !strings.Contains(en, "Tuesday 5 May") {
		t.Error("the English page should date the day in English")
	}
	if !strings.Contains(en, "1 month") {
		t.Error("the English page should say how far ahead you can book, in English")
	}
	for _, swedish := range []string{"tisdag", "månad", "Hur länge", "Lediga starttider"} {
		if strings.Contains(en, swedish) {
			t.Errorf("the English page still contains %q", swedish)
		}
	}
}

// A booking that cannot be made has to explain itself in the reader's language.
func TestValidationSpeaksTheReadersLanguage(t *testing.T) {
	h := newHarness(t)
	english := append(h.login("husets-losenord"), &http.Cookie{Name: i18n.Cookie, Value: "en"})

	rec := h.do("POST", "/resurs/elcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"3"},
		"medlem": {"anna.andersson"},
	}, english)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Choose one of the allowed lengths") {
		t.Errorf("the complaint should be in English:\n%s", body)
	}
	if strings.Contains(body, "tillåtna längderna") {
		t.Error("the Swedish complaint is still on the page")
	}
}

// ------------------------------------------------- what the bot writes in ---

// A direct message is not a page: it arrives in the member's chat, possibly
// days later, so it follows the language of their Mattermost account rather
// than whatever the browser that made the booking was set to.
func TestTheBotWritesInTheMembersOwnLanguage(t *testing.T) {
	h := newHarness(t)
	// The reader switches the site to English; Anna's account is Swedish.
	english := append(h.login("husets-losenord"), &http.Cookie{Name: i18n.Cookie, Value: "en"})

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"anna.andersson"},
	}, english)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d\n%s", rec.Code, rec.Body.String())
	}

	dm := h.chat.waitForDM(t, 1)
	if !strings.Contains(dm.Message, "är bekräftad") {
		t.Errorf("Anna reads Swedish in Mattermost, so her confirmation should be Swedish:\n%s", dm.Message)
	}

	// Bo's account is set to English, and his confirmation follows him.
	rec = h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(2)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"bo.bengtsson"},
	}, h.login("husets-losenord"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d\n%s", rec.Code, rec.Body.String())
	}
	dm = h.chat.waitForDM(t, 2)
	if !strings.Contains(dm.Message, "is confirmed") {
		t.Errorf("Bo reads English in Mattermost, so his confirmation should be English:\n%s", dm.Message)
	}
}

// The language is stored with the booking, so a cancellation months later
// still speaks it without asking Mattermost again.
func TestTheBookingRemembersItsLanguage(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"bo.bengtsson"},
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d", rec.Code)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(rec.Header().Get("Location"), "/bokning/"), "?ny=1")
	b, err := h.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if b.Lang != "en" {
		t.Errorf("stored language = %q, want en from Bo's account", b.Lang)
	}
	h.chat.waitForDM(t, 1)

	if rec := h.do("POST", "/bokning/"+id+"/avboka", url.Values{"token": {b.CancelToken}}, member); rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d", rec.Code)
	}
	if dm := h.chat.waitForDM(t, 2); !strings.Contains(dm.Message, "is **cancelled**") {
		t.Errorf("the cancellation should follow the booking's language:\n%s", dm.Message)
	}
}

// A member whose account says nothing we understand gets the house's own
// language rather than a blank or a guess.
func TestAnUnknownAccountLanguageFallsBackToTheHouses(t *testing.T) {
	h := newHarness(t)
	member := h.login("husets-losenord")

	rec := h.do("POST", "/resurs/ellastcykel/boka", url.Values{
		"datum": {h.date(1)}, "start": {"10:00"}, "langd": {"2"},
		"medlem": {"cecilia.dahl"}, // locale "de" in the fake directory
	}, member)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("booking = %d\n%s", rec.Code, rec.Body.String())
	}
	if dm := h.chat.waitForDM(t, 1); !strings.Contains(dm.Message, "är bekräftad") {
		t.Errorf("an unreadable locale should fall back to Swedish:\n%s", dm.Message)
	}
}
