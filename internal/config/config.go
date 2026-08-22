// Package config loads the declarative description of what can be booked and
// under which rules, plus the runtime settings that come from the environment.
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode describes how a resource is booked.
type Mode string

const (
	// ModeHours books a resource for a number of hours inside a single day,
	// e.g. a cargo bike for 4 hours.
	ModeHours Mode = "hours"
	// ModeDays books a resource for whole days/nights, e.g. a guest room.
	ModeDays Mode = "days"
)

// Config is the whole bookable world: site settings, categories and resources.
type Config struct {
	Site       Site       `yaml:"site"`
	Categories []Category `yaml:"categories"`
	Resources  []Resource `yaml:"resources"`

	location *time.Location
}

// Site holds presentation-level settings shared by every page.
type Site struct {
	Title string `yaml:"title"`
	// Language is what a visitor sees before choosing for themselves, and the
	// language of anything written to somebody we cannot ask. "sv" or "en".
	Language   string `yaml:"language"`
	Tagline    string `yaml:"tagline"`
	TaglineEN  string `yaml:"tagline_en"`
	HouseName  string `yaml:"house_name"`
	Timezone   string `yaml:"timezone"`
	HomeURL    string `yaml:"home_url"`
	SupportURL string `yaml:"support_url"`
	// FooterNote is rendered at the bottom of every page (Markdown-free plain text).
	FooterNote   string `yaml:"footer_note"`
	FooterNoteEN string `yaml:"footer_note_en"`
}

// TaglineFor and FooterFor give the house's own words in one language.
func (s Site) TaglineFor(lang string) string { return pick(lang, s.Tagline, s.TaglineEN) }
func (s Site) FooterFor(lang string) string  { return pick(lang, s.FooterNote, s.FooterNoteEN) }

// pick returns the wording for a language, falling back to the other one.
// Half a translation is better than a blank space where a sentence was: the
// reader can still make out what the house meant.
func pick(lang, sv, en string) string {
	sv, en = strings.TrimSpace(sv), strings.TrimSpace(en)
	if lang == "en" {
		if en != "" {
			return en
		}
		return sv
	}
	if sv != "" {
		return sv
	}
	return en
}

// Category groups resources on the start page. Everything a member reads has
// an _en sibling; leave it out and the Swedish is shown instead.
type Category struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	NameEN        string `yaml:"name_en"`
	Description   string `yaml:"description"`
	DescriptionEN string `yaml:"description_en"`
	Emoji         string `yaml:"emoji"`
	// Link and LinkText point somewhere the group belongs, typically the
	// Mattermost channel where the house talks about these things.
	Link       string `yaml:"link"`
	LinkText   string `yaml:"link_text"`
	LinkTextEN string `yaml:"link_text_en"`
}

// NameFor and DescriptionFor give the category's words in one language.
func (c Category) NameFor(lang string) string { return pick(lang, c.Name, c.NameEN) }
func (c Category) DescriptionFor(lang string) string {
	return pick(lang, c.Description, c.DescriptionEN)
}

// LinkLabelFor is the text to show for Link, falling back to the bare address.
func (c Category) LinkLabelFor(lang string) string {
	if label := pick(lang, c.LinkText, c.LinkTextEN); label != "" {
		return label
	}
	return c.Link
}

// Resource is one bookable thing: a bike, a room, a workshop. Everything a
// member reads has an _en sibling; leave it out and the Swedish is shown, so a
// house that only speaks one language writes each thing once.
type Resource struct {
	ID             string   `yaml:"id"`
	Category       string   `yaml:"category"`
	Name           string   `yaml:"name"`
	NameEN         string   `yaml:"name_en"`
	Emoji          string   `yaml:"emoji"`
	Description    string   `yaml:"description"`
	DescriptionEN  string   `yaml:"description_en"`
	Location       string   `yaml:"location"`
	LocationEN     string   `yaml:"location_en"`
	Instructions   string   `yaml:"instructions"`
	InstructionsEN string   `yaml:"instructions_en"`
	InfoURL        string   `yaml:"info_url"`
	Enabled        *bool    `yaml:"enabled"`
	Rules          Rules    `yaml:"booking"`
	Fields         []string `yaml:"extra_fields"`
}

