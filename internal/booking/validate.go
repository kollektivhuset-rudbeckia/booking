package booking

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// Request is a booking attempt as it arrives from the form.
type Request struct {
	Resource  config.Resource
	Start     time.Time
	End       time.Time
	Name      string
	Apartment string
	// MMUsername identifies the member, and every per-member cap counts
	// against it. MMUserID is where the confirmation is sent.
	MMUsername string
	MMUserID   string
	Email      string
	Phone      string
	Note       string
}

// Error is a validation failure phrased for the member reading the page, in
// the language they are reading it in.
type Error struct {
	Field   string
	Message string
}

func (e Error) Error() string { return e.Message }

// Validate checks a request against the resource rules and the current
// contents of the database. It returns the interval that must be free,
// buffer included, so the caller can hand it straight to store.Create.
func Validate(ctx context.Context, st *store.Store, req Request, now time.Time, loc *time.Location, lang i18n.Lang) (blockFrom, blockTo time.Time, errs []Error) {
	r := req.Resource
	ru := r.Rules
	name := r.NameFor(string(lang))

	if !r.Active() {
		return blockFrom, blockTo, []Error{{Message: i18n.T(lang, "error.notbookable", name)}}
	}
	if strings.TrimSpace(req.Name) == "" {
		errs = append(errs, Error{Field: "name", Message: "Fyll i ditt namn."})
	}
	if store.Member(req.MMUsername) == "" {
		errs = append(errs, Error{Field: "member", Message: i18n.T(lang, "member.choose")})
	}
	if !req.End.After(req.Start) {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.endbeforestart")})
		return blockFrom, blockTo, errs
	}
	if req.Start.Before(now.Add(time.Duration(ru.MinNoticeMinutes) * time.Minute)) {
		errs = append(errs, Error{Field: "time", Message: "Tiden har redan passerat."})
	}
	if req.Start.After(now.AddDate(0, 0, ru.MaxAdvanceDays)) {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.toofar", i18n.Days(lang, ru.MaxAdvanceDays))})
	}

	switch ru.Mode {
	case config.ModeHours:
		errs = append(errs, validateHours(r, req, loc, lang)...)
	case config.ModeDays:
		errs = append(errs, validateDays(r, req, loc, lang)...)
	}

	if len(errs) > 0 {
		return blockFrom, blockTo, errs
	}

	// Per-member caps.
	if ru.MaxActivePerUser > 0 {
		n, err := st.CountActiveForUser(ctx, r.ID, req.MMUsername, now)
		if err != nil {
			return blockFrom, blockTo, []Error{{Message: i18n.T(lang, "error.readfailed")}}
		}
		if n >= ru.MaxActivePerUser {
			errs = append(errs, Error{Message: i18n.T(lang, "error.toomany", n, name, ru.MaxActivePerUser)})
		}
	}
	if ru.MaxHoursPerWeekPerUser > 0 {
		weekFrom := req.Start.AddDate(0, 0, -7)
		weekTo := req.Start.AddDate(0, 0, 7)
		used, err := st.HoursForUserBetween(ctx, r.ID, req.MMUsername, weekFrom, weekTo)
		if err != nil {
			return blockFrom, blockTo, []Error{{Message: i18n.T(lang, "error.readfailed")}}
		}
		want := req.End.Sub(req.Start).Hours()
		if used+want > ru.MaxHoursPerWeekPerUser {
			errs = append(errs, Error{Message: i18n.T(lang, "error.weekhours",
				used+want, ru.MaxHoursPerWeekPerUser, name)})
		}
	}
	if len(errs) > 0 {
		return blockFrom, blockTo, errs
	}

	// The interval that must be free is the booking widened by the buffer.
	buffer := time.Duration(ru.BufferMinutes) * time.Minute
	blockFrom = req.Start.Add(-buffer)
	blockTo = req.End.Add(buffer)

	// A cheap pre-check so the member gets a nice message rather than a
	// conflict error; store.Create repeats it inside a transaction.
	existing, err := st.InRange(ctx, r.ID, blockFrom, blockTo)
	if err != nil {
		return blockFrom, blockTo, []Error{{Message: i18n.T(lang, "error.readexisting")}}
	}
	if _, hit := blocked(req.Start, req.End, buffer, existing); hit {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.justtaken")})
	}
	return blockFrom, blockTo, errs
}

func validateHours(r config.Resource, req Request, loc *time.Location, lang i18n.Lang) []Error {
	ru := r.Rules
	var errs []Error

	dur := req.End.Sub(req.Start)
	if err := CheckDuration(r, dur, lang); err != nil {
		errs = append(errs, *err)
	}

	local := req.Start.In(loc)
	openFrom, openTo := dayWindow(r, local, loc)
	if local.Before(openFrom) || req.End.In(loc).After(openTo) {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.openhours",
			r.NameFor(string(lang)), ru.OpenFrom, ru.OpenTo)})
	}

	step := time.Duration(ru.SlotStepMinutes) * time.Minute
	if offset := local.Sub(openFrom) % step; offset != 0 {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.slotgrid", ru.SlotStepMinutes)})
	}
	return errs
}

