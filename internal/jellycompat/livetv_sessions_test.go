// theramindex fork: tests for Live TV provider connection slots.

package jellycompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newLiveSessionEnv is a stream env whose provider leases slots: the stream
// answers leased, and both session routes answer 200.
func newLiveSessionEnv(t *testing.T) (*liveTestEnv, *liveProvider, *liveTVTestClock) {
	t.Helper()
	p := newLiveProvider(t)
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/101.ts", Container: "ts", Mode: "server", Leased: true})
	env.routes.setResponse(liveTestInstall, liveTVPathSessionHeartbeat, http.StatusOK, `{}`)
	env.routes.setResponse(liveTestInstall, liveTVPathSessionStop, http.StatusOK, `{}`)
	clock := &liveTVTestClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	env.bridge.now = clock.Now
	return env, p, clock
}

// livePlaybackInfoOnDevice negotiates a play the way a client that names its
// device does.
func livePlaybackInfoOnDevice(t *testing.T, router http.Handler, id, device string) playbackInfoResponseDTO {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Items/"+id+"/PlaybackInfo", strings.NewReader(`{"DeviceProfile":`+liveTVProfileTV+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Test", Device="TV", DeviceId="`+device+`", Version="1", Token="`+liveTokenP1+`"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo = %d %s", rec.Code, rec.Body.String())
	}
	return decodeLiveJSON[playbackInfoResponseDTO](t, rec)
}

func liveReport(t *testing.T, router http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", liveTokenP1)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func sessionIDs(calls []fakeRouteCall) []string {
	ids := make([]string, len(calls))
	for i, c := range calls {
		ids[i] = c.query[liveTVSessionIDParam]
	}
	return ids
}

// A play is leased under its PlaySessionId; progress renews the slot at most
// every beat, and a stop report frees it and ends the play.
func TestLivePlaySlotFollowsReports(t *testing.T) {
	env, _, clock := newLiveSessionEnv(t)
	id := liveChannelIDFor("m3u:news.hd")
	info := livePlaybackInfoOnDevice(t, env.router, id, "tv-1")
	if calls := env.routes.callsTo(liveTVPathStream); len(calls) != 1 || calls[0].query[liveTVSessionIDParam] != info.PlaySessionID {
		t.Fatalf("stream calls = %+v, want one with session_id %s", calls, info.PlaySessionID)
	}

	progress := `{"ItemId":"` + id + `","PlaySessionId":"` + info.PlaySessionID + `","PositionTicks":0}`
	if rec := liveReport(t, env.router, "/Sessions/Playing/Progress", progress); rec.Code != http.StatusNoContent {
		t.Fatalf("progress = %d", rec.Code)
	}
	if n := len(env.routes.callsTo(liveTVPathSessionHeartbeat)); n != 0 {
		t.Fatalf("heartbeats right after PlaybackInfo = %d, want 0", n)
	}
	clock.Advance(liveTVSessionBeat)
	liveReport(t, env.router, "/Sessions/Playing/Progress", progress)
	liveReport(t, env.router, "/Sessions/Playing/Progress", progress)
	if got := sessionIDs(env.routes.callsTo(liveTVPathSessionHeartbeat)); len(got) != 1 || got[0] != info.PlaySessionID {
		t.Fatalf("heartbeats after one beat = %v", got)
	}

	if rec := liveReport(t, env.router, "/Sessions/Playing/Stopped", progress); rec.Code != http.StatusNoContent {
		t.Fatalf("stopped = %d", rec.Code)
	}
	if got := sessionIDs(env.routes.callsTo(liveTVPathSessionStop)); len(got) != 1 || got[0] != info.PlaySessionID {
		t.Fatalf("stops = %v", got)
	}
	if _, ok := env.bridge.play(info.PlaySessionID); ok {
		t.Error("a stopped play is still known")
	}
}

// Switching channels on a device frees the old slot before asking for the new
// stream, so a one-connection account can switch.
func TestLiveNewPlayOnDeviceFreesTheOld(t *testing.T) {
	env, _, _ := newLiveSessionEnv(t)
	first := livePlaybackInfoOnDevice(t, env.router, liveChannelIDFor("m3u:news.hd"), "tv-1")
	livePlaybackInfoOnDevice(t, env.router, liveChannelIDFor("xtream:42"), "tv-2")
	if n := len(env.routes.callsTo(liveTVPathSessionStop)); n != 0 {
		t.Fatalf("another device's play freed %d slots", n)
	}
	livePlaybackInfoOnDevice(t, env.router, liveChannelIDFor("xtream:42"), "tv-1")
	if got := sessionIDs(env.routes.callsTo(liveTVPathSessionStop)); len(got) != 1 || got[0] != first.PlaySessionID {
		t.Fatalf("stops = %v, want the first play's %s", got, first.PlaySessionID)
	}
}

