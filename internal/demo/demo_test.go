package demo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

const testConfig = `
site:
  title: Demo
  timezone: Europe/Stockholm
resources:
  - id: ellastcykel
    name: Ellastcykeln
    booking:
      mode: hours
      durations: [1, 2, 4, 8]
      slot_step_minutes: 30
      buffer_minutes: 15
      open_from: "06:00"
      open_to: "22:00"
      max_advance_days: 30
  - id: gastrum-1
    name: Gästrum 1
    booking:
      mode: days
      min_days: 1
      max_days: 7
      max_advance_days: 180
  - id: avstangd
    name: Avstängd
    enabled: false
    booking:
      mode: hours
`

func setup(t *testing.T) (*store.Store, *config.Config, time.Time) {
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
	st, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// A Monday morning, so the seeded week is predictable.
	return st, cfg, time.Date(2026, 5, 4, 8, 0, 0, 0, cfg.Location())
}

func TestSeedFillsEveryActiveResource(t *testing.T) {
	st, cfg, now := setup(t)
	ctx := context.Background()

	n, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n == 0 {
		t.Fatal("seeded nothing")
	}

	all, err := st.Search(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	byResource := map[string]int{}
	for _, b := range all {
		byResource[b.ResourceID]++
	}
	if byResource["ellastcykel"] == 0 {
		t.Error("the bike got no example bookings")
	}
	if byResource["gastrum-1"] == 0 {
		t.Error("the guest room got no example bookings")
	}
	if byResource["avstangd"] != 0 {
		t.Error("a disabled resource must not be seeded")
	}
}

// The demo must leave plenty to book, or there is nothing to try out.
func TestSeedLeavesRoomToBook(t *testing.T) {
	st, cfg, now := setup(t)
	if _, err := Seed(context.Background(), st, cfg, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, _ := cfg.Resource("ellastcykel")
	loc := cfg.Location()

	for day := 0; day < 7; day++ {
		date := now.AddDate(0, 0, day)
		existing, err := st.InRange(context.Background(), res.ID,
			date.AddDate(0, 0, -1), date.AddDate(0, 0, 2))
		if err != nil {
			t.Fatalf("in range: %v", err)
		}
		view := booking.BuildDay(res, date, 2*time.Hour, existing, now, loc, "", i18n.SV)
		if view.FreeCount == 0 {
			t.Errorf("day +%d has no free two-hour slot; the demo looks fully booked", day)
		}
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	st, cfg, now := setup(t)
	ctx := context.Background()

	first, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	second, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second != 0 {
		t.Errorf("second seed created %d more bookings, want none", second)
	}

	all, _ := st.Search(ctx, store.Filter{})
	if len(all) != first {
		t.Errorf("database holds %d bookings after two seeds, want %d", len(all), first)
	}
}

// Every seeded booking must obey the rules the site enforces, so the demo does
// not show states a member could never create.
func TestSeededBookingsAreValid(t *testing.T) {
	st, cfg, now := setup(t)
	if _, err := Seed(context.Background(), st, cfg, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	all, err := st.Search(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	loc := cfg.Location()

	for _, b := range all {
		res, ok := cfg.Resource(b.ResourceID)
		if !ok {
			t.Fatalf("booking for unknown resource %q", b.ResourceID)
		}
		if !b.End.After(b.Start) {
			t.Errorf("%s ends before it starts", b.ID)
		}
		if b.Start.Before(now) {
			t.Errorf("%s starts in the past", b.ID)
		}
		if b.Start.After(now.AddDate(0, 0, res.Rules.MaxAdvanceDays)) {
			t.Errorf("%s is beyond the booking horizon", b.ID)
		}
		if b.Name == "" || b.MMUsername == "" {
			t.Errorf("%s has no name or Mattermost account", b.ID)
		}
		if res.Rules.Mode == config.ModeHours {
			openFrom, _ := config.ParseClock(res.Rules.OpenFrom)
			openTo, _ := config.ParseClock(res.Rules.OpenTo)
			s, e := b.Start.In(loc), b.End.In(loc)
			if mins := s.Hour()*60 + s.Minute(); mins < openFrom {
				t.Errorf("%s starts before opening", b.ID)
			}
			if mins := e.Hour()*60 + e.Minute(); mins > openTo && mins != 0 {
				t.Errorf("%s ends after closing", b.ID)
			}
			allowed := false
			for _, d := range res.Rules.Durations {
				if time.Duration(d*float64(time.Hour)) == e.Sub(s) {
					allowed = true
				}
			}
			if !allowed {
				t.Errorf("%s has a length nobody could book: %v", b.ID, e.Sub(s))
			}
		}
	}
}

// Demo bookings must carry no way of reaching anyone: no e-mail address, and
// no Mattermost account id for the bot to send a message to.
func TestSeedCannotReachAnyone(t *testing.T) {
	st, cfg, now := setup(t)
	if _, err := Seed(context.Background(), st, cfg, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	all, _ := st.Search(context.Background(), store.Filter{})
	for _, b := range all {
		if b.Email != "" {
			t.Errorf("%s carries the address %q; demo data must reach nobody", b.ID, b.Email)
		}
		if b.MMUserID != "" {
			t.Errorf("%s carries the Mattermost id %q; demo data must reach nobody", b.ID, b.MMUserID)
		}
	}
}