// NameFor, DescriptionFor, LocationFor and InstructionsFor give the house's
// own words about this thing in one language.
func (r Resource) NameFor(lang string) string { return pick(lang, r.Name, r.NameEN) }
func (r Resource) DescriptionFor(lang string) string {
	return pick(lang, r.Description, r.DescriptionEN)
}
func (r Resource) LocationFor(lang string) string { return pick(lang, r.Location, r.LocationEN) }
func (r Resource) InstructionsFor(lang string) string {
	return pick(lang, r.Instructions, r.InstructionsEN)
}

// Active reports whether the resource should be offered for booking.
func (r Resource) Active() bool { return r.Enabled == nil || *r.Enabled }

// Rules are the bookability parameters for one resource. Every field has a
// sensible default so a minimal config stays readable.
type Rules struct {
	Mode Mode `yaml:"mode"`

	// --- hour mode ---
	// Durations are the offered lengths in hours, e.g. [1, 2, 4, 8]. They are
	// the one-click choices; see CustomDuration for anything in between.
	Durations []float64 `yaml:"durations"`
	// CustomDuration lets a member type their own length instead of picking
	// one of Durations. It still has to fit MinDuration/MaxDuration, the
	// opening hours and the slot grid.
	CustomDuration bool `yaml:"custom_duration"`
	// MinDurationMinutes and MaxDurationMinutes bound a typed-in length.
	// They default to the slot step and to the longest offered duration.
	MinDurationMinutes int `yaml:"min_duration_minutes"`
	MaxDurationMinutes int `yaml:"max_duration_minutes"`
	// SlotStepMinutes is the grid that start times snap to.
	SlotStepMinutes int `yaml:"slot_step_minutes"`
	// OpenFrom/OpenTo bound the part of the day that can be booked ("06:00").
	OpenFrom string `yaml:"open_from"`
	OpenTo   string `yaml:"open_to"`

	// --- day mode ---
	MinDays  int    `yaml:"min_days"`
	MaxDays  int    `yaml:"max_days"`
	CheckIn  string `yaml:"check_in"`
	CheckOut string `yaml:"check_out"`

	// --- shared ---
	// BufferMinutes is mandatory free space kept between two bookings.
	BufferMinutes int `yaml:"buffer_minutes"`
	// MaxAdvanceDays is how far into the future a booking may reach.
	MaxAdvanceDays int `yaml:"max_advance_days"`
	// MinNoticeMinutes blocks bookings starting too soon from now.
	MinNoticeMinutes int `yaml:"min_notice_minutes"`
	// MaxActivePerUser caps simultaneous upcoming bookings per member. 0 = no cap.
	MaxActivePerUser int `yaml:"max_active_per_user"`
	// MaxHoursPerWeekPerUser caps booked hours in a rolling week. 0 = no cap.
	MaxHoursPerWeekPerUser float64 `yaml:"max_hours_per_week_per_user"`
	// RequireApproval is reserved for a future moderation step.
	RequireApproval bool `yaml:"require_approval"`
}

// Runtime holds the settings that come from the environment rather than YAML,
// because they are secrets or deployment specific.
type Runtime struct {
	ListenAddr    string
	ConfigPath    string
	DBPath        string
	BaseURL       string
	Password      string
	AdminPassword string
	SessionSecret []byte
	SessionMaxAge time.Duration
	Mattermost    MattermostSettings
	TrustProxy    bool
	// Demo fills in throwaway passwords, seeds example bookings and shows a
	// banner saying so. Never enable it on a real deployment.
	Demo bool
}

