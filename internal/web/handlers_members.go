package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/mattermost"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// memberCacheTTL is how long the directory listing is reused. The browser asks
// for the whole list every time the booking form is opened, and the house does
// not gain a new member between two of those.
const memberCacheTTL = 5 * time.Minute

// memberSuggestion is one row in the booking form's user picker. Name is what
// the member reads; Username is what the form submits.
type memberSuggestion struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// memberList is the picker's whole world: everyone who may book. Truncated
// says the house is too big to send at once, so the browser should ask the
// server to search instead of indexing the list itself.
type memberList struct {
	Users     []memberSuggestion `json:"users"`
	Truncated bool               `json:"truncated"`
}

// memberCache holds the directory between requests.
type memberCache struct {
	mu   sync.Mutex
	list memberList
	at   time.Time
}

// handleMemberSearch answers the booking form's user lookups. Without a query
// it returns everyone who may book, which the browser indexes and searches as
// you type; with ?q= it searches server-side, which is the fallback for a
// directory too large to send.
//
// It is behind the member gate like every other page, and it only ever offers
// people who are actually allowed to book, so the picker cannot suggest
// someone the form would then refuse.
func (s *Server) handleMemberSearch(w http.ResponseWriter, r *http.Request, v *view) {
	var (
		out memberList
		err error
	)
	if term := strings.TrimSpace(r.URL.Query().Get("q")); term != "" {
		out, err = s.searchMembers(r.Context(), term)
	} else {
		out, err = s.memberDirectory(r.Context())
	}
	if err != nil {
		s.log.Error("mattermost directory", "err", err)
		http.Error(w, `{"error":"could not read the Mattermost directory"}`, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Error("encode member list", "err", err)
	}
}

// searchMembers asks Mattermost to search, for a term of at least two letters.
func (s *Server) searchMembers(ctx context.Context, term string) (memberList, error) {
	out := memberList{Users: []memberSuggestion{}}
	if len([]rune(term)) < 2 {
		return out, nil
	}
	users, err := s.mm.Search(ctx, term)
	if err != nil {
		return out, err
	}
	out.Users = s.suggestions(users)
	return out, nil
}

// memberDirectory returns everyone who may book, from a short-lived cache.
//
// With an allow list there is nothing to list: the names are already known, so
// each is looked up directly instead of paging through the whole server.
func (s *Server) memberDirectory(ctx context.Context) (memberList, error) {
	s.members.mu.Lock()
	defer s.members.mu.Unlock()
	if !s.members.at.IsZero() && s.now().Sub(s.members.at) < memberCacheTTL {
		return s.members.list, nil
	}

	var (
		users     []mattermost.User
		truncated bool
	)
	if allow := s.rt.Mattermost.Allow; len(allow) > 0 {
		for _, username := range allow {
			u, err := s.mm.ByUsername(ctx, username)
			if err != nil {
				// One name in the allow list not being an account is a
				// configuration mistake, not a reason to offer nobody.
				s.log.Warn("allowed member is not a Mattermost account",
					"username", username, "err", err)
				continue
			}
			users = append(users, u)
		}
	} else {
		var err error
		users, truncated, err = s.mm.Directory(ctx)
		if err != nil {
			return memberList{Users: []memberSuggestion{}}, err
		}
		if truncated {
			s.log.Warn("mattermost directory is larger than the picker holds; "+
				"falling back to searching on the server",
				"listed", len(users), "limit", mattermost.DirectoryLimit)
		}
	}

	list := memberList{Users: s.suggestions(users), Truncated: truncated}
	s.members.list, s.members.at = list, s.now()
	return list, nil
}

// suggestions drops everyone who may not book and sorts what is left by the
// name the member reads, so an unfiltered list is already in a sensible order.
func (s *Server) suggestions(users []mattermost.User) []memberSuggestion {
	out := make([]memberSuggestion, 0, len(users))
	for _, u := range users {
		if !u.Active() || !s.rt.Mattermost.Allowed(u.Username) {
			continue
		}
		out = append(out, memberSuggestion{Username: u.Username, Name: u.DisplayName()})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := mattermost.Fold(out[i].Name), mattermost.Fold(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Username < out[j].Username
	})
	return out
}

// resolveMember turns what the booking form posted into a Mattermost account,
// or into the error the member should read. It is also where the allow list is
// enforced: while the bot is being tried out, only the listed people may book,
// so only they are candidates.
func (s *Server) resolveMember(ctx context.Context, lang i18n.Lang, typed string) (mattermost.User, []booking.Error) {
	u, errs := s.findMember(ctx, lang, typed, true)
	if len(errs) > 0 {
		return mattermost.User{}, errs
	}
	return s.allowed(lang, u)
}

// findMember turns what someone typed into one account. The field is a plain
// text input, so it has to cope with everything a person might reasonably
// leave in it: a username picked from the list, "@anna.andersson" pasted from
// a message, a full name, a nickname, or just "Anna" because that is all they
// know. Anything that points at exactly one person resolves to that person;
// anything that points at several says who they are, so the next keystroke
// settles it. Only a term that matches nobody is an error.
//
// onlyAllowed narrows the candidates to those who may book. The booking form
// wants that — it must not resolve to someone it would then refuse — while
// looking up bookings works for anyone in the house.
func (s *Server) findMember(ctx context.Context, lang i18n.Lang, typed string, onlyAllowed bool) (mattermost.User, []booking.Error) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return mattermost.User{}, memberError(i18n.T(lang, "member.whose"))
	}
	// Without a chat server there is nothing to look anything up in, so the
	// field is taken as typed. This is what the demo and local development do.
	if !s.mm.Enabled() {
		return mattermost.User{Username: store.Member(mattermost.Username(typed))}, nil
	}

	// A username is an exact address: look it up directly and skip searching.
	if username := mattermost.Username(typed); looksLikeUsername(username) {
		if u, err := s.mm.ByUsername(ctx, username); err == nil {
			return u, nil
		}
		// Not a username after all — fall through and search for it as a
		// name, so "Bo" finds Bo even though it looked like one.
	}

	candidates, err := s.mm.Search(ctx, typed)
	if err != nil {
		s.log.Error("mattermost name search", "term", typed, "err", err)
		return mattermost.User{}, memberError(i18n.T(lang, "member.unreachable"))
	}
	if onlyAllowed {
		kept := candidates[:0]
		for _, u := range candidates {
			if s.rt.Mattermost.Allowed(u.Username) {
				kept = append(kept, u)
			}
		}
		candidates = kept
	}

	// A term that is somebody's whole name, nickname or username wins over one
	// that merely starts it: with both "Anna Andersson" and "Anna Anderssons
	// gäst" in the house, typing the first name means the first person.
	several := "member.several.matching"
	if exact := exactly(candidates, typed); len(exact) > 0 {
		candidates, several = exact, "member.several.named"
	}

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return mattermost.User{}, memberError(i18n.T(lang, "member.unknown", typed))
	default:
		// Never guess between people. Naming them turns the dead end into a
		// choice: one more letter, or a click in the list, settles it.
		return mattermost.User{}, memberError(i18n.T(lang, several, typed, describe(lang, candidates)))
	}
}

