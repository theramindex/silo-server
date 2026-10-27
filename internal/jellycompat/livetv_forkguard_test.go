// theramindex fork: guards that keep the Live TV bridge from changing non-Live-TV behavior.

package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
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
// profile p1 of account 1. bridge and codec may be nil. A test that sends ids
// it encoded itself passes its codec, since the router decodes opaque ids
// through its own codec's reverse map.
func newGuardRouter(t *testing.T, content ContentService, bridge *LiveTV, codec *ResourceIDCodec) chi.Router {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	if err := store.Put(Session{Token: liveTokenP1, StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
		t.Fatal(err)
	}
	return NewRouter(Dependencies{Config: cfg, SessionStore: store, ContentService: content, LiveTV: bridge, IDCodec: codec})
}

// TestLiveTVOffMatchesUpstream: with SILO_JELLYCOMPAT_LIVETV=off the bridge is
// nil, and a bridge without providers must be indistinguishable from that on
// everything a client reads outside /LiveTv.
//
// Off-switch parity with upstream is identical except for path-segment
// normalization of livetv/channels: path_normalization.go maps those segments
// to LiveTv and Channels whether or not the bridge is on.
//
// With a bridge but no providers, /LiveTv/* answers 200 with empty content
// rather than upstream's 404. Clients reach it only when they see the Live TV
// policy flag, which is off without providers.
func TestLiveTVOffMatchesUpstream(t *testing.T) {
	content := &librariesContentService{}
	off := newGuardRouter(t, content, nil, nil)
	bridge, _ := newTestLiveTV(&fakeLiveTVRoutes{})
	idle := newGuardRouter(t, content, bridge, nil)

	for _, path := range []string{"/UserViews", "/Users/Me"} {
		want := serveLive(t, off, http.MethodGet, path, liveTokenP1)
		got := serveLive(t, idle, http.MethodGet, path, liveTokenP1)
		if want.Code != http.StatusOK || got.Code != want.Code {
			t.Errorf("%s: off = %d, no providers = %d, want both 200", path, want.Code, got.Code)
		}
		wantBody, gotBody := want.Body.String(), got.Body.String()
		if path == "/Users/Me" {
			// Upstream now returns session/request timestamps. These vary between
			// independent requests; compare every other field of the wire response.
			withoutRequestTimes := func(body string) string {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(body), &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "LastLoginDate")
				delete(fields, "LastActivityDate")
				normalized, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				return string(normalized)
			}
			wantBody, gotBody = withoutRequestTimes(wantBody), withoutRequestTimes(gotBody)
		}
		if gotBody != wantBody {
			t.Errorf("%s differs without providers\n off:          %s\n no providers: %s", path, wantBody, gotBody)
		}
	}

	if rec := serveLive(t, off, http.MethodGet, "/LiveTv/Channels", liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("/LiveTv/Channels with the bridge off = %d, want 404", rec.Code)
	}
}

// TestLiveTVOffMountsNoLiveTVRoutes lists every route of the bridge-off router.
func TestLiveTVOffMountsNoLiveTVRoutes(t *testing.T) {
	off := newGuardRouter(t, &librariesContentService{}, nil, nil)
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

// guardUpstreamContent is a ContentService whose GetItemDetail records the
// content id it was asked for and then fails. Every wrapped upstream handler
// reaches GetItemDetail before it needs any other dependency, so a recorded
// lookup is proof the request got through the wrapper to the upstream handler.
type guardUpstreamContent struct {
	librariesContentService
	lookups []string
}

func (s *guardUpstreamContent) GetItemDetail(_ context.Context, _ *Session, contentID string, _ *int) (*upstreamItemDetail, error) {
	s.lookups = append(s.lookups, contentID)
	return nil, &HTTPError{StatusCode: http.StatusNotFound}
}

// TestLiveTVWrappedRoutesForwardCatalogIDs sends a catalog item id through
// every route the bridge wraps, once with no bridge and once with a bridge
// that has a provider and channels. It proves two things for each route:
//
//   - the upstream handler ran on the bridge router: ContentService.GetItemDetail
//     was asked for the decoded catalog content id exactly once, as it is with
//     no bridge, and
//   - the wrapper added nothing: status and body equal the no-bridge answer.
//
// The stream routes carry Static=true, because without it HandleVideoStream
// stops at the playback-session lookup before any injectable dependency; with
// it the handler builds a session from GetItemDetail. A sanity request first
// shows the bridge router does serve a live channel, so the wrappers under
// test are active.
func TestLiveTVWrappedRoutesForwardCatalogIDs(t *testing.T) {
	offContent, onContent := &guardUpstreamContent{}, &guardUpstreamContent{}
	codec := NewResourceIDCodec()
	off := newGuardRouter(t, offContent, nil, codec)
	on := newGuardRouter(t, onContent, newLiveTestEnv(t, true).bridge, codec)

	live := serveLive(t, on, http.MethodGet, "/Items/"+liveChannelIDFor("m3u:news.hd"), liveTokenP1)
	if live.Code != http.StatusOK || len(onContent.lookups) != 0 {
		t.Fatalf("live channel on the bridge router = %d with %d upstream lookups, want 200 and none: %s",
			live.Code, len(onContent.lookups), live.Body.String())
	}

	const contentID = "tmdb-movie-1"
	id := codec.EncodeStringID(EncodedIDItem, contentID)
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
		{http.MethodGet, "/Videos/" + id + "/stream?Static=true"},
		{http.MethodHead, "/Videos/" + id + "/stream?Static=true"},
		{http.MethodGet, "/Videos/" + id + "/stream.ts?Static=true"},
		{http.MethodHead, "/Videos/" + id + "/stream.ts?Static=true"},
	}
	for _, rt := range routes {
		offContent.lookups, onContent.lookups = nil, nil
		want := serveLive(t, off, rt.method, rt.path, liveTokenP1)
		got := serveLive(t, on, rt.method, rt.path, liveTokenP1)

		if !slices.Equal(offContent.lookups, []string{contentID}) {
			t.Errorf("%s %s: no bridge looked up %q, want exactly [%s]; the route does not reach the upstream handler", rt.method, rt.path, offContent.lookups, contentID)
		}
		if !slices.Equal(onContent.lookups, []string{contentID}) {
			t.Errorf("%s %s: bridge router looked up %q, want exactly [%s]; the wrapper kept the catalog id from the upstream handler", rt.method, rt.path, onContent.lookups, contentID)
		}
		if got.Code != want.Code {
			t.Errorf("%s %s: bridge on = %d, bridge off = %d", rt.method, rt.path, got.Code, want.Code)
		}
		if got.Body.String() != want.Body.String() {
			t.Errorf("%s %s: body differs\n bridge on:  %s\n bridge off: %s", rt.method, rt.path, got.Body.String(), want.Body.String())
		}
	}
}
