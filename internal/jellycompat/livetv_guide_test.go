// theramindex fork: tests for the Live TV guide: programs, recommendations,
// program items and images, and channels' current programs.

package jellycompat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// liveGuideTestEnv serves the fixture channels and four programs around the
// test clock: news.hd airs Evening News (with an image) then a movie,
// xtream:42 airs Morning News, and sports.one a live game.
type liveGuideTestEnv struct {
	*liveTestEnv
	now                     int64
	evening, movie, morning string // provider titles
	game                    string
}

func newLiveGuideTestEnv(t *testing.T) *liveGuideTestEnv {
	t.Helper()
	env := &liveGuideTestEnv{liveTestEnv: newLiveTestEnv(t, true), evening: "Evening News", movie: "Late Movie", morning: "Morning News", game: "Lakers at Celtics"}
	env.now = env.bridge.now().Unix()
	n := env.now
	body := fmt.Sprintf(`{"programs":[
		{"id":"p1","channelId":"m3u:news.hd","title":%q,"summary":"Headlines.","startUnix":%d,"endUnix":%d,"imageUrl":"https://img.example.test/news.jpg","categories":["News"],"isMovie":false,"isSports":false,"isNews":true,"isKids":false,"isSeries":false,"isLive":false},
		{"id":"p2","channelId":"m3u:news.hd","title":%q,"summary":"","startUnix":%d,"endUnix":%d,"imageUrl":"","categories":["Movie"],"isMovie":true,"isSports":false,"isNews":false,"isKids":false,"isSeries":false,"isLive":false},
		{"id":"p3","channelId":"xtream:42","title":%q,"summary":"","startUnix":%d,"endUnix":%d,"imageUrl":"","categories":[],"isMovie":false,"isSports":false,"isNews":true,"isKids":false,"isSeries":false,"isLive":false},
		{"id":"p4","channelId":"m3u:sports.one","title":%q,"summary":"","startUnix":%d,"endUnix":%d,"imageUrl":"","categories":["Sports event"],"isMovie":false,"isSports":true,"isNews":false,"isKids":false,"isSeries":false,"isLive":true,"featured":true},
		{"id":"p5","channelId":"m3u:unknown","title":"Orphan","summary":"","startUnix":%d,"endUnix":%d,"imageUrl":"","categories":[],"isMovie":false,"isSports":false,"isNews":false,"isKids":false,"isSeries":false,"isLive":false}
	]}`, env.evening, n-1800, n+1800, env.movie, n+1800, n+5400, env.morning, n-600, n+3000, env.game, n-3600, n+7200, n-60, n+60)
	env.routes.setResponse(liveTestInstall, liveTVPathPrograms, http.StatusOK, body)
	return env
}

func (e *liveGuideTestEnv) programs(t *testing.T, method, path, token, body string) liveProgramQueryResult {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Emby-Token", token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
	}
	return decodeLiveJSON[liveProgramQueryResult](t, rec)
}

func programNames(res liveProgramQueryResult) []string {
	names := make([]string, len(res.Items))
	for i, item := range res.Items {
		names[i] = item.Name
	}
	return names
}

func liveDateParam(unix int64) string {
	return url.QueryEscape(time.Unix(unix, 0).UTC().Format(time.RFC3339))
}

func TestLiveTVProgramsGuideGrid(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	news, sports := liveChannelIDFor("m3u:news.hd"), liveChannelIDFor("m3u:sports.one")
	path := fmt.Sprintf("/LiveTv/Programs?ChannelIds=%s,%s&MinEndDate=%s&MaxStartDate=%s&SortBy=StartDate", news, sports, liveDateParam(env.now), liveDateParam(env.now+86400))
	res := env.programs(t, http.MethodGet, path, liveTokenP1, "")
	if got := strings.Join(programNames(res), "|"); got != env.game+"|"+env.evening+"|"+env.movie {
		t.Fatalf("programs = %s", got)
	}
	first := res.Items[0]
	if first.Type != "Program" || first.ChannelID != sports || first.ChannelName == "" || !first.IsSports || !first.IsLive || first.StartDate == "" || first.RunTimeTicks != 3*3600*10_000_000 {
		t.Errorf("game dto = %+v", first)
	}
	if res.Items[1].ImageTags[compatImagePrimary] == "" || res.Items[1].Overview != "Headlines." || res.Items[1].Genres[0] != "News" {
		t.Errorf("evening dto = %+v", res.Items[1])
	}

	calls := env.routes.callsTo(liveTVPathPrograms)
	if len(calls) != 1 || calls[0].query["channel_ids"] != "m3u:news.hd,m3u:sports.one" || calls[0].query["start"] != fmt.Sprint(roundDownMinute(env.now)) || calls[0].query["limit"] != "" {
		t.Errorf("provider calls = %+v", calls)
	}

	// The guide posts large channel lists as a GetProgramsDto body.
	body := fmt.Sprintf(`{"ChannelIds":[%q],"MinEndDate":%q,"MaxStartDate":%q}`, news,
		time.Unix(env.now, 0).UTC().Format(time.RFC3339), time.Unix(env.now+86400, 0).UTC().Format(time.RFC3339))
	res = env.programs(t, http.MethodPost, "/LiveTv/Programs", liveTokenP1, body)
	if got := strings.Join(programNames(res), "|"); got != env.evening+"|"+env.movie {
		t.Errorf("POST programs = %s", got)
	}
}

