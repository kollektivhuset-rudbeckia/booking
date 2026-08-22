package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustLoad parses a configuration that the test expects to be valid.
func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimal = `
site:
  title: Test
categories:
  - id: cyklar
    name: Cyklar
resources:
  - id: elcykel
    category: cyklar
    name: Elcykeln
    booking:
      mode: hours
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Site.Timezone != "Europe/Stockholm" {
		t.Errorf("timezone = %q, want the Stockholm default", cfg.Site.Timezone)
	}
	if cfg.Location() == nil {
		t.Fatal("location was not resolved")
	}
	r, ok := cfg.Resource("elcykel")
	if !ok {
		t.Fatal("resource elcykel missing")
	}
	if got, want := len(r.Rules.Durations), 4; got != want {
		t.Errorf("durations = %v, want the 1/2/4/8 default", r.Rules.Durations)
	}
	if r.Rules.SlotStepMinutes != 30 || r.Rules.MaxAdvanceDays != 90 {
		t.Errorf("defaults not applied: %+v", r.Rules)
	}
	if r.Rules.OpenFrom != "00:00" || r.Rules.OpenTo != "24:00" {
		t.Errorf("opening hours = %s–%s, want the full day", r.Rules.OpenFrom, r.Rules.OpenTo)
	}
	if !r.Active() {
		t.Error("a resource without an explicit enabled flag should be active")
	}
}

func TestLoadDayModeDefaults(t *testing.T) {
	cfg, err := Load(write(t, `
site:
  title: Test
resources:
  - id: gastrum
    name: Gästrum
    booking:
      mode: days
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r, _ := cfg.Resource("gastrum")
	if r.Rules.MinDays != 1 || r.Rules.MaxDays != 14 {
		t.Errorf("night limits = %d–%d, want 1–14", r.Rules.MinDays, r.Rules.MaxDays)
	}
	if r.Rules.CheckIn != "15:00" || r.Rules.CheckOut != "12:00" {
		t.Errorf("check times = %s/%s", r.Rules.CheckIn, r.Rules.CheckOut)
	}
}

