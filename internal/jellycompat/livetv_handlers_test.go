// theramindex fork: tests for Live TV listings, views, favorites, logos and policy.

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

const (
	liveTestInstall = 5
	liveTokenP1     = "live-token-p1"
	liveTokenP2     = "live-token-p2"
)

type liveTestEnv struct {
	router chi.Router
	routes *fakeLiveTVRoutes
	prefs  *fakeLiveTVPrefs
	bridge *LiveTV
}

// newLiveTestEnv builds the compat router with a Live TV bridge. When active,
// one provider serves the shared fixtures; otherwise no provider is installed.
func newLiveTestEnv(t *testing.T, active bool) *liveTestEnv {
	t.Helper()
	routes := &fakeLiveTVRoutes{}
	if active {
		routes.providers = []LiveTVProviderRef{{InstallationID: liveTestInstall, PluginID: "silo.ramindex.dispatcharr"}}
		routes.setResponse(liveTestInstall, liveTVPathInfo, http.StatusOK, liveFixtureRaw(t, "info.json"))
		routes.setResponse(liveTestInstall, liveTVPathChannels, http.StatusOK, liveFixtureRaw(t, "channels.json"))
	}
	bridge, _ := newTestLiveTV(routes)
	prefs := &fakeLiveTVPrefs{}
	bridge.prefs = prefs
	return &liveTestEnv{router: newLiveTestRouter(t, bridge), routes: routes, prefs: prefs, bridge: bridge}
}

// newLiveTestRouter builds the compat router with sessions for profiles p1 and
// p2 of account 1. bridge may be nil.
func newLiveTestRouter(t *testing.T, bridge *LiveTV) chi.Router {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	for token, profile := range map[string]string{liveTokenP1: "p1", liveTokenP2: "p2"} {
		if err := store.Put(Session{Token: token, StreamAppUserID: 1, ProfileID: profile, PseudoUserID: PseudoUserID(1, profile)}); err != nil {
			t.Fatal(err)
		}
	}
	return NewRouter(Dependencies{Config: cfg, SessionStore: store, ContentService: &librariesContentService{}, LiveTV: bridge})
}

