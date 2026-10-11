// theramindex fork: tests for Live TV provider discovery, merging and stream resolution.

package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRouteResponse struct {
	status int
	body   string
	err    error
}

type fakeRouteCall struct {
	installationID int
	caller         LiveTVCaller
	path           string
	query          map[string]string
}

// fakeLiveTVRoutes is a LiveTVRouteCaller serving canned responses. Later Live
// TV tests reuse it.
type fakeLiveTVRoutes struct {
	mu        sync.Mutex
	providers []LiveTVProviderRef
	listErr   error
	listCalls int
	responses map[int]map[string]fakeRouteResponse // installation -> path -> response
	calls     []fakeRouteCall
}

func (f *fakeLiveTVRoutes) ListProviders(context.Context) ([]LiveTVProviderRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]LiveTVProviderRef(nil), f.providers...), nil
}

func (f *fakeLiveTVRoutes) Call(_ context.Context, id int, c LiveTVCaller, method, path string, q map[string]string) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	query := make(map[string]string, len(q))
	for k, v := range q {
		query[k] = v
	}
	f.calls = append(f.calls, fakeRouteCall{installationID: id, caller: c, path: path, query: query})
	resp, ok := f.responses[id][path]
	if !ok {
		return 0, nil, fmt.Errorf("fakeLiveTVRoutes: no response for installation %d %s %s", id, method, path)
	}
	if resp.err != nil {
		return 0, nil, resp.err
	}
	return resp.status, []byte(resp.body), nil
}

// setResponse installs or replaces the canned response for one route.
func (f *fakeLiveTVRoutes) setResponse(installationID int, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.responses == nil {
		f.responses = map[int]map[string]fakeRouteResponse{}
	}
	if f.responses[installationID] == nil {
		f.responses[installationID] = map[string]fakeRouteResponse{}
	}
	f.responses[installationID][path] = fakeRouteResponse{status: status, body: body}
}

// callsTo returns the recorded calls to path, for every installation.
func (f *fakeLiveTVRoutes) callsTo(path string) []fakeRouteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRouteCall
	for _, c := range f.calls {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeLiveTVRoutes) listCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

type liveTVTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *liveTVTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *liveTVTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestLiveTV builds a LiveTV over routes with a controllable clock.
func newTestLiveTV(routes *fakeLiveTVRoutes) (*LiveTV, *liveTVTestClock) {
	clock := &liveTVTestClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	l := NewLiveTV(routes, nil, nil)
	l.now = clock.Now
	return l, clock
}

// liveTVChannelsBody renders a channels response with one channel per id.
func liveTVChannelsBody(ids ...string) string {
	items := make([]string, len(ids))
	for i, id := range ids {
		items[i] = fmt.Sprintf(`{"id":%q,"number":"%d","name":"Channel %s","logoUrl":"","groupId":"7","group":"News"}`, id, 100+i, id)
	}
	return "[" + strings.Join(items, ",") + "]"
}

func liveTVChannelIDs(lineup []liveChannel) []string {
	ids := make([]string, len(lineup))
	for i, ch := range lineup {
		ids[i] = fmt.Sprintf("%d/%s", ch.InstallationID, ch.ID)
	}
	return ids
}

func assertLiveTVLineup(t *testing.T, lineup []liveChannel, want ...string) {
	t.Helper()
	got := liveTVChannelIDs(lineup)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lineup = %v, want %v", got, want)
	}
}

func singleProviderRoutes(channelsBody string) *fakeLiveTVRoutes {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{{InstallationID: 1, PluginID: "silo.ramindex.dispatcharr"}}}
	routes.setResponse(1, liveTVPathChannels, 200, channelsBody)
	return routes
}

func TestLiveTVInactiveWithoutProviders(t *testing.T) {
	routes := &fakeLiveTVRoutes{}
	l, clock := newTestLiveTV(routes)
	ctx := context.Background()

	if l.Active(ctx) {
		t.Error("Active = true with no providers")
	}
	if lineup := l.Lineup(ctx, LiveTVCaller{}); len(lineup) != 0 {
		t.Errorf("lineup = %v, want empty", liveTVChannelIDs(lineup))
	}
	// The empty list is cached too, so an idle server does not poll the host.
	if routes.listCallCount() != 1 {
		t.Errorf("ListProviders called %d times, want 1", routes.listCallCount())
	}
	clock.Advance(61 * time.Second)
	l.Active(ctx)
	if routes.listCallCount() != 2 {
		t.Errorf("ListProviders called %d times after the TTL, want 2", routes.listCallCount())
	}
}

