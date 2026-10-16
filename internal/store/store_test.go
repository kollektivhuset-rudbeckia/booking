package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sample(id string, start time.Time, hours int) Booking {
	return Booking{
		ID: id, ResourceID: "ellastcykel",
		Start: start, End: start.Add(time.Duration(hours) * time.Hour),
		Mode: "hours", Name: "Anna", MMUsername: "anna.andersson",
		MMUserID: "u-anna", Email: "anna@example.se",
		Status: StatusConfirmed, CancelToken: "tok-" + id, CreatedAt: start.Add(-24 * time.Hour),
	}
}

func TestCreateAndGetRoundTrip(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	in := sample("a", start, 4)
	in.Apartment = "1403"
	in.Note = "Storhandling"

	if err := st.Create(ctx, in, in.Start, in.End); err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := st.Get(ctx, "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !out.Start.Equal(in.Start) || !out.End.Equal(in.End) {
		t.Errorf("times round-tripped wrong: %v–%v", out.Start, out.End)
	}
	if out.Apartment != "1403" || out.Note != "Storhandling" || out.CancelToken != "tok-a" {
		t.Errorf("fields round-tripped wrong: %+v", out)
	}
	if out.CancelledAt.Valid {
		t.Error("a fresh booking should not be cancelled")
	}
}

func TestCreateRejectsOverlap(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	first := sample("a", start, 4)
	if err := st.Create(ctx, first, first.Start, first.End); err != nil {
		t.Fatalf("create first: %v", err)
	}

	overlapping := sample("b", start.Add(2*time.Hour), 2)
	if err := st.Create(ctx, overlapping, overlapping.Start, overlapping.End); !errors.Is(err, ErrConflict) {
		t.Errorf("overlap error = %v, want ErrConflict", err)
	}

	// Back-to-back is fine when the caller asks for no buffer.
	adjacent := sample("c", start.Add(4*time.Hour), 2)
	if err := st.Create(ctx, adjacent, adjacent.Start, adjacent.End); err != nil {
		t.Errorf("adjacent booking rejected: %v", err)
	}

	// A different resource never conflicts.
	other := sample("d", start, 4)
	other.ResourceID = "elcykel"
	if err := st.Create(ctx, other, other.Start, other.End); err != nil {
		t.Errorf("other resource rejected: %v", err)
	}
}

// Two members clicking the same slot at the same moment must not both win.
func TestCreateIsRaceSafe(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)

	const racers = 8
	var wg sync.WaitGroup
	results := make([]error, racers)
	ready := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := sample(string(rune('a'+i)), start, 2)
			<-ready
			results[i] = st.Create(ctx, b, b.Start, b.End)
		}(i)
	}
	close(ready)
	wg.Wait()

	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrConflict):
		default:
			t.Errorf("racer %d got an unexpected error: %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d racers won the slot, want exactly 1", won)
	}
}

func TestCancelFreesTheSlot(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	start := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	b := sample("a", start, 4)
	if err := st.Create(ctx, b, b.Start, b.End); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := st.Cancel(ctx, "a", "wrong-token", false, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel with the wrong token: %v, want ErrNotFound", err)
	}
	if err := st.Cancel(ctx, "a", "tok-a", false, now); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Cancelling twice is a no-op, not a crash.
	if err := st.Cancel(ctx, "a", "tok-a", false, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second cancel: %v, want ErrNotFound", err)
	}

	got, err := st.Get(ctx, "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusCancelled || !got.CancelledAt.Valid {
		t.Errorf("status = %q cancelledAt.Valid = %v", got.Status, got.CancelledAt.Valid)
	}

	// The slot is bookable again.
	replacement := sample("b", start, 4)
	if err := st.Create(ctx, replacement, replacement.Start, replacement.End); err != nil {
		t.Errorf("slot not freed by cancellation: %v", err)
	}
}

