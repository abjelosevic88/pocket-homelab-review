package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// Pocket Core app links: another Pocket app (Office, Documents) may act here
// for a person with a pcx_ token, only within these scopes. Every route not
// listed in linkRoutes stays closed to it.

var linkOffers = []core.Offer{
	{Scope: "reviews:read", Name: "See your reviews", Description: "List your reviews and look up whether a file sent from another app is in review (the badge Pocket Office and Pocket Documents show)."},
	{Scope: "reviews:write", Name: "Send documents for review", Description: "Start a review from a document in another app, add a new revision to one, and keep the link back to the original up to date."},
}

// portable is what Pocket Move may copy out and bring in for a person
// (docs/move.md in pocket-homelab-core); Register adds move:export/import.
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.zip", File: "review.zip", Kind: core.KindPocket, Title: "The reviews you started: every revision of the document, all comments with who wrote them, pictures, tasks and who was on it"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, Form: "file", Title: "Adds the reviews from another box with you as their owner; comments keep their writer's name; reviews already here are skipped"},
	},
}

// capabilities is what Pocket Search, Pocket Day and Pocket Hub may ask
// (docs/capabilities.md in pocket-homelab-core; capabilities.go), each on its
// one route under /api/v1/pocket. Register adds pocket:search/day/widget.
// Nothing here approves, comments or tells anyone anything, so there is no
// capture: a review needs a document and people, more than a quick line.
var capabilities = core.Capabilities{
	Search: &core.Searchable{What: "The reviews you are on, by title and description", Kinds: []string{"review"}, Bangs: []string{"review", "reviews"}},
	Day: &core.DayFeed{Label: "Review", One: "review", Many: "reviews", Unit: "count",
		What: "The reviews you created, started, commented on, gave your verdict on or decided that day, and the decisions on your own reviews"},
	Widget: &core.Widget{Title: "Reviews", What: "The reviews waiting for you first, then your own reviews waiting for others"},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(reviews|reviews/lookup|dashboard)$`), "reviews:read"},
	{"GET", regexp.MustCompile(`^/api/v1/reviews/\d+/?$`), "reviews:read"},
	{"POST", regexp.MustCompile(`^/api/v1/reviews/import$`), "reviews:write"},
	{"POST", regexp.MustCompile(`^/api/v1/reviews/\d+/revisions$`), "reviews:write"},
	{"PUT", regexp.MustCompile(`^/api/v1/reviews/\d+/?$`), "reviews:write"},
	{"POST", regexp.MustCompile(`^/api/v1/reviews/\d+/status$`), "reviews:write"},
}

var errLinkScope = errors.New("outside the link's scope")

func linkAllows(scopes []string, r *http.Request) bool {
	if portable.LinkAllows(scopes, r) || capabilities.LinkAllows(scopes, r) {
		return true
	}
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token and maps it to the person it acts for.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !in.Active || !in.Delegated() || in.User == nil || in.User.Email == "" {
		return nil, http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, errLinkScope
	}
	return s.coreIdentityUser(ctx, in)
}
