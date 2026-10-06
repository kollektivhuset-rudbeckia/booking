package web

import (
	"net/http"
	"sort"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// regularOver is how many bookings it takes to get into the regulars' club:
// strictly more than this.
const regularOver = 5

// leaderboardSize is how many names the top list shows.
const leaderboardSize = 10

// statsPage is everything the statistics page shows. It is vanity: nothing
// here decides anything, it is just the house's bookings counted for fun.
type statsPage struct {
	Bookings   int
	Hours      float64
	Nights     int
	Members    int
	Cancelled  int
	CancelRate int

	// RegularOver is the bar for the regulars' club, for the page to say.
	RegularOver int

	Leaders   []bookerStat
	Regulars  []bookerStat
	Resources []bar
	Weekdays  []bar
	StartHour []bar
	Facts     []fact
}

// bookerStat is one member's tally.
type bookerStat struct {
	Rank      int
	Medal     string
	Name      string
	Username  string
	Count     int
	Hours     float64
	Nights    int
	Favourite config.Resource
	byRes     map[string]int
}

// bar is one row or column of a chart. Pct is relative to the largest bar, so
// the winner always reaches all the way across.
type bar struct {
	Label string
	Emoji string
	Count int
	Pct   float64
	Top   bool
}

// fact is one sentence of trivia, already in the reader's language.
type fact struct {
	Emoji string
	Text  string
}

// handleStats shows the house's bookings in numbers. It is open to every
// member: the names on it are the same ones the coming-bookings list shows.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, v *view) {
	list, err := s.store.Search(r.Context(), store.Filter{Ascending: true})
	if err != nil {
		s.log.Error("load stats", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.tryagain")
		return
	}
	v.Title = i18n.T(v.Lang, "stats.title")
	v.Data = s.buildStats(v.Lang, list, s.cfg.Location())
	s.render(w, r, http.StatusOK, "stats.html", v)
}

// buildStats counts the bookings. list must be in start order: where two
// bookings tie for a record, the earlier one keeps it.
func (s *Server) buildStats(lang i18n.Lang, list []store.Booking, loc *time.Location) statsPage {
	p := statsPage{RegularOver: regularOver}
	l := string(lang)
	resource := func(id string) config.Resource {
		if res, ok := s.cfg.Resource(id); ok {
			return res
		}
		return config.Resource{ID: id, Name: id}
	}

	people := map[string]*bookerStat{}
	perRes := map[string]int{}
	var weekdays [7]int
	var hours [24]int
	perDay := map[string]int{}
	var (
		longest, early, late, planner, first *store.Booking
		longestLen, plannerLead              time.Duration
		earlyMin, lateMin                    = 24 * 60, -1
		spontaneous                          int
	)

	for i := range list {
		b := &list[i]
		if !b.Active() {
			p.Cancelled++
			continue
		}
		p.Bookings++
		start, end := b.Start.In(loc), b.End.In(loc)
		days := b.Mode == string(config.ModeDays)

		// Bookings from before the Mattermost switch have no username; the
		// name they were made under is the best identity they have.
		key := b.MMUsername
		if key == "" {
			key = store.Member(b.Name)
		}
		who, ok := people[key]
		if !ok {
			who = &bookerStat{Username: b.MMUsername, byRes: map[string]int{}}
			people[key] = who
		}
		// Later bookings win, so a member who changed their display name is
		// shown under the one they use now.
		who.Name = b.Name
		who.Count++
		who.byRes[b.ResourceID]++
		perRes[b.ResourceID]++

		if days {
			n := nightsBetween(*b, loc)
			who.Nights += n
			p.Nights += n
		} else {
			h := end.Sub(start).Hours()
			who.Hours += h
			p.Hours += h
			hours[start.Hour()]++

			if m := start.Hour()*60 + start.Minute(); m < earlyMin {
				earlyMin, early = m, b
			}
			// A booking that ends at midnight ran to the very end of its day.
			m := end.Hour()*60 + end.Minute()
			if m == 0 && !end.Equal(start) {
				m = 24 * 60
			}
			if m > lateMin {
				lateMin, late = m, b
			}
		}
		// Monday first, as the house's calendar has it.
		weekdays[(int(start.Weekday())+6)%7]++
		perDay[i18n.ISODate(start)]++

		if d := b.End.Sub(b.Start); d > longestLen {
			longestLen, longest = d, b
		}
		lead := b.Start.Sub(b.CreatedAt)
		if lead > plannerLead {
			plannerLead, planner = lead, b
		}
		if lead >= 0 && lead < time.Hour {
			spontaneous++
		}
		if first == nil || b.CreatedAt.Before(first.CreatedAt) {
			first = b
		}
	}
	p.Members = len(people)
	if total := p.Bookings + p.Cancelled; total > 0 {
		p.CancelRate = int(float64(p.Cancelled)/float64(total)*100 + 0.5)
	}
	if p.Bookings == 0 {
		return p
	}

	// The leaderboard, most bookings first. More hours breaks a tie, then the
	// name, so the order is the same on every visit.
	ranked := make([]*bookerStat, 0, len(people))
	for _, who := range people {
		ranked = append(ranked, who)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Hours != b.Hours {
			return a.Hours > b.Hours
		}
		return a.Name < b.Name
	})
	var bestFriend *bookerStat
	var bestFriendRes string
	medals := []string{"🥇", "🥈", "🥉"}
	for i, who := range ranked {
		// Equal counts share a place, the way a sports table does it.
		who.Rank = i + 1
		if i > 0 && who.Count == ranked[i-1].Count {
			who.Rank = ranked[i-1].Rank
		}
		if who.Rank <= len(medals) {
			who.Medal = medals[who.Rank-1]
		}
		fav, favN := "", 0
		for id, n := range who.byRes {
			if n > favN || (n == favN && id < fav) {
				fav, favN = id, n
			}
		}
		who.Favourite = resource(fav)
		if bestFriend == nil || favN > bestFriend.byRes[bestFriendRes] {
			bestFriend, bestFriendRes = who, fav
		}
		if i < leaderboardSize {
			p.Leaders = append(p.Leaders, *who)
		}
		if who.Count > regularOver {
			p.Regulars = append(p.Regulars, *who)
		}
	}

	// Every resource in the configured order, then anything that has since
	// been removed from the configuration but still has history.
	var resBars []bar
	seen := map[string]bool{}
	for _, res := range s.cfg.Resources {
		seen[res.ID] = true
		if perRes[res.ID] == 0 && !res.Active() {
			continue
		}
		resBars = append(resBars, bar{Label: res.NameFor(l), Emoji: res.Emoji, Count: perRes[res.ID]})
	}
	for id, n := range perRes {
		if !seen[id] {
			resBars = append(resBars, bar{Label: id, Count: n})
		}
	}
	sort.SliceStable(resBars, func(i, j int) bool { return resBars[i].Count > resBars[j].Count })
	p.Resources = scaleBars(resBars)

	// Any Monday will do to name the days; this one is in January 2024.
	monday := time.Date(2024, 1, 1, 12, 0, 0, 0, loc)
	dayBars := make([]bar, 7)
	for i := range dayBars {
		dayBars[i] = bar{Label: i18n.TitleCase(i18n.WeekdayShort(lang, monday.AddDate(0, 0, i))), Count: weekdays[i]}
	}
	p.Weekdays = scaleBars(dayBars)

	// The hours of the day, trimmed to the span anything has started in.
	lo, hi := -1, -1
	for h, n := range hours {
		if n > 0 {
			if lo < 0 {
				lo = h
			}
			hi = h
		}
	}
	if lo >= 0 {
		hourBars := make([]bar, 0, hi-lo+1)
		for h := lo; h <= hi; h++ {
			hourBars = append(hourBars, bar{Label: time.Date(2024, 1, 1, h, 0, 0, 0, loc).Format("15"), Count: hours[h]})
		}
		p.StartHour = scaleBars(hourBars)
	}

	// --- the trivia ----------------------------------------------------------
	name := func(b *store.Booking) string { return b.Name }
	resName := func(id string) string { return resource(id).NameFor(l) }
	add := func(emoji, text string) { p.Facts = append(p.Facts, fact{Emoji: emoji, Text: text}) }

	if top := p.Resources[0]; top.Count > 0 {
		add("🏆", i18n.T(lang, "stats.fact.popular", top.Label, i18n.Count(lang, "occasion", top.Count)))
	}
	if top := topBar(p.Weekdays); top >= 0 {
		add("📅", i18n.T(lang, "stats.fact.weekday",
			i18n.TitleCase(i18n.Weekday(lang, monday.AddDate(0, 0, top)))))
	}
	if day, n := busiestDay(perDay); n > 1 {
		t, _ := time.ParseInLocation("2006-01-02", day, loc)
		add("🎉", i18n.T(lang, "stats.fact.busiestday", i18n.DateLongYear(lang, t), i18n.Count(lang, "booking", n)))
	}
	if longest != nil {
		length := i18n.Duration(longestLen)
		if longest.Mode == string(config.ModeDays) {
			length = i18n.Count(lang, "night", nightsBetween(*longest, loc))
		}
		add("⏳", i18n.T(lang, "stats.fact.longest", name(longest), resName(longest.ResourceID), length))
	}
	if early != nil {
		add("🐦", i18n.T(lang, "stats.fact.early", name(early), resName(early.ResourceID), i18n.Clock(early.Start.In(loc))))
	}
	if late != nil && late != early {
		add("🦉", i18n.T(lang, "stats.fact.late", name(late), resName(late.ResourceID), i18n.Clock(late.End.In(loc))))
	}
	if planner != nil && plannerLead >= 48*time.Hour {
		add("🗓️", i18n.T(lang, "stats.fact.planner", name(planner), resName(planner.ResourceID),
			i18n.Count(lang, "day", int(plannerLead.Hours()/24))))
	}
	if spontaneous > 1 {
		add("⚡", i18n.T(lang, "stats.fact.spontaneous", spontaneous))
	}
	if bestFriend != nil && bestFriend.byRes[bestFriendRes] >= 3 {
		add("💛", i18n.T(lang, "stats.fact.bestfriends", bestFriend.Name, resName(bestFriendRes),
			i18n.Count(lang, "occasion", bestFriend.byRes[bestFriendRes])))
	}
	if first != nil {
		add("🌱", i18n.T(lang, "stats.fact.first", name(first), i18n.DateLongYear(lang, first.CreatedAt.In(loc))))
	}
	return p
}

// scaleBars sets each bar's length against the longest and marks the winner.
func scaleBars(bars []bar) []bar {
	top := topBar(bars)
	if top < 0 {
		return bars
	}
	max := bars[top].Count
	for i := range bars {
		bars[i].Pct = float64(bars[i].Count) / float64(max) * 100
		bars[i].Top = bars[i].Count == max
	}
	return bars
}

// topBar is the index of the first longest bar, or -1 if they are all empty.
func topBar(bars []bar) int {
	top := -1
	for i, b := range bars {
		if b.Count > 0 && (top < 0 || b.Count > bars[top].Count) {
			top = i
		}
	}
	return top
}

// busiestDay is the date with the most bookings starting on it. The earliest
// date wins a tie.
func busiestDay(perDay map[string]int) (string, int) {
	best, bestN := "", 0
	for day, n := range perDay {
		if n > bestN || (n == bestN && day < best) {
			best, bestN = day, n
		}
	}
	return best, bestN
}