func TestInRangeOnlyReturnsOverlaps(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	for i, b := range []Booking{
		sample("a", base, 2),                  // 10–12
		sample("b", base.Add(4*time.Hour), 2), // 14–16
		sample("c", base.AddDate(0, 0, 1), 2), // next day
	} {
		if err := st.Create(ctx, b, b.Start, b.End); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	got, err := st.InRange(ctx, "ellastcykel", base.Add(time.Hour), base.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("in range: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("got %d bookings %v, want just a", len(got), ids(got))
	}

	// Touching at the boundary is not an overlap.
	got, err = st.InRange(ctx, "ellastcykel", base.Add(2*time.Hour), base.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("in range: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("boundary-touching window returned %v, want none", ids(got))
	}

	// An empty resource id spans every resource.
	all, err := st.InRange(ctx, "", base.AddDate(0, 0, -1), base.AddDate(0, 0, 2))
	if err != nil {
		t.Fatalf("in range all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("got %d across all resources, want 3", len(all))
	}
}

func TestByMemberIsCaseInsensitiveAndSkipsCancelled(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	base := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)

	a := sample("a", base, 2)
	b := sample("b", base.Add(4*time.Hour), 2)
	b.MMUsername = "Anna.Andersson"
	for _, x := range []Booking{a, b} {
		if err := st.Create(ctx, x, x.Start, x.End); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	got, err := st.ByMember(ctx, " @Anna.Andersson ", false, now)
	if err != nil {
		t.Fatalf("by member: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want both bookings regardless of case", len(got))
	}

	if err := st.Cancel(ctx, "a", "tok-a", false, now); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ = st.ByMember(ctx, "anna.andersson", false, now)
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("cancelled bookings should be hidden, got %v", ids(got))
	}
	got, _ = st.ByMember(ctx, "anna.andersson", true, now)
	if len(got) != 2 {
		t.Errorf("includeCancelled should return both, got %v", ids(got))
	}
}

func TestHoursForUserBetweenClipsToWindow(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	b := sample("a", base, 4) // 10:00–14:00
	if err := st.Create(ctx, b, b.Start, b.End); err != nil {
		t.Fatalf("seed: %v", err)
	}

	full, err := st.HoursForUserBetween(ctx, "ellastcykel", "anna.andersson",
		base.Add(-time.Hour), base.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("hours: %v", err)
	}
	if full != 4 {
		t.Errorf("full window = %v h, want 4", full)
	}

	// A window covering only the middle two hours counts two.
	clipped, _ := st.HoursForUserBetween(ctx, "ellastcykel", "anna.andersson",
		base.Add(time.Hour), base.Add(3*time.Hour))
	if clipped != 2 {
		t.Errorf("clipped window = %v h, want 2", clipped)
	}
}

func TestSearchAndStats(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	past := sample("past", now.AddDate(0, 0, -5), 2)
	soon := sample("soon", now.AddDate(0, 0, 3), 2)
	soon.Name = "Bo Bengtsson"
	far := sample("far", now.AddDate(0, 0, 60), 2)
	for _, b := range []Booking{past, soon, far} {
		if err := st.Create(ctx, b, b.Start, b.End); err != nil {
			t.Fatalf("seed %s: %v", b.ID, err)
		}
	}
	if err := st.Cancel(ctx, "far", "tok-far", false, now); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	stats, err := st.Stats(ctx, now)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Total != 3 || stats.Upcoming != 1 || stats.Cancelled != 1 || stats.Next30 != 1 {
		t.Errorf("stats = %+v, want total 3 upcoming 1 cancelled 1 next30 1", stats)
	}

	hits, err := st.Search(ctx, Filter{Query: "bengtsson"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "soon" {
		t.Errorf("name search returned %v, want soon", ids(hits))
	}

	from := now
	hits, _ = st.Search(ctx, Filter{From: &from, Status: StatusConfirmed})
	if len(hits) != 1 || hits[0].ID != "soon" {
		t.Errorf("upcoming confirmed = %v, want soon", ids(hits))
	}
}

func ids(bs []Booking) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ID
	}
	return out
}

// A database written before the Mattermost switch must open and keep its
// history: the new columns are added empty rather than the old rows lost.
func TestOpenMigratesADatabaseFromBeforeMattermost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE bookings (
			id           TEXT PRIMARY KEY,
			resource_id  TEXT NOT NULL,
			start_utc    TEXT NOT NULL,
			end_utc      TEXT NOT NULL,
			mode         TEXT NOT NULL DEFAULT 'hours',
			name         TEXT NOT NULL,
			apartment    TEXT NOT NULL DEFAULT '',
			email        TEXT NOT NULL,
			phone        TEXT NOT NULL DEFAULT '',
			note         TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'confirmed',
			cancel_token TEXT NOT NULL,
			created_at   TEXT NOT NULL,
			cancelled_at TEXT,
			created_ip   TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO bookings (id, resource_id, start_utc, end_utc, mode, name, email,
			cancel_token, created_at)
		VALUES ('gammal', 'ellastcykel', '2026-05-02T10:00:00Z', '2026-05-02T12:00:00Z',
			'hours', 'Anna Andersson', 'anna@example.se', 'tok', '2026-05-01T09:00:00Z');`)
	if err != nil {
		t.Fatalf("write the old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open a database from before the switch: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	old, err := st.Get(ctx, "gammal")
	if err != nil {
		t.Fatalf("read the old booking: %v", err)
	}
	if old.Name != "Anna Andersson" || old.Email != "anna@example.se" {
		t.Errorf("the old booking came back as %+v", old)
	}
	if old.MMUsername != "" || old.MMUserID != "" {
		t.Errorf("an old booking cannot have a Mattermost account, got %q/%q",
			old.MMUsername, old.MMUserID)
	}

	// And the migrated database is fully usable.
	fresh := sample("ny", time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC), 2)
	if err := st.Create(ctx, fresh, fresh.Start, fresh.End); err != nil {
		t.Fatalf("create in a migrated database: %v", err)
	}
	mine, err := st.ByMember(ctx, "anna.andersson", false, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("by member: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != "ny" {
		t.Errorf("member lookup returned %v, want just the new booking", ids(mine))
	}

	// Opening twice must be a no-op, not a duplicate-column error.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	again.Close()
}

func TestCreateOverCancelsWhatItCoversAndKeepsTheRest(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)

	inside := sample("inside", base, 2)
	outside := sample("outside", base.AddDate(0, 0, 3), 2)
	for _, b := range []Booking{inside, outside} {
		if err := st.Create(ctx, b, b.Start, b.End); err != nil {
			t.Fatalf("seed %s: %v", b.ID, err)
		}
	}

	block := Booking{
		ID: "blk", ResourceID: "ellastcykel", Kind: KindBlock,
		Start: base.Add(-10 * time.Hour), End: base.Add(14 * time.Hour),
		Mode: "hours", Name: "Simon", MMUsername: "simm", Note: "Punktering",
		Status: StatusConfirmed, CancelToken: "tok", CreatedAt: base,
	}
	// Without the take-over it is an ordinary conflict.
	if err := st.Create(ctx, block, block.Start, block.End); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create over a booking = %v, want ErrConflict", err)
	}
	now := base.Add(-time.Hour)
	bumped, err := st.CreateOver(ctx, block, now)
	if err != nil {
		t.Fatalf("CreateOver: %v", err)
	}
	if len(bumped) != 1 || bumped[0].ID != "inside" || bumped[0].Status != StatusCancelled {
		t.Fatalf("bumped = %+v, want just the booking inside the block, cancelled", bumped)
	}

	got, err := st.Get(ctx, "inside")
	if err != nil || got.Status != StatusCancelled || !got.CancelledAt.Valid || !got.CancelledAt.Time.Equal(now) {
		t.Errorf("the covered booking should be cancelled at %v, got %+v (err %v)", now, got, err)
	}
	if got, _ := st.Get(ctx, "outside"); got.Status != StatusConfirmed {
		t.Errorf("the booking outside the block should be untouched, got %s", got.Status)
	}
	if got, _ := st.Get(ctx, "blk"); !got.IsBlock() || got.Note != "Punktering" {
		t.Errorf("the block should come back as a block with its reason, got %+v", got)
	}

	// Blocks are not bookings: neither cap counts them, and the filter can
	// pick either kind.
	if n, _ := st.CountActiveForUser(ctx, "ellastcykel", "simm", now); n != 0 {
		t.Errorf("a block counted towards the active cap: %d", n)
	}
	blocks, _ := st.Search(ctx, Filter{Kind: KindBlock})
	bookings, _ := st.Search(ctx, Filter{Kind: KindBooking, Status: StatusConfirmed})
	if len(blocks) != 1 || len(bookings) != 1 {
		t.Errorf("filter by kind: %d blocks, %d confirmed bookings; want 1 and 1", len(blocks), len(bookings))
	}
}

func TestRowsFromBeforeTheKindColumnReadAsBookings(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	b := sample("old", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC), 1)
	if err := st.Create(ctx, b, b.Start, b.End); err != nil {
		t.Fatal(err)
	}
	// Kind was left empty on the way in, as every caller from before did.
	got, err := st.Get(ctx, "old")
	if err != nil || got.Kind != KindBooking || got.IsBlock() {
		t.Errorf("an unmarked row should read as a booking, got kind %q (err %v)", got.Kind, err)
	}
}