func TestLiveTVNilRoutesIsInactive(t *testing.T) {
	l := NewLiveTV(nil, nil, nil)
	if l.Active(context.Background()) {
		t.Error("Active = true with nil routes")
	}
}

func TestLiveTVProviderListKeptOnError(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	l, clock := newTestLiveTV(routes)
	ctx := context.Background()

	if !l.Active(ctx) {
		t.Fatal("Active = false with one provider")
	}
	clock.Advance(61 * time.Second)
	routes.mu.Lock()
	routes.listErr = errors.New("host unavailable")
	routes.mu.Unlock()

	if !l.Active(ctx) {
		t.Error("Active flipped to false after a failed refresh")
	}
	if got := l.Providers(ctx); len(got) != 1 || got[0].InstallationID != 1 {
		t.Errorf("Providers = %+v, want the previous list", got)
	}
	// The failed refresh still extends the TTL.
	if routes.listCallCount() != 2 {
		t.Errorf("ListProviders called %d times, want 2", routes.listCallCount())
	}
}

func TestLiveTVMergesProvidersWithoutIDCollisions(t *testing.T) {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{
		{InstallationID: 1, PluginID: "silo.ramindex.dispatcharr"},
		{InstallationID: 2, PluginID: "silo.ramindex.xtream"},
	}}
	routes.setResponse(1, liveTVPathChannels, 200, liveTVChannelsBody("xtream:42"))
	routes.setResponse(2, liveTVPathChannels, 200, liveTVChannelsBody("xtream:42"))
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	lineup := l.Lineup(ctx, LiveTVCaller{})
	assertLiveTVLineup(t, lineup, "1/xtream:42", "2/xtream:42")
	if lineup[0].EncodedID == lineup[1].EncodedID {
		t.Fatalf("both installations share encoded id %s", lineup[0].EncodedID)
	}
	for _, want := range lineup {
		if want.EncodedID != encodeLiveChannelID(want.InstallationID, want.ID) {
			t.Errorf("EncodedID = %s, want encodeLiveChannelID(%d, %q)", want.EncodedID, want.InstallationID, want.ID)
		}
		got, ok := l.ChannelByID(ctx, LiveTVCaller{}, want.EncodedID)
		if !ok || got.InstallationID != want.InstallationID || got.ID != "xtream:42" {
			t.Errorf("ChannelByID(%s) = %+v, %v; want installation %d", want.EncodedID, got, ok, want.InstallationID)
		}
	}
	if _, ok := l.ChannelByID(ctx, LiveTVCaller{}, encodeLiveChannelID(3, "xtream:42")); ok {
		t.Error("ChannelByID resolved a channel for an installation that is not a provider")
	}
	if _, ok := l.ChannelByID(ctx, LiveTVCaller{}, "not-a-live-channel-id"); ok {
		t.Error("ChannelByID resolved a malformed id")
	}
}

func TestLiveTVSkipsFailingProvider(t *testing.T) {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{{InstallationID: 1}, {InstallationID: 2}}}
	routes.setResponse(1, liveTVPathChannels, 200, liveTVChannelsBody("a", "b"))
	routes.setResponse(2, liveTVPathChannels, 500, `{"error":"boom"}`)
	l, _ := newTestLiveTV(routes)

	assertLiveTVLineup(t, l.Lineup(context.Background(), LiveTVCaller{}), "1/a", "1/b")
}

func TestLiveTVSkipsProviderWithUndecodableChannels(t *testing.T) {
	routes := &fakeLiveTVRoutes{providers: []LiveTVProviderRef{{InstallationID: 1}, {InstallationID: 2}}}
	routes.setResponse(1, liveTVPathChannels, 200, `{"not":"a list"}`)
	routes.setResponse(2, liveTVPathChannels, 200, liveTVChannelsBody("b"))
	l, _ := newTestLiveTV(routes)

	assertLiveTVLineup(t, l.Lineup(context.Background(), LiveTVCaller{}), "2/b")
}

func TestLiveTVCachesChannelsFor60s(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	l, clock := newTestLiveTV(routes)
	ctx := context.Background()

	l.Lineup(ctx, LiveTVCaller{})
	l.Lineup(ctx, LiveTVCaller{})
	if n := len(routes.callsTo(liveTVPathChannels)); n != 1 {
		t.Fatalf("channels called %d times within the TTL, want 1", n)
	}
	clock.Advance(59 * time.Second)
	l.Lineup(ctx, LiveTVCaller{})
	if n := len(routes.callsTo(liveTVPathChannels)); n != 1 {
		t.Fatalf("channels called %d times at 59s, want 1", n)
	}
	clock.Advance(2 * time.Second)
	l.Lineup(ctx, LiveTVCaller{})
	if n := len(routes.callsTo(liveTVPathChannels)); n != 2 {
		t.Fatalf("channels called %d times after the TTL, want 2", n)
	}
}

