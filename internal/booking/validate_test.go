package booking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func messages(errs []Error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Message
	}
	return strings.Join(parts, " | ")
}

func TestValidateAcceptsAGoodRequest(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")

	from, to, errs := Validate(context.Background(), st, Request{
		Resource:   bike(),
		Start:      at(loc, "2026-05-02 10:00"),
		End:        at(loc, "2026-05-02 14:00"),
		Name:       "Anna Andersson",
		MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %s", messages(errs))
	}
	// The blocked window is the booking widened by the 15 minute buffer.
	if got := from.In(loc).Format("15:04"); got != "09:45" {
		t.Errorf("block starts %s, want 09:45", got)
	}
	if got := to.In(loc).Format("15:04"); got != "14:15" {
		t.Errorf("block ends %s, want 14:15", got)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")
	base := Request{
		Resource:   bike(),
		Start:      at(loc, "2026-05-02 10:00"),
		End:        at(loc, "2026-05-02 14:00"),
		Name:       "Anna",
		MMUsername: "anna.andersson",
	}

	cases := []struct {
		name    string
		mutate  func(*Request)
		wantSub string
	}{
		{"no name", func(r *Request) { r.Name = "  " }, "namn"},
		{"no member", func(r *Request) { r.MMUsername = "" }, "Välj vem i huset"},
		{"unlisted duration", func(r *Request) { r.End = r.Start.Add(3 * time.Hour) }, "tillåtna längderna"},
		{"before opening", func(r *Request) {
			r.Start = at(loc, "2026-05-02 04:00")
			r.End = r.Start.Add(4 * time.Hour)
		}, "kan bokas mellan"},
		{"after closing", func(r *Request) {
			r.Start = at(loc, "2026-05-02 20:00")
			r.End = r.Start.Add(4 * time.Hour)
		}, "kan bokas mellan"},
		{"off the slot grid", func(r *Request) {
			r.Start = at(loc, "2026-05-02 10:10")
			r.End = r.Start.Add(4 * time.Hour)
		}, "30-minutersgräns"},
		{"in the past", func(r *Request) {
			r.Start = at(loc, "2026-04-20 10:00")
			r.End = r.Start.Add(4 * time.Hour)
		}, "passerat"},
		{"beyond the horizon", func(r *Request) {
			r.Start = at(loc, "2026-09-02 10:00")
			r.End = r.Start.Add(4 * time.Hour)
		}, "i förväg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			_, _, errs := Validate(context.Background(), st, req, now, loc, i18n.SV)
			if len(errs) == 0 {
				t.Fatalf("expected an error mentioning %q, got none", tc.wantSub)
			}
			if !strings.Contains(messages(errs), tc.wantSub) {
				t.Errorf("errors %q do not mention %q", messages(errs), tc.wantSub)
			}
		})
	}
}

func TestValidateEnforcesActiveBookingCap(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")
	res := bike()
	res.Rules.MaxActivePerUser = 1

	first := store.Booking{
		ID: "a", ResourceID: res.ID,
		Start:      at(loc, "2026-05-03 10:00"),
		End:        at(loc, "2026-05-03 12:00"),
		MMUsername: "anna.andersson",
		Name:       "Anna",
		Status:     store.StatusConfirmed, CreatedAt: now,
	}
	if err := st.Create(context.Background(), first, first.Start, first.End); err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	_, _, errs := Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-04 10:00"),
		End:      at(loc, "2026-05-04 12:00"),
		Name:     "Anna", MMUsername: "ANNA.Andersson",
	}, now, loc, i18n.SV)

	if !strings.Contains(messages(errs), "aktiva bokningar") {
		t.Errorf("expected the per-member cap to fire, got %q", messages(errs))
	}
}