func TestLoadSortsDurations(t *testing.T) {
	cfg, err := Load(write(t, `
site:
  title: Test
resources:
  - id: cykel
    name: Cykel
    booking:
      durations: [8, 1, 4, 2]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r, _ := cfg.Resource("cykel")
	want := []float64{1, 2, 4, 8}
	for i, d := range want {
		if r.Rules.Durations[i] != d {
			t.Fatalf("durations = %v, want sorted %v", r.Rules.Durations, want)
		}
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]string{
		"duplicate resource id": `
site: {title: T}
resources:
  - {id: a, name: A}
  - {id: a, name: B}
`,
		"unknown category": `
site: {title: T}
resources:
  - {id: a, name: A, category: nope}
`,
		"missing id": `
site: {title: T}
resources:
  - {name: A}
`,
		"unknown mode": `
site: {title: T}
resources:
  - {id: a, name: A, booking: {mode: weeks}}
`,
		"closing before opening": `
site: {title: T}
resources:
  - {id: a, name: A, booking: {open_from: "20:00", open_to: "08:00"}}
`,
		"bad clock": `
site: {title: T}
resources:
  - {id: a, name: A, booking: {open_from: "25:00"}}
`,
		"max nights below min": `
site: {title: T}
resources:
  - {id: a, name: A, booking: {mode: days, min_days: 5, max_days: 2}}
`,
		"unknown timezone": `
site: {title: T, timezone: Mars/Olympus}
resources: []
`,
		"unknown field": `
site: {title: T}
resources:
  - {id: a, name: A, colour: blue}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestGroupedKeepsConfigOrderAndDropsEmpties(t *testing.T) {
	cfg, err := Load(write(t, `
site: {title: T}
categories:
  - {id: cyklar, name: Cyklar}
  - {id: tomma, name: Tomma}
  - {id: rum, name: Rum}
resources:
  - {id: a, name: A, category: cyklar}
  - {id: b, name: B, category: rum}
  - {id: c, name: C}
  - {id: d, name: D, category: cyklar, enabled: false}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	groups := cfg.Grouped()
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want cyklar, rum and the loose bucket", len(groups))
	}
	if groups[0].Category.ID != "cyklar" || groups[1].Category.ID != "rum" {
		t.Errorf("groups out of config order: %s, %s", groups[0].Category.ID, groups[1].Category.ID)
	}
	if len(groups[0].Resources) != 1 {
		t.Errorf("the disabled resource should be hidden, got %d", len(groups[0].Resources))
	}
	// The loose bucket has no name of its own: the page names it, in the
	// language it is being read in.
	if groups[2].Category.ID != "ovrigt" {
		t.Errorf("uncategorised resources should fall into a group of their own, got %q", groups[2].Category.ID)
	}
	if len(groups[2].Resources) != 1 {
		t.Errorf("the uncategorised resource is missing, got %d", len(groups[2].Resources))
	}
}

func TestParseAndFormatClock(t *testing.T) {
	cases := map[string]int{"00:00": 0, "06:30": 390, "15:00": 900, "24:00": 1440}
	for in, want := range cases {
		got, err := ParseClock(in)
		if err != nil {
			t.Fatalf("ParseClock(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseClock(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "8", "8:00:00", "24:30", "-1:00", "aa:bb", "12:99"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
	if got := FormatClock(390); got != "06:30" {
		t.Errorf("FormatClock(390) = %q, want 06:30", got)
	}
}

func TestLoadReportsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("err = %v, want a read failure", err)
	}
}

func TestMattermostAllowList(t *testing.T) {
	open := MattermostSettings{}
	if !open.Allowed("vem.som.helst") {
		t.Error("without an allow list everyone in the house may book")
	}

	limited := MattermostSettings{Allow: []string{"mikael.ostberg"}}
	for _, ok := range []string{"mikael.ostberg", "@mikael.ostberg", " Mikael.Ostberg "} {
		if !limited.Allowed(ok) {
			t.Errorf("Allowed(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "anna.andersson", "mikael"} {
		if limited.Allowed(no) {
			t.Errorf("Allowed(%q) = true, want false", no)
		}
	}
	if limited.AllowList() != "mikael.ostberg" {
		t.Errorf("AllowList() = %q", limited.AllowList())
	}
}

func TestMattermostEnabledNeedsBothHalves(t *testing.T) {
	cases := []struct {
		settings MattermostSettings
		want     bool
	}{
		{MattermostSettings{}, false},
		{MattermostSettings{URL: "https://chat.example.com"}, false},
		{MattermostSettings{Token: "tok"}, false},
		{MattermostSettings{URL: "https://chat.example.com", Token: "tok"}, true},
	}
	for _, c := range cases {
		if got := c.settings.Enabled(); got != c.want {
			t.Errorf("Enabled(%+v) = %v, want %v", c.settings, got, c.want)
		}
	}
}

func TestLoadRuntimeReadsTheMattermostBot(t *testing.T) {
	t.Setenv("BOOKING_PASSWORD", "hemligt")
	t.Setenv("MATTERMOST_URL", "https://chat.rudbeckia.nu/")
	t.Setenv("MATTERMOST_TOKEN", "  bot-token  ")
	t.Setenv("MATTERMOST_ALLOW", "@Mikael.Ostberg, anna.andersson ,")

	rt, err := LoadRuntime()
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if rt.Mattermost.URL != "https://chat.rudbeckia.nu" {
		t.Errorf("url = %q, want it without the trailing slash", rt.Mattermost.URL)
	}
	if rt.Mattermost.Token != "bot-token" {
		t.Errorf("token = %q, want it trimmed", rt.Mattermost.Token)
	}
	want := []string{"mikael.ostberg", "anna.andersson"}
	if len(rt.Mattermost.Allow) != 2 || rt.Mattermost.Allow[0] != want[0] || rt.Mattermost.Allow[1] != want[1] {
		t.Errorf("allow = %v, want %v", rt.Mattermost.Allow, want)
	}
}

// Half a bot is worse than none: it looks configured and silently is not.
func TestLoadRuntimeRefusesHalfAMattermostConfiguration(t *testing.T) {
	t.Setenv("BOOKING_PASSWORD", "hemligt")
	t.Setenv("MATTERMOST_URL", "https://chat.rudbeckia.nu")
	t.Setenv("MATTERMOST_TOKEN", "")
	if _, err := LoadRuntime(); err == nil {
		t.Error("a url without a token should be refused at startup")
	}
}

// Demo mode must never reach a real chat server.
func TestDemoModeDropsTheMattermostConfiguration(t *testing.T) {
	t.Setenv("DEMO", "true")
	t.Setenv("MATTERMOST_URL", "https://chat.rudbeckia.nu")
	t.Setenv("MATTERMOST_TOKEN", "bot-token")

	rt, err := LoadRuntime()
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if rt.Mattermost.Enabled() {
		t.Errorf("demo mode kept %+v; it must talk to nobody", rt.Mattermost)
	}
}

func TestLanguageDefaultsToSwedishAndRefusesOthers(t *testing.T) {
	cfg := mustLoad(t, `
site:
  title: Bokning
resources:
  - id: cykel
    name: Cykeln
`)
	if cfg.Site.Language != "sv" {
		t.Errorf("language = %q, want sv by default", cfg.Site.Language)
	}

	if _, err := Load(write(t, `
site:
  title: Bokning
  language: de
resources:
  - id: cykel
    name: Cykeln
`)); err == nil {
		t.Error("a language the site has no words for should be refused")
	}
}

// The house writes its own words once, and adds an _en sibling when it has
// one. A missing translation shows the other language rather than a blank.
func TestTheHousesOwnWordsFallBackBetweenLanguages(t *testing.T) {
	cfg := mustLoad(t, `
site:
  title: Bokning
  tagline: Kollektivhuset
  tagline_en: The housing co-operative
categories:
  - id: cyklar
    name: Cyklar
    name_en: Bikes
    description: Husets cyklar.
resources:
  - id: cykel
    category: cyklar
    name: Ellastcykeln
    name_en: The cargo bike
    description: Med plats för barn.
    location: Cykelrummet
    location_en: The bike room
    instructions: Nyckeln hänger i städrummet.
`)
	res := cfg.Resources[0]
	cat := cfg.Categories[0]

	cases := []struct{ got, want string }{
		{cfg.Site.TaglineFor("sv"), "Kollektivhuset"},
		{cfg.Site.TaglineFor("en"), "The housing co-operative"},
		{cat.NameFor("en"), "Bikes"},
		{res.NameFor("sv"), "Ellastcykeln"},
		{res.NameFor("en"), "The cargo bike"},
		{res.LocationFor("en"), "The bike room"},
		// No English written: the Swedish shows through rather than nothing.
		{res.DescriptionFor("en"), "Med plats för barn."},
		{res.InstructionsFor("en"), "Nyckeln hänger i städrummet."},
		{cat.DescriptionFor("en"), "Husets cyklar."},
		// And a language nobody configured behaves like the Swedish one.
		{res.NameFor("de"), "Ellastcykeln"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// A category with no name at all is the loose bucket, which the page names.
func TestLinkLabelFallsBackToTheAddress(t *testing.T) {
	c := Category{Link: "https://chat.example.com/channels/cykelpoolen"}
	if got := c.LinkLabelFor("sv"); got != c.Link {
		t.Errorf("= %q, want the bare address", got)
	}
	c.LinkText = "#cykelpoolen"
	if got := c.LinkLabelFor("en"); got != "#cykelpoolen" {
		t.Errorf("= %q, want the Swedish label when there is no English one", got)
	}
	c.LinkTextEN = "#cykelpoolen in Mattermost"
	if got := c.LinkLabelFor("en"); got != "#cykelpoolen in Mattermost" {
		t.Errorf("= %q", got)
	}
}