func TestLivePlaybackInfoConnectionLimit(t *testing.T) {
	env, _, _ := newLiveSessionEnv(t)
	env.routes.setResponse(liveTestInstall, liveTVPathStream, http.StatusConflict, `{"code":"connection_limit"}`)
	_, info := livePlaybackInfo(t, env.router, liveChannelIDFor("m3u:news.hd"), liveTVProfileTV)
	if info.ErrorCode != liveTVPlaybackErrorRateLimit || len(info.MediaSources) != 0 {
		t.Fatalf("PlaybackInfo = %+v, want %s", info, liveTVPlaybackErrorRateLimit)
	}
}

// A provider that forgot the session, after a restart, is asked for the stream
// again under the same id; when the slot is gone the play ends.
func TestLiveHeartbeatRenewsAForgottenSession(t *testing.T) {
	env, _, clock := newLiveSessionEnv(t)
	id := liveChannelIDFor("m3u:news.hd")
	info := livePlaybackInfoOnDevice(t, env.router, id, "tv-1")
	progress := `{"ItemId":"` + id + `","PlaySessionId":"` + info.PlaySessionID + `"}`
	env.routes.setResponse(liveTestInstall, liveTVPathSessionHeartbeat, http.StatusNotFound, `{"code":"session_not_found"}`)

	clock.Advance(liveTVSessionBeat)
	liveReport(t, env.router, "/Sessions/Playing/Progress", progress)
	calls := env.routes.callsTo(liveTVPathStream)
	if got := sessionIDs(calls); len(got) != 2 || got[1] != info.PlaySessionID || calls[1].query["format"] != liveTVContainerTS {
		t.Fatalf("stream calls = %+v, want a renewal for %s", calls, info.PlaySessionID)
	}
	if _, ok := env.bridge.play(info.PlaySessionID); !ok {
		t.Fatal("a renewed play ended")
	}

	env.routes.setResponse(liveTestInstall, liveTVPathStream, http.StatusConflict, `{"code":"connection_limit"}`)
	clock.Advance(liveTVSessionBeat)
	liveReport(t, env.router, "/Sessions/Playing/Progress", progress)
	if _, ok := env.bridge.play(info.PlaySessionID); ok {
		t.Fatal("a play whose slot was taken still plays")
	}
}

// A provider that does not lease is never sent session calls.
func TestLiveUnleasedPlayHasNoSessionCalls(t *testing.T) {
	p := newLiveProvider(t)
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/101.ts", Container: "ts", Mode: "server"})
	clock := &liveTVTestClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	env.bridge.now = clock.Now
	id := liveChannelIDFor("m3u:news.hd")
	info := livePlaybackInfoOnDevice(t, env.router, id, "tv-1")
	clock.Advance(liveTVSessionBeat)
	report := `{"ItemId":"` + id + `","PlaySessionId":"` + info.PlaySessionID + `"}`
	liveReport(t, env.router, "/Sessions/Playing/Progress", report)
	liveReport(t, env.router, "/Sessions/Playing/Stopped", report)
	livePlaybackInfoOnDevice(t, env.router, id, "tv-1")
	if n := len(env.routes.callsTo(liveTVPathSessionHeartbeat)) + len(env.routes.callsTo(liveTVPathSessionStop)); n != 0 {
		t.Fatalf("session calls = %d, want 0", n)
	}
}

// A stream request without a play holds its slot only while it relays.
func TestLiveStreamWithoutPlayFreesItsSlot(t *testing.T) {
	env, p, _ := newLiveSessionEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/Videos/"+liveChannelIDFor("m3u:news.hd")+"/stream.ts", nil)
	req.Header.Set("X-Emby-Token", liveTokenP1)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != len(p.ts) {
		t.Fatalf("stream = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	streams := env.routes.callsTo(liveTVPathStream)
	stops := env.routes.callsTo(liveTVPathSessionStop)
	if len(streams) != 1 || len(stops) != 1 || stops[0].query[liveTVSessionIDParam] != streams[0].query[liveTVSessionIDParam] {
		t.Fatalf("stream calls %+v, stop calls %+v", streams, stops)
	}
}

// Reports for anything but a live play reach upstream with their body.
func TestLivePlaybackReportPassesOtherPlaysThrough(t *testing.T) {
	h := &liveTVHandlers{bridge: NewLiveTV(&fakeLiveTVRoutes{}, nil, nil)}
	var got string
	next := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusTeapot)
	}
	body := `{"ItemId":"movie","PlaySessionId":"not-live"}`
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: liveTokenP1, StreamAppUserID: 1, ProfileID: "p1"}))
	rec := httptest.NewRecorder()
	h.wrapPlaybackReport(next, liveReportProgress)(rec, req)
	if rec.Code != http.StatusTeapot || got != body {
		t.Fatalf("next got %d %q", rec.Code, got)
	}
}