func TestValidateEnforcesWeeklyHourCap(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")
	res := bike()
	res.Rules.MaxHoursPerWeekPerUser = 6
	res.Rules.MaxActivePerUser = 0

	seed := store.Booking{
		ID: "a", ResourceID: res.ID,
		Start:      at(loc, "2026-05-03 10:00"),
		End:        at(loc, "2026-05-03 14:00"),
		MMUsername: "anna.andersson", Name: "Anna",
		Status: store.StatusConfirmed, CreatedAt: now,
	}
	if err := st.Create(context.Background(), seed, seed.Start, seed.End); err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	_, _, errs := Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-04 10:00"),
		End:      at(loc, "2026-05-04 14:00"), // 4 h more, over the 6 h cap
		Name:     "Anna", MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)
	if !strings.Contains(messages(errs), "gränsen är") {
		t.Errorf("expected the weekly hour cap to fire, got %q", messages(errs))
	}

	// Two hours fits inside the cap.
	_, _, errs = Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-04 10:00"),
		End:      at(loc, "2026-05-04 12:00"),
		Name:     "Anna", MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)
	if len(errs) != 0 {
		t.Errorf("2 h should fit under the 6 h cap, got %q", messages(errs))
	}
}

func TestValidateDetectsOverlap(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")

	seed := store.Booking{
		ID: "a", ResourceID: "ellastcykel",
		Start:      at(loc, "2026-05-03 10:00"),
		End:        at(loc, "2026-05-03 14:00"),
		MMUsername: "bo.bengtsson", Name: "Bo",
		Status: store.StatusConfirmed, CreatedAt: now,
	}
	if err := st.Create(context.Background(), seed, seed.Start, seed.End); err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	// 14:00 would touch the previous booking's 15 minute buffer.
	_, _, errs := Validate(context.Background(), st, Request{
		Resource: bike(),
		Start:    at(loc, "2026-05-03 14:00"),
		End:      at(loc, "2026-05-03 15:00"),
		Name:     "Anna", MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)
	if !strings.Contains(messages(errs), "bokad") {
		t.Errorf("expected a conflict inside the buffer, got %q", messages(errs))
	}

	// 14:30 clears the buffer.
	_, _, errs = Validate(context.Background(), st, Request{
		Resource: bike(),
		Start:    at(loc, "2026-05-03 14:30"),
		End:      at(loc, "2026-05-03 15:30"),
		Name:     "Anna", MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)
	if len(errs) != 0 {
		t.Errorf("14:30 clears the buffer but was rejected: %q", messages(errs))
	}
}

func TestValidateNightLimits(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")

	mk := func(from, to string) []Error {
		start, end := DayRange(room(), at(loc, from+" 00:00"), at(loc, to+" 00:00"), loc)
		_, _, errs := Validate(context.Background(), st, Request{
			Resource: room(), Start: start, End: end,
			Name: "Anna", MMUsername: "anna.andersson",
		}, now, loc, i18n.SV)
		return errs
	}

	if errs := mk("2026-05-10", "2026-05-13"); len(errs) != 0 {
		t.Errorf("3 nights should be fine, got %q", messages(errs))
	}
	if errs := mk("2026-05-10", "2026-05-25"); !strings.Contains(messages(errs), "Längsta bokning") {
		t.Errorf("15 nights should exceed the 7 night limit, got %q", messages(errs))
	}
}

func TestValidateRejectsDisabledResource(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	off := false
	res := bike()
	res.Enabled = &off

	_, _, errs := Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-02 10:00"),
		End:      at(loc, "2026-05-02 14:00"),
		Name:     "Anna", MMUsername: "anna.andersson",
	}, at(loc, "2026-05-01 08:00"), loc, i18n.SV)

	if !strings.Contains(messages(errs), "går inte att boka") {
		t.Errorf("a disabled resource should be refused, got %q", messages(errs))
	}
}