func TestLiveTVKeepsStaleLineupOnError(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a", "b", "c"))
	l, clock := newTestLiveTV(routes)
	ctx := context.Background()

	assertLiveTVLineup(t, l.Lineup(ctx, LiveTVCaller{}), "1/a", "1/b", "1/c")
	clock.Advance(61 * time.Second)
	routes.setResponse(1, liveTVPathChannels, 500, "")

	assertLiveTVLineup(t, l.Lineup(ctx, LiveTVCaller{}), "1/a", "1/b", "1/c")
	if n := len(routes.callsTo(liveTVPathChannels)); n != 2 {
		t.Errorf("channels called %d times, want the failed refresh to have been attempted", n)
	}
}

func TestLiveTVDropsEmptyAndDuplicateChannelIDs(t *testing.T) {
	routes := singleProviderRoutes(`[
		{"id":"a","number":"1","name":"First A","logoUrl":"","groupId":"7","group":"News"},
		{"id":"","number":"2","name":"No ID","logoUrl":"","groupId":"7","group":"News"},
		{"id":"b","number":"3","name":"B","logoUrl":"","groupId":"7","group":"News"},
		{"id":"a","number":"4","name":"Second A","logoUrl":"","groupId":"7","group":"News"}
	]`)
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	lineup := l.Lineup(ctx, LiveTVCaller{})
	assertLiveTVLineup(t, lineup, "1/a", "1/b")
	if lineup[0].Name != "First A" {
		t.Errorf("duplicate id kept %q, want the first entry", lineup[0].Name)
	}
	got, ok := l.ChannelByID(ctx, LiveTVCaller{}, encodeLiveChannelID(1, "a"))
	if !ok || got.Name != "First A" {
		t.Errorf("ChannelByID(a) = %+v, %v; want the first entry", got, ok)
	}
}

func TestLiveTVChannelByIDRefreshesOnMiss(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	assertLiveTVLineup(t, l.Lineup(ctx, LiveTVCaller{}), "1/a")
	routes.setResponse(1, liveTVPathChannels, 200, liveTVChannelsBody("a", "new"))

	got, ok := l.ChannelByID(ctx, LiveTVCaller{}, encodeLiveChannelID(1, "new"))
	if !ok || got.ID != "new" || got.InstallationID != 1 {
		t.Fatalf("ChannelByID(new) = %+v, %v; want the channel added after the first lineup", got, ok)
	}
	// The refreshed lineup replaces the cached one.
	assertLiveTVLineup(t, l.Lineup(ctx, LiveTVCaller{}), "1/a", "1/new")
	if n := len(routes.callsTo(liveTVPathChannels)); n != 2 {
		t.Errorf("channels called %d times, want 2", n)
	}
}

func TestLiveTVChannelByIDRefreshesAtMostOncePerCall(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()
	missing := encodeLiveChannelID(1, "missing")

	// Cold cache: the lookup's own fetch already is the refresh.
	if _, ok := l.ChannelByID(ctx, LiveTVCaller{}, missing); ok {
		t.Fatal("found a channel the provider does not serve")
	}
	if n := len(routes.callsTo(liveTVPathChannels)); n != 1 {
		t.Fatalf("channels called %d times on a cold miss, want 1", n)
	}
	// Warm cache: one forced refresh, then give up.
	if _, ok := l.ChannelByID(ctx, LiveTVCaller{}, missing); ok {
		t.Fatal("found a channel the provider does not serve")
	}
	if n := len(routes.callsTo(liveTVPathChannels)); n != 2 {
		t.Fatalf("channels called %d times on a warm miss, want 2", n)
	}
}

func TestLiveTVPassesCallerIdentity(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	l, _ := newTestLiveTV(routes)
	caller := LiveTVCaller{UserID: 5, ProfileID: "p1"}

	l.Lineup(context.Background(), caller)

	calls := routes.callsTo(liveTVPathChannels)
	if len(calls) != 1 || calls[0].caller != caller || calls[0].installationID != 1 {
		t.Fatalf("channels calls = %+v, want one call as %+v", calls, caller)
	}
}