// MattermostSettings configures the bot that notifies members and answers the
// booking form's user lookups.
type MattermostSettings struct {
	// URL is the Mattermost server, e.g. "https://chat.rudbeckia.nu".
	URL string
	// Token is the bot account's personal access token.
	Token string
	// Allow lists the usernames that may book at all. Empty means everyone in
	// the directory may book, which is where the house will end up; a short
	// list is how a rollout starts.
	Allow []string
}

// Enabled reports whether the bot can really reach Mattermost.
func (m MattermostSettings) Enabled() bool { return m.URL != "" && m.Token != "" }

// Allowed reports whether a Mattermost username may make bookings. With no
// allow list everyone may.
func (m MattermostSettings) Allowed(username string) bool {
	if len(m.Allow) == 0 {
		return true
	}
	username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(username), "@")))
	for _, a := range m.Allow {
		if strings.ToLower(a) == username {
			return true
		}
	}
	return false
}

// AllowList is the allowed usernames as a readable list, for the pages and
// messages that have to explain the restriction.
func (m MattermostSettings) AllowList() string { return strings.Join(m.Allow, ", ") }

// Load reads and validates the YAML configuration at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	if c.Site.Title == "" {
		c.Site.Title = "Bokning"
	}
	if c.Site.Timezone == "" {
		c.Site.Timezone = "Europe/Stockholm"
	}
	if c.Site.Language == "" {
		c.Site.Language = "sv"
	}
	if c.Site.Language != "sv" && c.Site.Language != "en" {
		return fmt.Errorf(`site.language must be "sv" or "en", not %q`, c.Site.Language)
	}
	loc, err := time.LoadLocation(c.Site.Timezone)
	if err != nil {
		return fmt.Errorf("unknown timezone %q: %w", c.Site.Timezone, err)
	}
	c.location = loc

	seenCat := map[string]bool{}
	for _, cat := range c.Categories {
		if cat.ID == "" {
			return fmt.Errorf("category %q is missing an id", cat.Name)
		}
		if seenCat[cat.ID] {
			return fmt.Errorf("duplicate category id %q", cat.ID)
		}
		seenCat[cat.ID] = true
	}

	seenRes := map[string]bool{}
	for i := range c.Resources {
		r := &c.Resources[i]
		if r.ID == "" {
			return fmt.Errorf("resource %q is missing an id", r.Name)
		}
		if seenRes[r.ID] {
			return fmt.Errorf("duplicate resource id %q", r.ID)
		}
		seenRes[r.ID] = true
		if r.Category != "" && !seenCat[r.Category] {
			return fmt.Errorf("resource %q refers to unknown category %q", r.ID, r.Category)
		}
		if err := normalizeRules(r); err != nil {
			return fmt.Errorf("resource %q: %w", r.ID, err)
		}
	}
	return nil
}