func TestValidateSkipsCapsForCaretakers(t *testing.T) {
	loc := stockholm(t)
	st := newStore(t)
	now := at(loc, "2026-05-01 08:00")
	res := bike()
	res.Rules.MaxActivePerUser = 1
	res.Rules.MaxHoursPerWeekPerUser = 6
	res.Rules.Caretakers = []string{"simm"}

	// One upcoming booking of four hours: enough to trip both caps for an
	// ordinary member on the next attempt.
	seed := store.Booking{
		ID: "a", ResourceID: res.ID,
		Start:      at(loc, "2026-05-03 10:00"),
		End:        at(loc, "2026-05-03 14:00"),
		MMUsername: "simm", Name: "Simon",
		Status: store.StatusConfirmed, CreatedAt: now,
	}
	if err := st.Create(context.Background(), seed, seed.Start, seed.End); err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	_, _, errs := Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-04 10:00"),
		End:      at(loc, "2026-05-04 14:00"),
		Name:     "Simon", MMUsername: "@Simm",
	}, now, loc, i18n.SV)
	if len(errs) != 0 {
		t.Errorf("a caretaker should pass both caps, got %q", messages(errs))
	}

	// The exemption is per name: everyone else is still capped.
	other := store.Booking{
		ID: "b", ResourceID: res.ID,
		Start:      at(loc, "2026-05-05 10:00"),
		End:        at(loc, "2026-05-05 12:00"),
		MMUsername: "anna.andersson", Name: "Anna",
		Status: store.StatusConfirmed, CreatedAt: now,
	}
	if err := st.Create(context.Background(), other, other.Start, other.End); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	_, _, errs = Validate(context.Background(), st, Request{
		Resource: res,
		Start:    at(loc, "2026-05-06 10:00"),
		End:      at(loc, "2026-05-06 12:00"),
		Name:     "Anna", MMUsername: "anna.andersson",
	}, now, loc, i18n.SV)
	if !strings.Contains(messages(errs), "aktiva bokningar") {
		t.Errorf("expected the per-member cap to still fire for others, got %q", messages(errs))
	}
}

func TestValidateBlockCoversWholeDaysAndOnlyForCaretakers(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-01 08:00")
	res := bike()
	res.Rules.Caretakers = []string{"simm"}
	day := func(s string) time.Time { return at(loc, s+" 00:00") }

	start, end, errs := ValidateBlock(BlockRequest{
		Resource: res, From: day("2026-05-03"), To: day("2026-05-04"),
		Reason: "Punktering", MMUsername: "@Simm",
	}, now, loc, i18n.SV)
	if len(errs) != 0 {
		t.Fatalf("a caretaker's two-day block should pass, got %q", messages(errs))
	}
	if !start.Equal(day("2026-05-03")) || !end.Equal(day("2026-05-05")) {
		t.Errorf("block = %v – %v, want midnight 3 May to midnight 5 May", start, end)
	}

	cases := []struct {
		name string
		req  BlockRequest
		want string
	}{
		{"not a caretaker", BlockRequest{Resource: res, From: day("2026-05-03"), To: day("2026-05-03"), Reason: "x", MMUsername: "anna.andersson"}, "ansvarar"},
		{"no reason", BlockRequest{Resource: res, From: day("2026-05-03"), To: day("2026-05-03"), Reason: "  ", MMUsername: "simm"}, "varför"},
		{"no dates", BlockRequest{Resource: res, Reason: "x", MMUsername: "simm"}, "första och sista"},
		{"ends before it starts", BlockRequest{Resource: res, From: day("2026-05-04"), To: day("2026-05-03"), Reason: "x", MMUsername: "simm"}, "Sista dagen"},
		{"in the past", BlockRequest{Resource: res, From: day("2026-04-30"), To: day("2026-05-03"), Reason: "x", MMUsername: "simm"}, "passerat"},
		{"a year and a day", BlockRequest{Resource: res, From: day("2026-05-03"), To: day("2027-05-03"), Reason: "x", MMUsername: "simm"}, "högst"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := ValidateBlock(tc.req, now, loc, i18n.SV)
			if !strings.Contains(messages(errs), tc.want) {
				t.Errorf("errors %q do not mention %q", messages(errs), tc.want)
			}
		})
	}

	// The administrator may block on a caretaker's behalf; today is fine.
	_, _, errs = ValidateBlock(BlockRequest{
		Resource: res, From: day("2026-05-01"), To: day("2026-05-01"),
		Reason: "x", MMUsername: "anna.andersson", Admin: true,
	}, now, loc, i18n.SV)
	if len(errs) != 0 {
		t.Errorf("an admin blocking today should pass, got %q", messages(errs))
	}
}