// exactly returns the accounts whose full name, nickname or username is the
// term, give or take capitals and accents.
func exactly(users []mattermost.User, term string) []mattermost.User {
	want := mattermost.Fold(term)
	var out []mattermost.User
	for _, u := range users {
		switch want {
		case mattermost.Fold(u.DisplayName()), mattermost.Fold(u.Nickname), mattermost.Fold(u.Username):
			out = append(out, u)
		}
	}
	return out
}

// describe lists people the way the page talks about them, so an ambiguous
// search reads as a choice rather than a rejection.
func describe(lang i18n.Lang, users []mattermost.User) string {
	const most = 5
	var names []string
	for i, u := range users {
		if i == most {
			names = append(names, i18n.T(lang, "member.andmore"))
			break
		}
		names = append(names, u.DisplayName()+" (@"+u.Username+")")
	}
	return strings.Join(names, ", ")
}

// allowed passes a resolved account through the allow list.
func (s *Server) allowed(lang i18n.Lang, u mattermost.User) (mattermost.User, []booking.Error) {
	if u.Username == "" {
		return mattermost.User{}, memberError(i18n.T(lang, "member.choose"))
	}
	if !s.rt.Mattermost.Allowed(u.Username) {
		return mattermost.User{}, memberError(i18n.T(lang, "member.notallowed", s.rt.Mattermost.AllowList()))
	}
	return u, nil
}

func memberError(msg string) []booking.Error {
	return []booking.Error{{Field: "member", Message: msg}}
}

// looksLikeUsername reports whether a value could be a Mattermost username at
// all. Names with spaces or accents never are.
func looksLikeUsername(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