func liveFixtureRaw(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "livetv-v1", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

func liveChannelIDFor(providerID string) string {
	return encodeLiveChannelID(liveTestInstall, providerID)
}

func (e *liveTestEnv) seedPrefs(doc string) {
	e.prefs.seed(1, liveTestInstall, map[string]string{liveTVPrefsKey: doc})
}

func serveLive(t *testing.T, router http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeLiveJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d response: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return out
}

func (e *liveTestEnv) channels(t *testing.T, query, token string) liveChannelQueryResult {
	t.Helper()
	rec := serveLive(t, e.router, http.MethodGet, "/LiveTv/Channels"+query, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /LiveTv/Channels%s = %d %s", query, rec.Code, rec.Body.String())
	}
	return decodeLiveJSON[liveChannelQueryResult](t, rec)
}

func liveIDs(res liveChannelQueryResult) []string {
	ids := make([]string, len(res.Items))
	for i, item := range res.Items {
		ids[i] = item.ID
	}
	return ids
}

func favoriteOf(t *testing.T, res liveChannelQueryResult, id string) bool {
	t.Helper()
	for _, item := range res.Items {
		if item.ID == id {
			return item.UserData != nil && item.UserData.IsFavorite
		}
	}
	t.Fatalf("channel %s not in %v", id, liveIDs(res))
	return false
}

func TestLiveTVChannelsHidesHiddenGroups(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"profiles":{"p1":{"hiddenCategories":{"7":true}}}}`)

	res := env.channels(t, "", liveTokenP1)
	if res.TotalRecordCount != 1 || len(res.Items) != 1 || res.Items[0].ID != liveChannelIDFor("m3u:sports.one") {
		t.Fatalf("channels = %v (total %d), want only m3u:sports.one", liveIDs(res), res.TotalRecordCount)
	}
	// p2 has no entry of its own and reads the legacy keys, which hide nothing.
	if res := env.channels(t, "", liveTokenP2); res.TotalRecordCount != 3 {
		t.Errorf("p2 total = %d, want 3", res.TotalRecordCount)
	}
}

func TestLiveTVChannelsIsFavoriteFilterAndFavoriteSorting(t *testing.T) {
	env := newLiveTestEnv(t, true)
	c1, c3 := "m3u:news.hd", "m3u:sports.one"
	env.seedPrefs(`{"profiles":{"p1":{"favorites":{"` + c1 + `":true,"` + c3 + `":true},"favoriteOrder":["` + c3 + `","` + c1 + `"]}}}`)

	res := env.channels(t, "?EnableFavoriteSorting=true", liveTokenP1)
	ids := liveIDs(res)
	if len(ids) != 3 || ids[0] != liveChannelIDFor(c3) || ids[1] != liveChannelIDFor(c1) || ids[2] != liveChannelIDFor("xtream:42") {
		t.Errorf("favorite-sorted ids = %v, want %s, %s, then the rest", ids, c3, c1)
	}

	res = env.channels(t, "?isFavorite=true", liveTokenP1)
	if res.TotalRecordCount != 2 || !favoriteOf(t, res, liveChannelIDFor(c1)) || !favoriteOf(t, res, liveChannelIDFor(c3)) {
		t.Errorf("IsFavorite=true = %v, want the two favorites", liveIDs(res))
	}
	res = env.channels(t, "?IsFavorite=false", liveTokenP1)
	if ids := liveIDs(res); len(ids) != 1 || ids[0] != liveChannelIDFor("xtream:42") {
		t.Errorf("IsFavorite=false = %v, want only xtream:42", ids)
	}
}

func TestLiveTVChannelsPaging(t *testing.T) {
	env := newLiveTestEnv(t, true)
	all := env.channels(t, "", liveTokenP1)
	page := env.channels(t, "?StartIndex=1&Limit=1&UnknownParam=x", liveTokenP1)
	if page.TotalRecordCount != 3 || page.StartIndex != 1 {
		t.Errorf("total/start = %d/%d, want 3/1", page.TotalRecordCount, page.StartIndex)
	}
	if ids := liveIDs(page); len(ids) != 1 || ids[0] != all.Items[1].ID {
		t.Errorf("page = %v, want [%s]", ids, all.Items[1].ID)
	}
}

func TestLiveTVChannelsSortOptions(t *testing.T) {
	env := newLiveTestEnv(t, true)
	byName := liveIDs(env.channels(t, "?SortBy=SortName", liveTokenP1))
	want := []string{liveChannelIDFor("m3u:news.hd"), liveChannelIDFor("xtream:42"), liveChannelIDFor("m3u:sports.one")}
	if strings.Join(byName, ",") != strings.Join(want, ",") {
		t.Errorf("SortBy=SortName = %v, want %v", byName, want)
	}
	desc := liveIDs(env.channels(t, "?SortBy=ChannelNumber&SortOrder=Descending", liveTokenP1))
	if len(desc) != 3 || desc[0] != liveChannelIDFor("m3u:sports.one") || desc[2] != liveChannelIDFor("m3u:news.hd") {
		t.Errorf("number descending = %v", desc)
	}
}

func TestLiveTVChannelsProfileIsolation(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"profiles":{"p1":{"favorites":{"m3u:news.hd":true}},"p2":{"favorites":{"m3u:sports.one":true}}}}`)
	news, sports := liveChannelIDFor("m3u:news.hd"), liveChannelIDFor("m3u:sports.one")

	p1 := env.channels(t, "", liveTokenP1)
	p2 := env.channels(t, "", liveTokenP2)
	if !favoriteOf(t, p1, news) || favoriteOf(t, p1, sports) {
		t.Error("p1 should see only m3u:news.hd as a favorite")
	}
	if favoriteOf(t, p2, news) || !favoriteOf(t, p2, sports) {
		t.Error("p2 should see only m3u:sports.one as a favorite")
	}
}

func TestLiveTVChannelsPrefsErrorStillLists(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.prefs.getErr = errors.New("settings down")
	if res := env.channels(t, "", liveTokenP1); res.TotalRecordCount != 3 {
		t.Errorf("total = %d, want 3 when preferences cannot be read", res.TotalRecordCount)
	}
}