func TestLiveTVLogoURL(t *testing.T) {
	routes := singleProviderRoutes(`[
		{"id":"http","number":"1","name":"H","logoUrl":"http://logos.example/h.png","groupId":"7","group":"News"},
		{"id":"https","number":"2","name":"S","logoUrl":"https://logos.example/s.png","groupId":"7","group":"News"},
		{"id":"empty","number":"3","name":"E","logoUrl":"","groupId":"7","group":"News"},
		{"id":"data","number":"4","name":"D","logoUrl":"data:image/png;base64,AAAA","groupId":"7","group":"News"},
		{"id":"relative","number":"5","name":"R","logoUrl":"/logos/r.png","groupId":"7","group":"News"}
	]`)
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	for id, want := range map[string]string{
		"http":  "http://logos.example/h.png",
		"https": "https://logos.example/s.png",
	} {
		got, ok := l.LogoURL(ctx, encodeLiveChannelID(1, id))
		if !ok || got != want {
			t.Errorf("LogoURL(%s) = %q, %v; want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"empty", "data", "relative", "unknown"} {
		if got, ok := l.LogoURL(ctx, encodeLiveChannelID(1, id)); ok {
			t.Errorf("LogoURL(%s) = %q, true; want no logo", id, got)
		}
	}
	// Logo requests carry no identity.
	for _, c := range routes.callsTo(liveTVPathChannels) {
		if c.caller != (LiveTVCaller{}) {
			t.Errorf("logo lookup called the provider as %+v, want anonymous", c.caller)
		}
	}
}

func TestLiveTVInfoCachedAndStale(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	routes.setResponse(1, liveTVPathInfo, 200, `{"contractVersion":1,"name":"Dispatcharr","streamHosts":["a.example:9191"]}`)
	l, clock := newTestLiveTV(routes)
	ctx := context.Background()

	info, err := l.Info(ctx, LiveTVCaller{}, 1)
	if err != nil || info.Name != "Dispatcharr" || len(info.StreamHosts) != 1 {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	if _, err := l.Info(ctx, LiveTVCaller{}, 1); err != nil {
		t.Fatal(err)
	}
	if n := len(routes.callsTo(liveTVPathInfo)); n != 1 {
		t.Fatalf("info called %d times within the TTL, want 1", n)
	}

	clock.Advance(61 * time.Second)
	routes.setResponse(1, liveTVPathInfo, 500, "")
	info, err = l.Info(ctx, LiveTVCaller{}, 1)
	if err != nil || info.Name != "Dispatcharr" {
		t.Fatalf("Info after a failed refresh = %+v, %v; want the stale copy", info, err)
	}
}

func TestLiveTVInfoErrorWithoutCopy(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	routes.setResponse(1, liveTVPathInfo, 503, "")
	l, _ := newTestLiveTV(routes)

	if _, err := l.Info(context.Background(), LiveTVCaller{}, 1); err == nil {
		t.Fatal("Info succeeded with a failing provider and no cached copy")
	}
}

func TestLiveTVResolveStreamValidatesHost(t *testing.T) {
	const streamHostJSON = `{"contractVersion":1,"name":"Dispatcharr","streamHosts":["a.example:9191"]}`

	tests := []struct {
		name    string
		status  int
		body    string
		want    liveTVStream
		wantErr error
		anyErr  bool
	}{
		{
			name:   "allowed host",
			status: 200,
			body:   `{"url":"http://a.example:9191/x","container":"ts","mode":"redirect"}`,
			want:   liveTVStream{URL: "http://a.example:9191/x", Container: "ts", Mode: "redirect"},
		},
		{
			name:   "host compared case-insensitively",
			status: 200,
			body:   `{"url":"https://A.Example:9191/x.m3u8","container":"hls","mode":"server"}`,
			want:   liveTVStream{URL: "https://A.Example:9191/x.m3u8", Container: "hls", Mode: "server"},
		},
		{
			name:    "foreign host",
			status:  200,
			body:    `{"url":"http://evil.example/x","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{
			name:    "right host wrong port",
			status:  200,
			body:    `{"url":"http://a.example/x","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{
			name:    "userinfo cannot smuggle an allowed host",
			status:  200,
			body:    `{"url":"http://a.example:9191@evil.example/x","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{
			name:    "non-http scheme",
			status:  200,
			body:    `{"url":"ftp://a.example:9191/x","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{
			name:    "relative url",
			status:  200,
			body:    `{"url":"/x","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{
			name:    "unparseable url",
			status:  200,
			body:    `{"url":"http://a.example:9191/%zz","container":"ts","mode":"redirect"}`,
			wantErr: errLiveTVHostNotAllowed,
		},
		{name: "format unavailable", status: 422, body: `{"code":"format_unavailable"}`, wantErr: errLiveTVFormatUnavailable},
		{name: "channel not found", status: 404, body: `{"code":"channel_not_found"}`, wantErr: errLiveTVChannelNotFound},
		{name: "other status", status: 502, body: `{}`, anyErr: true},
		{name: "undecodable body", status: 200, body: `<html>`, anyErr: true},
		{
			name:   "unknown container",
			status: 200,
			body:   `{"url":"http://a.example:9191/x","container":"mp4","mode":"redirect"}`,
			anyErr: true,
		},
		{
			name:   "unknown mode",
			status: 200,
			body:   `{"url":"http://a.example:9191/x","container":"ts","mode":"proxy"}`,
			anyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := singleProviderRoutes(liveTVChannelsBody("m3u:news.hd"))
			routes.setResponse(1, liveTVPathInfo, 200, streamHostJSON)
			routes.setResponse(1, liveTVPathStream, tt.status, tt.body)
			l, _ := newTestLiveTV(routes)
			ctx := context.Background()
			caller := LiveTVCaller{UserID: 9, ProfileID: "p2"}

			ch, ok := l.ChannelByID(ctx, caller, encodeLiveChannelID(1, "m3u:news.hd"))
			if !ok {
				t.Fatal("channel not found")
			}
			got, err := l.ResolveStream(ctx, caller, ch, "ts")

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if got != (liveTVStream{}) {
					t.Errorf("stream = %+v returned alongside an error", got)
				}
			case tt.anyErr:
				if err == nil {
					t.Fatal("expected an error")
				}
				for _, sentinel := range []error{errLiveTVChannelNotFound, errLiveTVFormatUnavailable, errLiveTVHostNotAllowed} {
					if errors.Is(err, sentinel) {
						t.Errorf("err = %v, want a generic failure, not %v", err, sentinel)
					}
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if got != tt.want {
					t.Errorf("stream = %+v, want %+v", got, tt.want)
				}
			}

			calls := routes.callsTo(liveTVPathStream)
			if len(calls) != 1 {
				t.Fatalf("stream called %d times, want 1", len(calls))
			}
			if q := calls[0].query; q["channel_id"] != "m3u:news.hd" || q["format"] != "ts" {
				t.Errorf("stream query = %v, want channel_id=m3u:news.hd format=ts", q)
			}
			if calls[0].caller != caller {
				t.Errorf("stream called as %+v, want %+v", calls[0].caller, caller)
			}
		})
	}
}

func TestLiveTVResolveStreamErrorsDoNotLeakURL(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	routes.setResponse(1, liveTVPathInfo, 200, `{"contractVersion":1,"name":"X","streamHosts":["a.example"]}`)
	routes.setResponse(1, liveTVPathStream, 200, `{"url":"http://user:secret@evil.example/%zz","container":"ts","mode":"server"}`)
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	ch, _ := l.ChannelByID(ctx, LiveTVCaller{}, encodeLiveChannelID(1, "a"))
	_, err := l.ResolveStream(ctx, LiveTVCaller{}, ch, "any")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "evil.example") {
		t.Errorf("error leaks the stream URL: %v", err)
	}
}

func TestLiveTVResolveStreamNeedsInfo(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a"))
	routes.setResponse(1, liveTVPathInfo, 500, "")
	routes.setResponse(1, liveTVPathStream, 200, `{"url":"http://a.example/x","container":"ts","mode":"redirect"}`)
	l, _ := newTestLiveTV(routes)
	ctx := context.Background()

	ch, _ := l.ChannelByID(ctx, LiveTVCaller{}, encodeLiveChannelID(1, "a"))
	if _, err := l.ResolveStream(ctx, LiveTVCaller{}, ch, "any"); err == nil {
		t.Fatal("ResolveStream succeeded without any way to check the stream host")
	}
}

func TestLiveTVConcurrentLineup(t *testing.T) {
	routes := singleProviderRoutes(liveTVChannelsBody("a", "b"))
	l, _ := newTestLiveTV(routes)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lineup := l.Lineup(context.Background(), LiveTVCaller{}); len(lineup) != 2 {
				t.Errorf("lineup has %d channels, want 2", len(lineup))
			}
		}()
	}
	wg.Wait()
}
