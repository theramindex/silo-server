// theramindex fork: tests that a provider with per-user lineups never shares one user's data with another.

package jellycompat

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// perUserRoutes serves installation 1 as a perUserLineup provider: user u sees
// channel "u<u>" logo, guide and stream host u<u>.example, and an anonymous
// caller gets only the flag and 401s, as XC for Silo does.
func perUserRoutes(start int64) *fakeLiveTVRoutes {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{{InstallationID: 1, PluginID: "xc"}}}
	routes.byCaller = func(_ int, c LiveTVCaller, path string) (fakeRouteResponse, bool) {
		if c.UserID == 0 {
			if path == liveTVPathInfo {
				return fakeRouteResponse{status: http.StatusOK, body: `{"contractVersion":1,"name":"XC for Silo","streamHosts":[],"perUserLineup":true}`}, true
			}
			return fakeRouteResponse{status: http.StatusUnauthorized, body: `{}`}, true
		}
		host := fmt.Sprintf("u%d.example", c.UserID)
		ch := fmt.Sprintf("u%d", c.UserID)
		switch path {
		case liveTVPathInfo:
			return fakeRouteResponse{status: http.StatusOK, body: fmt.Sprintf(`{"contractVersion":1,"name":"XC for Silo","streamHosts":[%q],"perUserLineup":true}`, host)}, true
		case liveTVPathChannels:
			return fakeRouteResponse{status: http.StatusOK, body: fmt.Sprintf(`[{"id":%q,"name":%q,"logoUrl":"https://%s/logo.png"}]`, ch, ch, host)}, true
		case liveTVPathPrograms:
			return fakeRouteResponse{status: http.StatusOK, body: fmt.Sprintf(
				`{"programs":[{"id":"p","channelId":%q,"title":"%s show","startUnix":%d,"endUnix":%d,"imageUrl":"https://%s/p.jpg"},`+
					`{"id":"q","channelId":"u1","title":"on u1","startUnix":%d,"endUnix":%d}]}`,
				ch, ch, start, start+3600, host, start, start+3600)}, true
		case liveTVPathStream:
			// Every user is handed user 1's host, so only user 1 may play.
			return fakeRouteResponse{status: http.StatusOK, body: `{"url":"http://u1.example/live.ts","container":"ts","mode":"redirect"}`}, true
		}
		return fakeRouteResponse{}, false
	}
	return routes
}

func lineupIDs(channels []liveChannel) []string {
	ids := make([]string, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
	}
	return ids
}

func TestLiveTVPerUserLineupIsolated(t *testing.T) {
	routes := perUserRoutes(0)
	l, _ := newTestLiveTV(routes)
	ctx := t.Context()
	u1, u2 := LiveTVCaller{UserID: 1}, LiveTVCaller{UserID: 2}

	// Within one TTL, each user still gets only their own lineup.
	for range 2 {
		if got := lineupIDs(l.Lineup(ctx, u1)); !slices.Equal(got, []string{"u1"}) {
			t.Fatalf("user 1 lineup = %v", got)
		}
		if got := lineupIDs(l.Lineup(ctx, u2)); !slices.Equal(got, []string{"u2"}) {
			t.Fatalf("user 2 lineup = %v", got)
		}
	}
	if n := len(routes.callsTo(liveTVPathChannels)); n != 2 {
		t.Errorf("channels calls = %d, want one per user", n)
	}
	if got := lineupIDs(l.Lineup(ctx, LiveTVCaller{})); len(got) != 0 {
		t.Errorf("anonymous lineup = %v, want none", got)
	}

	if _, ok := l.ChannelByID(ctx, u2, encodeLiveChannelID(1, "u1")); ok {
		t.Error("user 2 resolved user 1's channel")
	}
	if ch, ok := l.ChannelByID(ctx, u1, encodeLiveChannelID(1, "u1")); !ok || ch.Name != "u1" {
		t.Errorf("user 1 channel = %+v %v", ch, ok)
	}

	for user, want := range map[int]string{1: "u1.example", 2: "u2.example"} {
		info, err := l.Info(ctx, LiveTVCaller{UserID: user}, 1)
		if err != nil || !slices.Equal(info.StreamHosts, []string{want}) {
			t.Errorf("user %d info = %+v %v", user, info, err)
		}
	}
	if _, err := l.Info(ctx, LiveTVCaller{}, 1); !errors.Is(err, errLiveTVUserRequired) {
		t.Errorf("anonymous info error = %v, want errLiveTVUserRequired", err)
	}

	// The host check uses the caller's own stream hosts.
	if _, err := l.ResolveStream(ctx, u1, liveChannel{liveTVChannel: liveTVChannel{ID: "u1"}, InstallationID: 1}, "ts", ""); err != nil {
		t.Errorf("user 1 stream: %v", err)
	}
	if _, err := l.ResolveStream(ctx, u2, liveChannel{liveTVChannel: liveTVChannel{ID: "u2"}, InstallationID: 1}, "ts", ""); !errors.Is(err, errLiveTVHostNotAllowed) {
		t.Errorf("user 2 stream on user 1's host: %v, want errLiveTVHostNotAllowed", err)
	}

	if logo, ok := l.LogoURL(ctx, u1, encodeLiveChannelID(1, "u1")); !ok || logo != "https://u1.example/logo.png" {
		t.Errorf("user 1 logo = %q %v", logo, ok)
	}
	if _, ok := l.LogoURL(ctx, u2, encodeLiveChannelID(1, "u1")); ok {
		t.Error("user 2 got user 1's logo")
	}
	if _, ok := l.LogoURL(ctx, LiveTVCaller{}, encodeLiveChannelID(1, "u1")); ok {
		t.Error("anonymous caller got user 1's logo")
	}

	// Only info is ever asked anonymously.
	routes.mu.Lock()
	defer routes.mu.Unlock()
	for _, c := range routes.calls {
		if c.caller.UserID == 0 && c.path != liveTVPathInfo {
			t.Errorf("anonymous call to %s", c.path)
		}
	}
}