func TestLiveTVViewChildrenThroughItems(t *testing.T) {
	env := newLiveTestEnv(t, true)
	for _, path := range []string{
		"/Items?ParentId=" + liveTVViewID + "&Limit=2",
		"/Users/" + PseudoUserID(1, "p1").String() + "/Items?parentId=f1000000-0000-0000-0000-000000000001&Limit=2",
	} {
		rec := serveLive(t, env.router, http.MethodGet, path, liveTokenP1)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		res := decodeLiveJSON[liveChannelQueryResult](t, rec)
		if res.TotalRecordCount != 3 || len(res.Items) != 2 || res.Items[0].Type != "TvChannel" {
			t.Errorf("%s = %d items of %d", path, len(res.Items), res.TotalRecordCount)
		}
	}
	other := "/Users/" + PseudoUserID(1, "p2").String() + "/Items?ParentId=" + liveTVViewID
	if rec := serveLive(t, env.router, http.MethodGet, other, liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("another profile's items = %d, want 404", rec.Code)
	}
}

func liveWrapperRequest(t *testing.T, register func(chi.Router), path string, session *Session) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if session != nil {
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestLiveTVItemWrapperPassesCatalogIDsThrough(t *testing.T) {
	env := newLiveTestEnv(t, true)
	h := newLiveTVHandlers(env.bridge, "server-1", nil, nil, nil)
	codec := NewResourceIDCodec()
	session := &Session{StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}

	for _, wrapped := range []*liveTVHandlers{h, nil, {}} {
		for _, id := range []string{codec.EncodeIntID(EncodedIDLibrary, 3), codec.EncodeStringID(EncodedIDItem, "movie-1")} {
			called := false
			next := func(w http.ResponseWriter, r *http.Request) { called = true }
			rec := liveWrapperRequest(t, func(r chi.Router) { r.Get("/Items/{id}", wrapped.wrapItem(next)) }, "/Items/"+id, session)
			if !called || rec.Code != http.StatusOK {
				t.Errorf("wrapper %v: next not called for %s (status %d)", wrapped, id, rec.Code)
			}
		}
	}
}

func TestLiveTVFavoriteWrapperPassesCatalogIDsThrough(t *testing.T) {
	env := newLiveTestEnv(t, true)
	h := newLiveTVHandlers(env.bridge, "server-1", nil, nil, nil)
	codec := NewResourceIDCodec()
	session := &Session{StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}
	user := session.PseudoUserID.String()

	for _, wrapped := range []*liveTVHandlers{h, nil, {}} {
		for _, id := range []string{codec.EncodeIntID(EncodedIDLibrary, 3), codec.EncodeStringID(EncodedIDItem, "movie-1")} {
			for _, favorite := range []bool{true, false} {
				for _, route := range []string{"/UserFavoriteItems/{itemId}", "/Users/{userId}/FavoriteItems/{itemId}"} {
					called := false
					next := func(w http.ResponseWriter, r *http.Request) { called = true }
					path := strings.NewReplacer("{userId}", user, "{itemId}", id).Replace(route)
					rec := liveWrapperRequest(t, func(r chi.Router) { r.Get(route, wrapped.wrapFavorite(next, favorite)) }, path, session)
					if !called || rec.Code != http.StatusOK {
						t.Errorf("wrapper %v favorite=%v %s: next not called for %s (status %d)", wrapped, favorite, route, id, rec.Code)
					}
				}
			}
		}
	}
	if stored := env.prefs.stored(1, liveTestInstall); len(stored) != 0 {
		t.Errorf("a catalog favorite wrote Live TV preferences: %v", stored)
	}
}

func TestLiveTVWrappersRequireSession(t *testing.T) {
	env := newLiveTestEnv(t, true)
	h := newLiveTVHandlers(env.bridge, "server-1", nil, nil, nil)
	next := func(w http.ResponseWriter, r *http.Request) { t.Error("next called for a live channel id") }
	id := liveChannelIDFor("m3u:news.hd")
	cases := map[string]func(chi.Router){
		"/Items/" + id:                    func(r chi.Router) { r.Get("/Items/{id}", h.wrapItem(next)) },
		"/Items?ParentId=" + liveTVViewID: func(r chi.Router) { r.Get("/Items", h.wrapItems(next)) },
		"/UserFavoriteItems/" + id:        func(r chi.Router) { r.Get("/UserFavoriteItems/{itemId}", h.wrapFavorite(next, true)) },
	}
	for path, register := range cases {
		if rec := liveWrapperRequest(t, register, path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, rec.Code)
		}
	}
}