func validateDays(r config.Resource, req Request, loc *time.Location, lang i18n.Lang) []Error {
	ru := r.Rules
	var errs []Error

	// Count nights from calendar dates so DST transitions cannot shift the total.
	s, e := req.Start.In(loc), req.End.In(loc)
	sd := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
	ed := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
	nights := int(ed.Sub(sd).Hours()/24 + 0.5)

	if nights < ru.MinDays {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.shortest",
			i18n.Count(lang, "night", ru.MinDays))})
	}
	if nights > ru.MaxDays {
		errs = append(errs, Error{Field: "time", Message: i18n.T(lang, "error.longest",
			i18n.Count(lang, "night", ru.MaxDays))})
	}
	return errs
}

// CheckDuration reports whether a length is bookable for a resource: one of
// the offered choices, or — when the resource allows it — any typed-in length
// that fits the configured bounds and the slot grid.
func CheckDuration(r config.Resource, dur time.Duration, lang i18n.Lang) *Error {
	ru := r.Rules
	for _, d := range ru.Durations {
		if durationOf(d) == dur {
			return nil
		}
	}
	if !ru.CustomDuration {
		return &Error{Field: "duration", Message: i18n.T(lang, "error.pickduration",
			i18n.DurationList(lang, ru.Durations))}
	}

	min := time.Duration(ru.MinDurationMinutes) * time.Minute
	max := time.Duration(ru.MaxDurationMinutes) * time.Minute
	step := time.Duration(ru.SlotStepMinutes) * time.Minute

	switch {
	case dur < min:
		return &Error{Field: "duration", Message: i18n.T(lang, "error.shortest", i18n.Duration(min))}
	case dur > max:
		return &Error{Field: "duration", Message: i18n.T(lang, "error.longest", i18n.Duration(max))}
	case dur%step != 0:
		return &Error{Field: "duration", Message: i18n.T(lang, "error.stepduration",
			i18n.Duration(step),
			i18n.Duration(dur/step*step),
			i18n.Duration((dur/step+1)*step))}
	}
	return nil
}

// ParseHours reads a typed-in length in hours. It accepts both "2.5" and the
// Swedish "2,5" whatever language the page is in — a Swedish keyboard types a
// comma however the site is set — and rounds to whole minutes so floating
// point dust can never turn 2.5 h into 2 h 29 min 59 s.
func ParseHours(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.Replace(s, ",", ".", 1))
	if s == "" {
		return 0, ErrNoLength
	}
	hours, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, ErrNotANumber
	}
	if hours <= 0 {
		return 0, ErrNotPositive
	}
	return time.Duration(math.Round(hours*60)) * time.Minute, nil
}

// The ways a typed-in length can be unreadable. They are values rather than
// sentences because the sentence belongs to whichever language the member is
// reading; the web layer turns each of these into one.
var (
	ErrNoLength    = errors.New("no length given")
	ErrNotANumber  = errors.New("not a number")
	ErrNotPositive = errors.New("length must be greater than zero")
)

// HoursParam renders a duration as the "langd" query parameter: hours, with a
// dot, and no floating point dust. ParseHours reads it back exactly.
func HoursParam(d time.Duration) string {
	mins := int(d.Minutes())
	if mins%60 == 0 {
		return strconv.Itoa(mins / 60)
	}
	return strconv.FormatFloat(float64(mins)/60, 'f', -1, 64)
}

// FormatHoursInput renders a duration the way it should appear in the "own
// length" field: "2" rather than "2.0", and "2,5" with a Swedish comma.
func FormatHoursInput(d time.Duration) string {
	hours := d.Minutes() / 60
	if hours == math.Trunc(hours) {
		return strconv.FormatFloat(hours, 'f', 0, 64)
	}
	return strings.Replace(strconv.FormatFloat(hours, 'f', -1, 64), ".", ",", 1)
}

// IsPreset reports whether a length is one of the one-click choices.
func IsPreset(r config.Resource, dur time.Duration) bool {
	for _, d := range r.Rules.Durations {
		if durationOf(d) == dur {
			return true
		}
	}
	return false
}

func durationOf(hours float64) time.Duration {
	return time.Duration(hours * float64(time.Hour))
}

// Durations converts the configured hour values into durations.
func Durations(ds []float64) []time.Duration {
	out := make([]time.Duration, len(ds))
	for i, d := range ds {
		out[i] = durationOf(d)
	}
	return out
}