func TestLiveTVProgramsRowsFilterByClass(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	res := env.programs(t, http.MethodGet, "/LiveTv/Programs?IsMovie=true&HasAired=false&Limit=12", liveTokenP1, "")
	if got := strings.Join(programNames(res), "|"); got != env.movie {
		t.Fatalf("movies = %s", got)
	}
	calls := env.routes.callsTo(liveTVPathPrograms)
	if len(calls) != 1 || calls[0].query["class"] != liveTVClassMovie || calls[0].query["channel_ids"] != "" || calls[0].query["limit"] != fmt.Sprint(liveProgramsMaxLimit) {
		t.Errorf("provider calls = %+v", calls)
	}

	// Shows: series only; nothing here is a series.
	res = env.programs(t, http.MethodGet, "/LiveTv/Programs?IsSeries=true&IsMovie=false&IsSports=false&IsKids=false&IsNews=false&HasAired=false", liveTokenP1, "")
	if len(res.Items) != 0 {
		t.Errorf("shows = %v", programNames(res))
	}
}

func TestLiveTVProgramsRowsLeadWithFeatured(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	res := env.programs(t, http.MethodGet, "/LiveTv/Programs?HasAired=false&Limit=2", liveTokenP1, "")
	if got := strings.Join(programNames(res), "|"); got != env.game+"|"+env.evening {
		t.Errorf("row = %s, want the featured game first", got)
	}
	// No favorites: the featured game still leads On Now, ahead of artwork.
	res = env.programs(t, http.MethodGet, "/LiveTv/Programs/Recommended?IsAiring=true", liveTokenP2, "")
	if got := strings.Join(programNames(res), "|"); got != env.game+"|"+env.evening+"|"+env.morning {
		t.Errorf("on now = %s", got)
	}
}

func TestLiveTVProgramsSkipHiddenGroups(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	env.seedPrefs(`{"profiles":{"p1":{"hiddenCategories":{"7":true}}}}`)
	res := env.programs(t, http.MethodGet, "/LiveTv/Programs?HasAired=false", liveTokenP1, "")
	if got := strings.Join(programNames(res), "|"); got != env.game {
		t.Errorf("p1 programs = %s, want only the game", got)
	}
	if res := env.programs(t, http.MethodGet, "/LiveTv/Programs?HasAired=false", liveTokenP2, ""); len(res.Items) != 4 {
		t.Errorf("p2 programs = %v, want all four known-channel programs", programNames(res))
	}
	hidden := env.programs(t, http.MethodGet, "/LiveTv/Programs?HasAired=false", liveTokenP2, "").Items[1].ID
	if rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Programs/"+hidden, liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("hidden program for p1 = %d, want 404", rec.Code)
	}
}

func TestLiveTVRecommendedPutsFavoritesThenArtworkFirst(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	env.seedPrefs(`{"profiles":{"p1":{"favorites":{"m3u:sports.one":true}}}}`)
	res := env.programs(t, http.MethodGet, "/LiveTv/Programs/Recommended?IsAiring=true&Limit=12", liveTokenP1, "")
	if got := strings.Join(programNames(res), "|"); got != env.game+"|"+env.evening+"|"+env.morning {
		t.Errorf("on now = %s, want the favorite, then the program with artwork, then the rest", got)
	}
	res = env.programs(t, http.MethodGet, "/LiveTv/Programs/Recommended?IsAiring=true&Limit=1", liveTokenP1, "")
	if len(res.Items) != 1 || res.Items[0].Name != env.game {
		t.Errorf("limit 1 = %v", programNames(res))
	}
}

