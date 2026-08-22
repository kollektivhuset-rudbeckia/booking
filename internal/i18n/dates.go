package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Swedish writes weekdays and months in lower case and dates as "18 augusti";
// English capitalises them and says "18 August". Keeping both here means a
// page never mixes the two conventions.
var weekdays = map[Lang][7]string{
	SV: {"söndag", "måndag", "tisdag", "onsdag", "torsdag", "fredag", "lördag"},
	EN: {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
}

var weekdaysShort = map[Lang][7]string{
	SV: {"sön", "mån", "tis", "ons", "tor", "fre", "lör"},
	EN: {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
}

var months = map[Lang][12]string{
	SV: {"januari", "februari", "mars", "april", "maj", "juni",
		"juli", "augusti", "september", "oktober", "november", "december"},
	EN: {"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"},
}

var monthsShort = map[Lang][12]string{
	SV: {"jan", "feb", "mar", "apr", "maj", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},
	EN: {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
}

// lang narrows anything unexpected to a language we have tables for.
func lang(l Lang) Lang {
	if l == EN {
		return EN
	}
	return SV
}

// Weekday returns "måndag" or "Monday".
func Weekday(l Lang, t time.Time) string { return weekdays[lang(l)][int(t.Weekday())] }

// WeekdayShort returns "mån" or "Mon", for the day strip.
func WeekdayShort(l Lang, t time.Time) string { return weekdaysShort[lang(l)][int(t.Weekday())] }

// WeekdayInitials are the seven column headings of the month calendar,
// Monday first.
func WeekdayInitials(l Lang) []string {
	src := weekdaysShort[lang(l)]
	return []string{src[1], src[2], src[3], src[4], src[5], src[6], src[0]}
}

// Month returns "augusti" or "August".
func Month(l Lang, t time.Time) string { return months[lang(l)][int(t.Month())-1] }

// MonthShort returns "aug" or "Aug".
func MonthShort(l Lang, t time.Time) string { return monthsShort[lang(l)][int(t.Month())-1] }

// MonthYear renders "augusti 2026" or "August 2026", for the calendar heading.
func MonthYear(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d", Month(l, t), t.Year())
}

// DateLong renders "måndag 18 augusti" or "Monday 18 August".
func DateLong(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d %s", Weekday(l, t), t.Day(), Month(l, t))
}

// DateLongYear adds the year.
func DateLongYear(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", Weekday(l, t), t.Day(), Month(l, t), t.Year())
}

// DateShort renders "18 aug" or "18 Aug".
func DateShort(l Lang, t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), MonthShort(l, t))
}

// Clock renders "13:00". Both languages use the 24-hour clock: the house is in
// Sweden, and a bike booked "from 1" is not how anybody here writes it.
func Clock(t time.Time) string { return t.Format("15:04") }

// ISODate renders "2026-08-18", for URLs and date inputs rather than for
// reading, and is the same in every language.
func ISODate(t time.Time) string { return t.Format("2006-01-02") }

// Interval renders a booking as compactly as it can: within one day as
// "måndag 18 augusti 13:00–15:00", across days as "18 aug 15:00 – 21 aug 12:00".
func Interval(l Lang, start, end time.Time) string {
	if ISODate(start) == ISODate(end) {
		return fmt.Sprintf("%s %s–%s", DateLong(l, start), Clock(start), Clock(end))
	}
	return fmt.Sprintf("%s %s – %s %s", DateShort(l, start), Clock(start), DateShort(l, end), Clock(end))
}

// RelativeDay returns "idag", "imorgon" or the weekday name — the way a
// neighbour would say it rather than a date.
func RelativeDay(l Lang, t, now time.Time) string {
	switch daysBetween(now, t) {
	case 0:
		return T(l, "day.today")
	case 1:
		return T(l, "day.tomorrow")
	case 2:
		return T(l, "day.dayafter")
	}
	return Weekday(l, t)
}

func daysBetween(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	b := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location())
	return int(b.Sub(a).Hours()/24 + 0.5)
}

// TitleCase upper-cases the first rune, for a Swedish weekday starting a
// sentence. English weekdays are already capitalised, so this is a no-op there.
func TitleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// Count renders "3 nätter" or "3 nights". The unit names a pair of rows in the
// catalogue, which holds the singular and the plural of each language.
func Count(l Lang, unit string, n int) string {
	return fmt.Sprintf("%d %s", n, Plural(l, unit, n))
}

// Plural returns just the noun, for a sentence that already has the number.
func Plural(l Lang, unit string, n int) string {
	key := "unit." + unit + ".many"
	if n == 1 {
		key = "unit." + unit + ".one"
	}
	return T(l, key)
}

// Duration renders a length the way the house talks about it: "4 h", "30 min",
// "1 h 30 min". The units read the same in both languages.
func Duration(d time.Duration) string {
	mins := int(d.Minutes())
	h, m := mins/60, mins%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	default:
		return fmt.Sprintf("%d h %d min", h, m)
	}
}

// DurationList renders the offered lengths as "1 h, 2 h, 4 h eller 8 h".
func DurationList(l Lang, hours []float64) string {
	parts := make([]string, len(hours))
	for i, h := range hours {
		parts[i] = Duration(time.Duration(h * float64(time.Hour)))
	}
	return JoinOr(l, parts)
}

// JoinOr renders a list as "a, b eller c" / "a, b or c".
func JoinOr(l Lang, parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " " + T(l, "or") + " " + parts[len(parts)-1]
}

// JoinAnd renders a list as "a, b och c" / "a, b and c".
func JoinAnd(l Lang, parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " " + T(l, "and") + " " + parts[len(parts)-1]
}

// Days renders a booking horizon in the largest unit that comes out even:
// 90 days is "3 månader", 365 is "1 år", 45 stays "45 dagar".
func Days(l Lang, d int) string {
	switch {
	case d > 0 && d%365 == 0:
		return Count(l, "year", d/365)
	case d > 0 && d%30 == 0:
		return Count(l, "month", d/30)
	default:
		return Count(l, "day", d)
	}
}