func TestLiveTVPerUserGuideIsolated(t *testing.T) {
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()
	routes := perUserRoutes(start)
	l, _ := newTestLiveTV(routes)
	ctx := t.Context()
	u1, u2 := LiveTVCaller{UserID: 1}, LiveTVCaller{UserID: 2}
	q := liveProgramRequest{Start: start, End: start + 3600}

	titles := func(c LiveTVCaller) []string {
		t.Helper()
		programs, err := l.Programs(ctx, c, 1, q)
		if err != nil {
			t.Fatalf("user %d programs: %v", c.UserID, err)
		}
		var out []string
		for _, p := range programs {
			out = append(out, p.Title)
		}
		return out
	}
	for range 2 {
		// User 2's provider also lists a program on channel u1, which is not
		// in user 2's lineup, so it is dropped.
		if got := titles(u1); !slices.Equal(got, []string{"u1 show", "on u1"}) {
			t.Fatalf("user 1 guide = %v", got)
		}
		if got := titles(u2); !slices.Equal(got, []string{"u2 show"}) {
			t.Fatalf("user 2 guide = %v", got)
		}
	}
	if n := len(routes.callsTo(liveTVPathPrograms)); n != 2 {
		t.Errorf("programs calls = %d, want one per user", n)
	}
	if _, err := l.Programs(ctx, LiveTVCaller{}, 1, q); !errors.Is(err, errLiveTVUserRequired) {
		t.Errorf("anonymous programs error = %v", err)
	}

	id := encodeLiveProgramID(1, "u1", start)
	if _, img, ok := l.ProgramImage(ctx, u1, id); !ok || img != "https://u1.example/p.jpg" {
		t.Errorf("user 1 program image = %q %v", img, ok)
	}
	if _, img, ok := l.ProgramImage(ctx, u2, id); ok {
		t.Errorf("user 2 got user 1's program image %q", img)
	}
	if _, img, ok := l.ProgramImage(ctx, LiveTVCaller{}, id); ok {
		t.Errorf("anonymous caller got user 1's program image %q", img)
	}
	if _, ok := l.ProgramByID(ctx, u2, id); ok {
		t.Error("user 2 resolved user 1's program")
	}
}

func TestLiveTVSharedLineupStillShared(t *testing.T) {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{{InstallationID: 1, PluginID: "d"}}}
	routes.setResponse(1, liveTVPathInfo, http.StatusOK, `{"contractVersion":1,"name":"D","streamHosts":["d.example"]}`)
	routes.setResponse(1, liveTVPathChannels, http.StatusOK, `[{"id":"a","name":"A"}]`)
	routes.setResponse(1, liveTVPathPrograms, http.StatusOK, `{"programs":[]}`)
	l, _ := newTestLiveTV(routes)
	ctx := t.Context()

	for user := range 3 {
		c := LiveTVCaller{UserID: user}
		if got := lineupIDs(l.Lineup(ctx, c)); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("user %d lineup = %v", user, got)
		}
		if info, err := l.Info(ctx, c, 1); err != nil || !slices.Equal(info.StreamHosts, []string{"d.example"}) {
			t.Fatalf("user %d info = %+v %v", user, info, err)
		}
		if _, err := l.Programs(ctx, c, 1, liveProgramRequest{Start: 0, End: 60}); err != nil {
			t.Fatalf("user %d programs: %v", user, err)
		}
	}
	for path, want := range map[string]int{liveTVPathChannels: 1, liveTVPathInfo: 1, liveTVPathPrograms: 1} {
		if n := len(routes.callsTo(path)); n != want {
			t.Errorf("%s calls = %d, want %d", path, n, want)
		}
	}
}

func TestLiveTVPerUserScopesPruned(t *testing.T) {
	routes := perUserRoutes(0)
	l, clock := newTestLiveTV(routes)
	ctx := t.Context()
	l.Lineup(ctx, LiveTVCaller{UserID: 1})
	clock.Advance(liveTVUserScopeIdle - time.Minute)
	l.Lineup(ctx, LiveTVCaller{UserID: 2})
	clock.Advance(2 * time.Minute)
	l.Providers(ctx) // refreshes the expired list, which prunes

	l.mu.Lock()
	defer l.mu.Unlock()
	var users []int
	for scope := range l.installs {
		users = append(users, scope.UserID)
	}
	slices.Sort(users)
	if !slices.Equal(users, []int{0, 2}) {
		t.Errorf("scopes left for users %v, want the shared scope and user 2", users)
	}
}