func normalizeRules(r *Resource) error {
	ru := &r.Rules
	if ru.Mode == "" {
		ru.Mode = ModeHours
	}
	switch ru.Mode {
	case ModeHours:
		if len(ru.Durations) == 0 {
			ru.Durations = []float64{1, 2, 4, 8}
		}
		sort.Float64s(ru.Durations)
		for _, d := range ru.Durations {
			if d <= 0 {
				return fmt.Errorf("duration %v must be positive", d)
			}
		}
		if ru.SlotStepMinutes <= 0 {
			ru.SlotStepMinutes = 30
		}
		if ru.MinDurationMinutes <= 0 {
			ru.MinDurationMinutes = ru.SlotStepMinutes
		}
		if ru.MaxDurationMinutes <= 0 {
			// The longest one-click choice, so turning on custom_duration
			// alone never quietly widens what people may book.
			ru.MaxDurationMinutes = int(ru.Durations[len(ru.Durations)-1] * 60)
		}
		if ru.MaxDurationMinutes < ru.MinDurationMinutes {
			return fmt.Errorf("max_duration_minutes (%d) is smaller than min_duration_minutes (%d)",
				ru.MaxDurationMinutes, ru.MinDurationMinutes)
		}
		if ru.MinDurationMinutes%ru.SlotStepMinutes != 0 {
			return fmt.Errorf("min_duration_minutes (%d) must be a multiple of slot_step_minutes (%d)",
				ru.MinDurationMinutes, ru.SlotStepMinutes)
		}
		if ru.OpenFrom == "" {
			ru.OpenFrom = "00:00"
		}
		if ru.OpenTo == "" {
			ru.OpenTo = "24:00"
		}
		if _, err := ParseClock(ru.OpenFrom); err != nil {
			return fmt.Errorf("open_from: %w", err)
		}
		to, err := ParseClock(ru.OpenTo)
		if err != nil {
			return fmt.Errorf("open_to: %w", err)
		}
		from, _ := ParseClock(ru.OpenFrom)
		if to <= from {
			return fmt.Errorf("open_to (%s) must be after open_from (%s)", ru.OpenTo, ru.OpenFrom)
		}
	case ModeDays:
		if ru.MinDays <= 0 {
			ru.MinDays = 1
		}
		if ru.MaxDays <= 0 {
			ru.MaxDays = 14
		}
		if ru.MaxDays < ru.MinDays {
			return fmt.Errorf("max_days (%d) is smaller than min_days (%d)", ru.MaxDays, ru.MinDays)
		}
		if ru.CheckIn == "" {
			ru.CheckIn = "15:00"
		}
		if ru.CheckOut == "" {
			ru.CheckOut = "12:00"
		}
		if _, err := ParseClock(ru.CheckIn); err != nil {
			return fmt.Errorf("check_in: %w", err)
		}
		if _, err := ParseClock(ru.CheckOut); err != nil {
			return fmt.Errorf("check_out: %w", err)
		}
	default:
		return fmt.Errorf("unknown booking mode %q (use %q or %q)", ru.Mode, ModeHours, ModeDays)
	}
	if ru.MaxAdvanceDays <= 0 {
		ru.MaxAdvanceDays = 90
	}
	if ru.BufferMinutes < 0 {
		return fmt.Errorf("buffer_minutes cannot be negative")
	}
	return nil
}

// Location is the timezone every date and time is presented in.
func (c *Config) Location() *time.Location { return c.location }

// Resource looks up a resource by id.
func (c *Config) Resource(id string) (Resource, bool) {
	for _, r := range c.Resources {
		if r.ID == id {
			return r, true
		}
	}
	return Resource{}, false
}

// Category looks up a category by id.
func (c *Config) Category(id string) (Category, bool) {
	for _, cat := range c.Categories {
		if cat.ID == id {
			return cat, true
		}
	}
	return Category{}, false
}

// Group is a category together with the resources that belong to it.
type Group struct {
	Category  Category
	Resources []Resource
}

// Grouped returns active resources bucketed by category, in config order.
// Resources without a category end up in a trailing group the page names.
func (c *Config) Grouped() []Group {
	groups := make([]Group, 0, len(c.Categories)+1)
	index := map[string]int{}
	for _, cat := range c.Categories {
		index[cat.ID] = len(groups)
		groups = append(groups, Group{Category: cat})
	}
	var loose []Resource
	for _, r := range c.Resources {
		if !r.Active() {
			continue
		}
		if i, ok := index[r.Category]; ok {
			groups[i].Resources = append(groups[i].Resources, r)
			continue
		}
		loose = append(loose, r)
	}
	out := groups[:0]
	for _, g := range groups {
		if len(g.Resources) > 0 {
			out = append(out, g)
		}
	}
	if len(loose) > 0 {
		// The name is left to the page, which has both languages.
		out = append(out, Group{Category: Category{ID: "ovrigt"}, Resources: loose})
	}
	return out
}

// ParseClock parses "HH:MM" into minutes since midnight. "24:00" is accepted
// and means end of day.
func ParseClock(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	if h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return h*60 + m, nil
}

// FormatClock renders minutes since midnight as "HH:MM".
func FormatClock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}