func TestLiveTVItemWrapperServesChannel(t *testing.T) {
	env := newLiveTestEnv(t, true)
	id := liveChannelIDFor("m3u:news.hd")
	for _, path := range []string{
		"/Items/" + id,
		"/Users/" + PseudoUserID(1, "p1").String() + "/Items/" + id,
		"/LiveTv/Channels/" + id,
	} {
		rec := serveLive(t, env.router, http.MethodGet, path, liveTokenP1)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		dto := decodeLiveJSON[liveChannelDTO](t, rec)
		if dto.ID != id || dto.Type != "TvChannel" || dto.Name != "News HD" {
			t.Errorf("%s = %+v", path, dto)
		}
	}

	rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveTVViewID, liveTokenP1)
	view := decodeLiveJSON[baseItemDTO](t, rec)
	if rec.Code != http.StatusOK || view.CollectionType != "livetv" || view.Type != "UserView" || !view.IsFolder {
		t.Errorf("view item = %d %+v", rec.Code, view)
	}
}

func TestLiveTVItemWrapperUnknownChannel404(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"hiddenCategories":{"8":true}}`)
	for _, id := range []string{liveChannelIDFor("m3u:missing"), encodeLiveChannelID(99, "m3u:news.hd"), liveChannelIDFor("m3u:sports.one")} {
		for _, path := range []string{"/Items/" + id, "/LiveTv/Channels/" + id} {
			rec := serveLive(t, env.router, http.MethodGet, path, liveTokenP1)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Item not found") {
				t.Errorf("%s = %d %s, want 404 Item not found", path, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Channels/"+liveTVViewID, liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("view id as a channel = %d, want 404", rec.Code)
	}
}

func TestLiveTVFavoriteWrapperBothRoutes(t *testing.T) {
	env := newLiveTestEnv(t, true)
	id := liveChannelIDFor("xtream:42")
	ctx := context.Background()

	rec := serveLive(t, env.router, http.MethodPost, "/UserFavoriteItems/"+id, liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST favorite = %d %s", rec.Code, rec.Body.String())
	}
	if ud := decodeLiveJSON[itemUserDataDTO](t, rec); !ud.IsFavorite || ud.ItemID != id {
		t.Errorf("POST response = %+v", ud)
	}
	prefs, err := env.bridge.ProfilePrefs(ctx, 1, liveTestInstall, "p1")
	if err != nil || !prefs.Favorites["xtream:42"] {
		t.Fatalf("after POST p1 favorites = %v (%v)", prefs.Favorites, err)
	}
	if other, _ := env.bridge.ProfilePrefs(ctx, 1, liveTestInstall, "p2"); other.Favorites["xtream:42"] {
		// p2 has no entry and reads the legacy keys, which the p1 write must not touch.
		t.Error("p1's favorite leaked to p2")
	}

	legacy := "/Users/" + PseudoUserID(1, "p1").String() + "/FavoriteItems/" + id
	rec = serveLive(t, env.router, http.MethodDelete, legacy, liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE legacy favorite = %d %s", rec.Code, rec.Body.String())
	}
	if ud := decodeLiveJSON[itemUserDataDTO](t, rec); ud.IsFavorite {
		t.Errorf("DELETE response IsFavorite = true")
	}
	if prefs, _ := env.bridge.ProfilePrefs(ctx, 1, liveTestInstall, "p1"); prefs.Favorites["xtream:42"] {
		t.Error("favorite still stored after DELETE")
	}

	wrongUser := "/Users/" + PseudoUserID(1, "p2").String() + "/FavoriteItems/" + id
	if rec := serveLive(t, env.router, http.MethodPost, wrongUser, liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("legacy favorite for another profile = %d, want 404", rec.Code)
	}
	if rec := serveLive(t, env.router, http.MethodPost, "/UserFavoriteItems/"+liveChannelIDFor("m3u:missing"), liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("favorite of an unknown channel = %d, want 404", rec.Code)
	}
}

func TestLiveTVFavoriteWriteFailureIs500(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.prefs.setErr = errors.New("disk full")
	rec := serveLive(t, env.router, http.MethodPost, "/UserFavoriteItems/"+liveChannelIDFor("xtream:42"), liveTokenP1)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "ServerError") {
		t.Errorf("write failure = %d %s, want 500 ServerError", rec.Code, rec.Body.String())
	}
}

func liveViewTypes(t *testing.T, router http.Handler) []string {
	t.Helper()
	rec := serveLive(t, router, http.MethodGet, "/UserViews", liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("/UserViews = %d %s", rec.Code, rec.Body.String())
	}
	var types []string
	for _, item := range decodeLiveJSON[queryResultDTO](t, rec).Items {
		types = append(types, item.CollectionType)
	}
	return types
}

func TestLiveTVViewAppearsOnlyWhenActive(t *testing.T) {
	if types := liveViewTypes(t, newLiveTestEnv(t, true).router); strings.Join(types, ",") != "livetv,folders" {
		t.Errorf("active views = %v, want [livetv folders]", types)
	}
	if types := liveViewTypes(t, newLiveTestEnv(t, false).router); len(types) != 0 {
		t.Errorf("inactive views = %v, want none", types)
	}
	if types := liveViewTypes(t, newLiveTestRouter(t, nil)); len(types) != 0 {
		t.Errorf("views without a bridge = %v, want none", types)
	}
}

func TestLiveTVPolicyFlag(t *testing.T) {
	for name, tc := range map[string]struct {
		router http.Handler
		want   bool
	}{
		"active":    {newLiveTestEnv(t, true).router, true},
		"inactive":  {newLiveTestEnv(t, false).router, false},
		"no bridge": {newLiveTestRouter(t, nil), false},
	} {
		rec := serveLive(t, tc.router, http.MethodGet, "/Users/Me", liveTokenP1)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: /Users/Me = %d %s", name, rec.Code, rec.Body.String())
		}
		if got := decodeLiveJSON[userDTOResponse](t, rec).Policy.EnableLiveTVAccess; got != tc.want {
			t.Errorf("%s: EnableLiveTvAccess = %v, want %v", name, got, tc.want)
		}
	}
}

func TestLiveTVInfo(t *testing.T) {
	type info struct {
		Services []struct {
			Name   string
			Status string
		}
		IsEnabled    bool
		EnabledUsers []string
	}
	rec := serveLive(t, newLiveTestEnv(t, true).router, http.MethodGet, "/LiveTv/Info", liveTokenP1)
	got := decodeLiveJSON[info](t, rec)
	if rec.Code != http.StatusOK || !got.IsEnabled || len(got.Services) != 1 || got.Services[0].Name != "Dispatcharr" || got.Services[0].Status != "Ok" {
		t.Errorf("active info = %d %s", rec.Code, rec.Body.String())
	}
	if len(got.EnabledUsers) != 1 || got.EnabledUsers[0] != PseudoUserID(1, "p1").String() {
		t.Errorf("EnabledUsers = %v", got.EnabledUsers)
	}

	rec = serveLive(t, newLiveTestEnv(t, false).router, http.MethodGet, "/LiveTv/Info", liveTokenP1)
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != `{"Services":[],"IsEnabled":false,"EnabledUsers":[]}` {
		t.Errorf("inactive info = %d %s", rec.Code, body)
	}
}

// Jellyfin web's home page waits on its Live TV section before loading any
// library row, so these listings must answer 200 rather than 404.
func TestLiveTVGuideListingsAnswerEmpty(t *testing.T) {
	router := newLiveTestEnv(t, true).router
	for _, path := range []string{"/LiveTv/Programs", "/LiveTv/Programs/Recommended?IsAiring=true&limit=1", "/LiveTv/Recordings", "/LiveTv/Recordings/Series", "/LiveTv/Timers", "/LiveTv/SeriesTimers"} {
		rec := serveLive(t, router, http.MethodGet, path, liveTokenP1)
		if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
			t.Errorf("GET %s = %d %s", path, rec.Code, body)
		}
	}
	if rec := serveLive(t, router, http.MethodPost, "/LiveTv/Programs", liveTokenP1); rec.Code != http.StatusOK {
		t.Errorf("POST /LiveTv/Programs = %d %s", rec.Code, rec.Body.String())
	}
	rec := serveLive(t, router, http.MethodGet, "/LiveTv/GuideInfo", liveTokenP1)
	got := decodeLiveJSON[liveTVGuideInfoDTO](t, rec)
	if rec.Code != http.StatusOK || got.StartDate == "" || got.EndDate == "" {
		t.Errorf("GuideInfo = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveLive(t, router, http.MethodGet, "/LiveTv/Programs/Recommended", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("Recommended without a session = %d, want 401", rec.Code)
	}
}

func TestLiveTVRoutesAbsentWithoutBridge(t *testing.T) {
	router := newLiveTestRouter(t, nil)
	for _, path := range []string{"/LiveTv/Info", "/LiveTv/Channels", "/LiveTv/Channels/" + liveChannelIDFor("m3u:news.hd")} {
		if rec := serveLive(t, router, http.MethodGet, path, liveTokenP1); rec.Code != http.StatusNotFound {
			t.Errorf("%s without a bridge = %d, want 404", path, rec.Code)
		}
	}
	if newLiveTVHandlers(nil, "", nil, nil, nil) != nil {
		t.Error("newLiveTVHandlers(nil bridge) should be nil")
	}
}

// newLogoTestEnv serves one channel whose logo is on logoURL. streamHosts is
// the provider's declared host list.
func newLogoTestEnv(t *testing.T, logoURL string, streamHosts []string) *liveTestEnv {
	t.Helper()
	env := newLiveTestEnv(t, true)
	hosts, _ := json.Marshal(streamHosts)
	env.routes.setResponse(liveTestInstall, liveTVPathInfo, http.StatusOK, `{"contractVersion":1,"name":"Dispatcharr","streamHosts":`+string(hosts)+`}`)
	env.routes.setResponse(liveTestInstall, liveTVPathChannels, http.StatusOK, `[{"id":"logo:1","number":"1","name":"Logo","logoUrl":"`+logoURL+`","groupId":"1","group":"G"}]`)
	return env
}

func TestLiveTVLogoProxied(t *testing.T) {
	logo := []byte("\x89PNG logo bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(logo)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	env := newLogoTestEnv(t, server.URL+"/logo.png", []string{host})
	// Anonymous, as <img> requests are.
	rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveChannelIDFor("logo:1")+"/Images/Primary", "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(logo) {
		t.Fatalf("logo = %d %q, want 200 with the logo bytes", rec.Code, rec.Body.String())
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveChannelIDFor("logo:missing")+"/Images/Primary", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel logo = %d, want 404", rec.Code)
	}
}

func TestLiveTVLogoPrivateHostNotInStreamHosts404(t *testing.T) {
	fetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetched = true }))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	env := newLogoTestEnv(t, "http://127.0.0.1:"+u.Port()+"/logo.png", []string{"dispatcharr.example:9191"})
	rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveChannelIDFor("logo:1")+"/Images/Primary", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("private logo = %d, want 404", rec.Code)
	}
	if fetched {
		t.Error("a logo on a private host outside streamHosts was fetched")
	}
}

// A declared host still goes through a guarded dialer: 0.0.0.0 dials loopback
// on Linux, but it is a blocked address and is refused even for a declared
// host.
func TestLiveTVLogoDeclaredHostRefusesBlockedAddress(t *testing.T) {
	fetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG"))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host := "0.0.0.0:" + u.Port()

	env := newLogoTestEnv(t, "http://"+host+"/logo.png", []string{host})
	rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveChannelIDFor("logo:1")+"/Images/Primary", "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("logo on a blocked declared address = %d, want 502", rec.Code)
	}
	if fetched {
		t.Error("a logo on a blocked address was fetched")
	}
}

func TestLiveTVLogoRejectsNonImage(t *testing.T) {
	contentType := "text/html"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	env := newLogoTestEnv(t, server.URL+"/logo.png", []string{host})
	path := "/Items/" + liveChannelIDFor("logo:1") + "/Images/Primary"
	for _, ct := range []string{"text/html", "", "application/json"} {
		contentType = ct
		rec := serveLive(t, env.router, http.MethodGet, path, "")
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "login") {
			t.Errorf("logo with Content-Type %q = %d %q, want 502", ct, rec.Code, rec.Body.String())
		}
	}
	contentType = "IMAGE/svg+xml; charset=utf-8"
	if rec := serveLive(t, env.router, http.MethodGet, path, ""); rec.Code != http.StatusOK {
		t.Errorf("logo with an image Content-Type = %d, want 200", rec.Code)
	}
}