func TestLiveTVProgramItemAndImage(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	res := env.programs(t, http.MethodGet, "/LiveTv/Programs?HasAired=false&IsNews=true", liveTokenP1, "")
	var id string
	for _, item := range res.Items {
		if item.Name == env.evening {
			id = item.ID
		}
	}
	if id == "" {
		t.Fatalf("evening news missing from %v", programNames(res))
	}
	for _, path := range []string{"/LiveTv/Programs/" + id, "/Items/" + id, "/Users/" + PseudoUserID(1, "p1").String() + "/Items/" + strings.ReplaceAll(id, "-", "")} {
		rec := serveLive(t, env.router, http.MethodGet, path, liveTokenP1)
		got := decodeLiveJSON[liveProgramDTO](t, rec)
		if rec.Code != http.StatusOK || got.Name != env.evening || got.ID != id || got.ChannelID != liveChannelIDFor("m3u:news.hd") {
			t.Errorf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Programs/"+encodeLiveProgramID(liveTestInstall, "m3u:news.hd", env.now+600), liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("program at an empty minute = %d, want 404", rec.Code)
	}

	inst, img, ok := env.bridge.ProgramImage(t.Context(), LiveTVCaller{}, id)
	if !ok || inst != liveTestInstall || img != "https://img.example.test/news.jpg" {
		t.Errorf("ProgramImage = %d %q %v", inst, img, ok)
	}
	// An id never listed is resolved through its provider.
	env.bridge.guideMu.Lock()
	clear(env.bridge.programImages)
	env.bridge.guideMu.Unlock()
	if _, img, ok := env.bridge.ProgramImage(t.Context(), LiveTVCaller{}, id); !ok || img != "https://img.example.test/news.jpg" {
		t.Errorf("resolved ProgramImage = %q %v", img, ok)
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/Items/"+id+"/Images/Backdrop", ""); rec.Code != http.StatusNotFound {
		t.Errorf("program backdrop = %d, want 404", rec.Code)
	}
}

func TestLiveTVChannelsCurrentProgramAndCategoryFilter(t *testing.T) {
	env := newLiveGuideTestEnv(t)
	res := env.channels(t, "", liveTokenP1)
	current := map[string]string{}
	for _, item := range res.Items {
		if item.CurrentProgram != nil {
			current[item.ID] = item.CurrentProgram.Name
		}
	}
	if current[liveChannelIDFor("m3u:news.hd")] != env.evening || current[liveChannelIDFor("xtream:42")] != env.morning || current[liveChannelIDFor("m3u:sports.one")] != env.game {
		t.Errorf("current programs = %v", current)
	}
	for _, item := range env.channels(t, "?AddCurrentProgram=false", liveTokenP1).Items {
		if item.CurrentProgram != nil {
			t.Errorf("AddCurrentProgram=false still set %s", item.ID)
		}
	}

	res = env.channels(t, "?IsSports=true&AddCurrentProgram=false", liveTokenP1)
	if ids := liveIDs(res); len(ids) != 1 || ids[0] != liveChannelIDFor("m3u:sports.one") {
		t.Errorf("sports channels = %v", ids)
	}
	res = env.channels(t, "?IsNews=true&IsMovie=true&AddCurrentProgram=false", liveTokenP1)
	if res.TotalRecordCount != 2 {
		t.Errorf("news or movie channels = %v", liveIDs(res))
	}
}

func TestLiveTVProgramsWithoutGuideRoute(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.routes.setResponse(liveTestInstall, liveTVPathPrograms, http.StatusNotFound, `{"code":"not_found"}`)
	rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Programs/Recommended?IsAiring=true", liveTokenP1)
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
		t.Errorf("recommended without guide data = %d %s", rec.Code, body)
	}
	// A failing provider is skipped rather than failing the listing.
	env.routes.setResponse(liveTestInstall, liveTVPathPrograms, http.StatusInternalServerError, ``)
	if rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Programs", liveTokenP1); rec.Code != http.StatusOK {
		t.Errorf("programs with a failing provider = %d", rec.Code)
	}
}

func TestLiveProgramsQueryWindow(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	yes, no := true, false
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	for name, tc := range map[string]struct {
		q          liveProgramsQuery
		start, end time.Time
	}{
		"default is the next day":  {liveProgramsQuery{}, now, now.Add(24 * time.Hour)},
		"airing is now":            {liveProgramsQuery{IsAiring: &yes}, now, now.Add(time.Second)},
		"upcoming starts now":      {liveProgramsQuery{HasAired: &no, MinEndDate: at(-time.Hour)}, now, now.Add(24 * time.Hour)},
		"guide day":                {liveProgramsQuery{MinEndDate: at(time.Hour), MaxStartDate: at(25 * time.Hour)}, now.Add(time.Hour), now.Add(25 * time.Hour)},
		"wide windows are clamped": {liveProgramsQuery{MinEndDate: at(0), MaxStartDate: at(30 * 24 * time.Hour)}, now, now.Add(liveGuideMaxSpan)},
		"aired looks back a day":   {liveProgramsQuery{HasAired: &yes}, now.Add(-24 * time.Hour), now},
	} {
		start, end := tc.q.window(now)
		if start != tc.start.Unix() || end != tc.end.Unix() {
			t.Errorf("%s: window = %v..%v, want %v..%v", name, time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC(), tc.start, tc.end)
		}
	}
}
