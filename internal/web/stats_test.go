package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// book stores a confirmed hourly booking for a named member.
func (h *harness) book(name, resource string, start time.Time, length time.Duration) store.Booking {
	h.Helper()
	b := store.Booking{
		ID:          fmt.Sprintf("%s-%s-%s", resource, name, start.Format("0102-1504")),
		ResourceID:  resource,
		Start:       start,
		End:         start.Add(length),
		Mode:        "hours",
		Name:        name,
		MMUsername:  directoryUsername(name),
		Status:      store.StatusConfirmed,
		CancelToken: "t",
		CreatedAt:   h.now,
	}
	if err := h.store.Create(context.Background(), b, b.Start, b.End); err != nil {
		h.Fatalf("book %s for %s: %v", resource, name, err)
	}
	return b
}

func TestStatsCrownsTheTopBookerAndTheRegulars(t *testing.T) {
	h := newHarness(t)
	// Anna books six times and gets into the club; Bo books five, which is
	// not more than five, and stays out.
	for day := 1; day <= 6; day++ {
		h.book("Anna Andersson", "ellastcykel", h.at(day, 9, 0), 2*time.Hour)
	}
	for day := 1; day <= 5; day++ {
		h.book("Bo Bengtsson", "elcykel", h.at(day, 14, 0), time.Hour)
	}
	cancelled := h.book("Cecilia Dahl", "elcykel", h.at(1, 18, 0), time.Hour)
	if err := h.store.Cancel(context.Background(), cancelled.ID, "", true, h.now); err != nil {
		t.Fatal(err)
	}

	rec := h.do("GET", "/statistik", nil, h.login("husets-losenord"))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats = %d", rec.Code)
	}
	body := rec.Body.String()

	board := between(body, `class="leaderboard"`, "</ol>")
	anna, bo := strings.Index(board, "Anna Andersson"), strings.Index(board, "Bo Bengtsson")
	if anna < 0 || bo < 0 || anna > bo {
		t.Errorf("Anna should top the board above Bo:\n%s", board)
	}
	if !strings.Contains(board, "🥇") || !strings.Contains(board, "6 bokningar") {
		t.Errorf("the winner should have the gold medal and six bookings:\n%s", board)
	}
	if strings.Contains(board, "Cecilia") {
		t.Error("a cancelled booking should not put anyone on the board")
	}

	club := between(body, `class="regulars"`, "</ul>")
	if !strings.Contains(club, "Anna Andersson") {
		t.Error("six bookings is more than five, so Anna is a regular")
	}
	if strings.Contains(club, "Bo Bengtsson") {
		t.Error("five bookings is not more than five, so Bo is not a regular yet")
	}

	if !strings.Contains(body, "<strong>11</strong>") {
		t.Error("the tile should count the eleven confirmed bookings")
	}
	if !strings.Contains(body, "<strong>8 %</strong>") {
		t.Error("one of twelve bookings cancelled is 8 %")
	}
	if !strings.Contains(body, "Ellastcykeln är husets favorit – bokad 6 gånger.") {
		t.Errorf("expected the favourite resource among the facts:\n%s", between(body, `class="trivia"`, "</ul>"))
	}
	if !strings.Contains(body, "Morgonpigg: Anna Andersson") {
		t.Error("Anna's nine o'clock start is the earliest")
	}
	if !strings.Contains(body, "Bästa vänner: Anna Andersson och Ellastcykeln") {
		t.Error("Anna and the cargo bike are the most loyal pair")
	}
}

func TestStatsIsFriendlyWithNoBookings(t *testing.T) {
	h := newHarness(t)
	body := h.do("GET", "/statistik", nil, h.login("husets-losenord")).Body.String()
	if !strings.Contains(body, "Inget är bokat än") {
		t.Error("an empty house should say there is nothing to count")
	}
	if strings.Contains(body, `class="leaderboard"`) {
		t.Error("there is no leaderboard without bookings")
	}
}

func TestStatsSpeaksEnglish(t *testing.T) {
	h := newHarness(t)
	h.book("Anna Andersson", "ellastcykel", h.at(1, 9, 0), 2*time.Hour)
	member := h.login("husets-losenord")
	rec := h.do("POST", "/sprak", url.Values{"lang": {"en"}, "next": {"/statistik"}}, member)
	cookies := append(member, rec.Result().Cookies()...)

	body := h.do("GET", "/statistik", nil, cookies).Body.String()
	for _, want := range []string{"The keenest bookers", "1 booking", "Nobody has booked more than 5 times yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("the English page should say %q", want)
		}
	}
}

func TestStatsIsBehindThePasswordAndInTheMenu(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/statistik", nil, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous stats = %d, want a redirect to the login", rec.Code)
	}
	body := h.do("GET", "/", nil, h.login("husets-losenord")).Body.String()
	if !strings.Contains(body, `href="/statistik"`) {
		t.Error("the menu should link to the statistics")
	}
}
