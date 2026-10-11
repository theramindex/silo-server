// theramindex fork: guards that keep the Live TV bridge from changing non-Live-TV behavior.

package jellycompat

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestLiveTVEnabledFromEnv(t *testing.T) {
	for value, want := range map[string]bool{
		"":         true,
		"on":       true,
		"true":     true,
		"1":        true,
		"yes":      true,
		"off":      false,
		"OFF":      false,
		" Off ":    false,
		"false":    false,
		"False":    false,
		"0":        false,
		"disabled": false,
		"DISABLED": false,
		"\tno\n":   true,
	} {
		getenv := func(key string) string {
			if key != "SILO_JELLYCOMPAT_LIVETV" {
				t.Errorf("read %q, want SILO_JELLYCOMPAT_LIVETV", key)
			}
			return value
		}
		if got := LiveTVEnabledFromEnv(getenv); got != want {
			t.Errorf("SILO_JELLYCOMPAT_LIVETV=%q: enabled = %v, want %v", value, got, want)
		}
	}
}

// newGuardRouter builds the compat router around content, with sessions for
// profile p1 of account 1. bridge may be nil.
func newGuardRouter(t *testing.T, content ContentService, bridge *LiveTV) chi.Router {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	if err := store.Put(Session{Token: liveTokenP1, StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
		t.Fatal(err)
	}
	return NewRouter(Dependencies{Config: cfg, SessionStore: store, ContentService: content, LiveTV: bridge})
}

// TestLiveTVOffMatchesUpstream: with SILO_JELLYCOMPAT_LIVETV=off the bridge is
// nil, and a bridge without providers must be indistinguishable from that on
// everything a client reads outside /LiveTv.
func TestLiveTVOffMatchesUpstream(t *testing.T) {
	content := &librariesContentService{}
	off := newGuardRouter(t, content, nil)
	bridge, _ := newTestLiveTV(&fakeLiveTVRoutes{})
	idle := newGuardRouter(t, content, bridge)

	for _, path := range []string{"/UserViews", "/Users/Me"} {
		want := serveLive(t, off, http.MethodGet, path, liveTokenP1)
		got := serveLive(t, idle, http.MethodGet, path, liveTokenP1)
		if want.Code != http.StatusOK || got.Code != want.Code {
			t.Errorf("%s: off = %d, no providers = %d, want both 200", path, want.Code, got.Code)
		}
		if got.Body.String() != want.Body.String() {
			t.Errorf("%s differs without providers\n off:          %s\n no providers: %s", path, want.Body.String(), got.Body.String())
		}
	}

	if rec := serveLive(t, off, http.MethodGet, "/LiveTv/Channels", liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("/LiveTv/Channels with the bridge off = %d, want 404", rec.Code)
	}
}

// TestLiveTVOffMountsNoLiveTVRoutes lists every route of the bridge-off router.
func TestLiveTVOffMountsNoLiveTVRoutes(t *testing.T) {
	off := newGuardRouter(t, &librariesContentService{}, nil)
	err := chi.Walk(off, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "/LiveTv") || strings.Contains(route, "/live") {
			t.Errorf("%s %s is mounted with the bridge off", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestLiveTVWrappedRoutesForwardCatalogIDs sends a catalog item id through
// every route the bridge wraps. The answer must match the same request with no
// bridge, so a wrapper never swallows or rewrites a non-Live-TV request.
func TestLiveTVWrappedRoutesForwardCatalogIDs(t *testing.T) {
	content := &librariesContentService{}
	off := newGuardRouter(t, content, nil)
	bridge, _ := newTestLiveTV(&fakeLiveTVRoutes{})
	on := newGuardRouter(t, content, bridge)

	id := NewResourceIDCodec().EncodeStringID(EncodedIDItem, "tmdb-movie-1")
	user := PseudoUserID(1, "p1").String()
	type route struct{ method, path string }
	routes := []route{
		{http.MethodGet, "/Items/" + id},
		{http.MethodGet, "/Users/" + user + "/Items/" + id},
		{http.MethodGet, "/Items/" + id + "/PlaybackInfo"},
		{http.MethodPost, "/Items/" + id + "/PlaybackInfo"},
		{http.MethodGet, "/Users/" + user + "/Items/" + id + "/PlaybackInfo"},
		{http.MethodPost, "/Users/" + user + "/Items/" + id + "/PlaybackInfo"},
		{http.MethodPost, "/UserFavoriteItems/" + id},
		{http.MethodDelete, "/UserFavoriteItems/" + id},
		{http.MethodPost, "/Users/" + user + "/FavoriteItems/" + id},
		{http.MethodDelete, "/Users/" + user + "/FavoriteItems/" + id},
		{http.MethodGet, "/Videos/" + id + "/stream"},
		{http.MethodHead, "/Videos/" + id + "/stream"},
		{http.MethodGet, "/Videos/" + id + "/stream.ts"},
		{http.MethodHead, "/Videos/" + id + "/stream.ts"},
	}
	for _, rt := range routes {
		want := serveLive(t, off, rt.method, rt.path, liveTokenP1)
		got := serveLive(t, on, rt.method, rt.path, liveTokenP1)
		if got.Code != want.Code {
			t.Errorf("%s %s: bridge on = %d, bridge off = %d", rt.method, rt.path, got.Code, want.Code)
		}
		if got.Body.String() != want.Body.String() {
			t.Errorf("%s %s: body differs\n bridge on:  %s\n bridge off: %s", rt.method, rt.path, got.Body.String(), want.Body.String())
		}
	}
}
