// theramindex fork: tests for Live TV playback: PlaybackInfo, TS redirect and relay, HLS relay.

package jellycompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
)

const (
	liveTVProfileTV      = `{"DirectPlayProfiles":[{"Type":"Video","Container":"ts,mp4,mkv"}]}`
	liveTVProfileBrowser = `{"DirectPlayProfiles":[{"Type":"Video","Container":"mp4"},{"Type":"Video","Container":"webm"}]}`
	// liveSecret stands in for the credentials a provider puts in its stream
	// URL path. It must never reach a client or a log line.
	liveSecret = "s3cret-pass"
)

func TestDeviceProfilePlaysTS(t *testing.T) {
	cases := []struct {
		name    string
		profile DeviceProfile
		want    bool
	}{
		{"mp4 and mkv only", DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mp4,mkv"}}}, false},
		{"ts listed, no type", DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Container: "ts,mp4"}}}, true},
		{"mpegts", DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Container: "mpegts"}}}, true},
		{"m2ts with spaces", DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mp4, M2TS"}}}, true},
		{"audio ts", DeviceProfile{DirectPlayProfiles: []DirectPlayProfile{{Type: "Audio", Container: "ts"}}}, false},
		{"empty", DeviceProfile{}, false},
	}
	for _, tc := range cases {
		if got := deviceProfilePlaysTS(tc.profile); got != tc.want {
			t.Errorf("%s: deviceProfilePlaysTS = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fakeRelayRegister records registered URLs and refuses hosts outside allowed.
func fakeRelayRegister(allowed ...string) (func(*url.URL) (string, error), *[]string) {
	var got []string
	return func(abs *url.URL) (string, error) {
		if !liveHostAllowed(allowed, abs.Host) {
			return "", fmt.Errorf("host %s not allowed", abs.Host)
		}
		got = append(got, abs.String())
		return fmt.Sprintf("live/tok%d?x=1", len(got)), nil
	}, &got
}

func TestRewriteLivePlaylist(t *testing.T) {
	base, _ := url.Parse("http://provider.example:9191/" + liveSecret + "/hls/index.m3u8?token=abc")

	media := "#EXTM3U\r\n" +
		"#EXT-X-TARGETDURATION:6\r\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x01\r\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\r\n" +
		"#EXTINF:6.000,\r\n" +
		"seg1.ts\r\n" +
		"\r\n" +
		"#EXTINF:6.000,\r\n" +
		"http://cdn.example/abs/seg2.ts?sig=1\r\n" +
		"#EXT-X-ENDLIST"
	register, got := fakeRelayRegister("provider.example:9191", "cdn.example")
	out, err := rewriteLivePlaylist([]byte(media), base, register)
	if err != nil {
		t.Fatal(err)
	}
	want := "#EXTM3U\r\n" +
		"#EXT-X-TARGETDURATION:6\r\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"live/tok1?x=1\",IV=0x01\r\n" +
		"#EXT-X-MAP:URI=\"live/tok2?x=1\"\r\n" +
		"#EXTINF:6.000,\r\n" +
		"live/tok3?x=1\r\n" +
		"\r\n" +
		"#EXTINF:6.000,\r\n" +
		"live/tok4?x=1\r\n" +
		"#EXT-X-ENDLIST"
	if string(out) != want {
		t.Errorf("rewritten media playlist:\n%q\nwant\n%q", out, want)
	}
	wantURLs := []string{
		"http://provider.example:9191/" + liveSecret + "/hls/key.bin",
		"http://provider.example:9191/" + liveSecret + "/hls/init.mp4",
		"http://provider.example:9191/" + liveSecret + "/hls/seg1.ts",
		"http://cdn.example/abs/seg2.ts?sig=1",
	}
	if strings.Join(*got, " ") != strings.Join(wantURLs, " ") {
		t.Errorf("registered %v, want %v", *got, wantURLs)
	}

	master := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"en\",URI=\"audio/en.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1280000,AUDIO=\"aud\"\n" +
		"low/index.m3u8\n" +
		"#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=86000,URI=\"low/iframe.m3u8\"\n"
	register, got = fakeRelayRegister("provider.example:9191")
	out, err = rewriteLivePlaylist([]byte(master), base, register)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "m3u8") || strings.Contains(string(out), liveSecret) {
		t.Errorf("master playlist kept a provider URI:\n%s", out)
	}
	if !strings.Contains(string(out), "#EXT-X-STREAM-INF:BANDWIDTH=1280000,AUDIO=\"aud\"\n") || len(*got) != 3 {
		t.Errorf("master playlist = %q, registered %v", out, *got)
	}

	register, _ = fakeRelayRegister("provider.example:9191")
	if _, err := rewriteLivePlaylist([]byte("#EXTM3U\n#EXTINF:6,\nhttp://evil.example/seg.ts\n"), base, register); err == nil {
		t.Error("a segment on a disallowed host should fail the rewrite")
	}
}

// liveProvider is an httptest server standing in for a Dispatcharr or Xtream
// upstream. Its paths carry liveSecret like real credentialed URLs do.
type liveProvider struct {
	*httptest.Server
	ts       []byte
	segments map[string][]byte
	playlist map[string]string
	hits     atomic.Int32
}

func newLiveProvider(t *testing.T) *liveProvider {
	t.Helper()
	p := &liveProvider{
		ts:       bytes.Repeat([]byte("0123456789abcdef"), 200<<10/16),
		segments: map[string][]byte{},
		playlist: map[string]string{},
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if r.URL.Path == "/"+liveSecret+"/101.ts" {
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(p.ts)
			return
		}
		if body, ok := p.playlist[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(body))
			return
		}
		if seg, ok := p.segments[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(seg)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *liveProvider) host() string { return strings.TrimPrefix(p.URL, "http://") }

// newLiveStreamEnv serves the shared channel fixtures with the provider's host
// as the only streamHost, and answers stream requests with stream. Logs go to
// the returned buffer, which keeps the request log out of test output; the
// test fails if any line names liveSecret.
func newLiveStreamEnv(t *testing.T, p *liveProvider, stream liveTVStream) (*liveTestEnv, *lockedWriter) {
	t.Helper()
	logs := captureLiveLogs(t)
	t.Cleanup(func() {
		if strings.Contains(logs.String(), liveSecret) {
			t.Errorf("logs leak a provider URL:\n%s", logs.String())
		}
	})
	env := newLiveTestEnv(t, true)
	env.routes.setResponse(liveTestInstall, liveTVPathInfo, http.StatusOK,
		`{"contractVersion":1,"name":"Dispatcharr","streamHosts":["`+p.host()+`"]}`)
	env.setStream(t, stream)
	return env, logs
}

func (e *liveTestEnv) setStream(t *testing.T, stream liveTVStream) {
	t.Helper()
	body, err := json.Marshal(stream)
	if err != nil {
		t.Fatal(err)
	}
	e.routes.setResponse(liveTestInstall, liveTVPathStream, http.StatusOK, string(body))
}

// captureLiveLogs sends slog output to a buffer for the rest of the test.
func captureLiveLogs(t *testing.T) *lockedWriter {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	w := &lockedWriter{w: &buf}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return w
}

// lockedWriter is a buffer safe for concurrent log writes.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

func livePlaybackInfo(t *testing.T, router http.Handler, id, profile string) (*httptest.ResponseRecorder, playbackInfoResponseDTO) {
	t.Helper()
	body := `{"DeviceProfile":` + profile + `}`
	req := httptest.NewRequest(http.MethodPost, "/Items/"+id+"/PlaybackInfo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", liveTokenP1)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec, playbackInfoResponseDTO{}
	}
	return rec, decodeLiveJSON[playbackInfoResponseDTO](t, rec)
}

// serveLiveMedia requests a media path with no token, as players do.
func serveLiveMedia(t *testing.T, router http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertNoProviderURL(t *testing.T, p *liveProvider, what, s string) {
	t.Helper()
	if strings.Contains(s, p.host()) || strings.Contains(s, liveSecret) {
		t.Errorf("%s leaks the provider URL: %s", what, s)
	}
}

func lastStreamFormat(t *testing.T, env *liveTestEnv) string {
	t.Helper()
	calls := env.routes.callsTo(liveTVPathStream)
	if len(calls) == 0 {
		t.Fatal("no stream call")
	}
	return calls[len(calls)-1].query["format"]
}

func TestLivePlaybackInfoTSClientRedirect(t *testing.T) {
	p := newLiveProvider(t)
	providerURL := p.URL + "/" + liveSecret + "/101.ts"
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: providerURL, Container: "ts", Mode: "redirect"})
	id := liveChannelIDFor("m3u:news.hd")

	rec, info := livePlaybackInfo(t, env.router, id, liveTVProfileTV)
	if rec.Code != http.StatusOK || info.ErrorCode != "" || len(info.MediaSources) != 1 || info.PlaySessionID == "" {
		t.Fatalf("PlaybackInfo = %d %s", rec.Code, rec.Body.String())
	}
	if f := lastStreamFormat(t, env); f != "any" {
		t.Errorf("format = %q, want any for a TS-capable client", f)
	}
	src := info.MediaSources[0]
	if src.ID != id || src.Container != "ts" || !src.SupportsDirectPlay || !src.SupportsDirectStream || src.SupportsTranscoding ||
		!src.IsInfiniteStream || src.Protocol != "Http" || src.Name != "News HD" {
		t.Errorf("source = %+v", src)
	}
	wantURL := "/Videos/" + id + "/stream.ts?Static=true&MediaSourceId=" + id + "&PlaySessionId=" + info.PlaySessionID
	if src.DirectStreamURL != wantURL || src.Path != wantURL {
		t.Errorf("DirectStreamUrl = %q, Path = %q, want %q", src.DirectStreamURL, src.Path, wantURL)
	}

	if rec := serveLiveMedia(t, env.router, http.MethodHead, src.DirectStreamURL); rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "video/mp2t" || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d %q body %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = serveLiveMedia(t, env.router, http.MethodGet, src.DirectStreamURL)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != providerURL {
		t.Errorf("GET stream = %d Location %q, want 302 to the provider", rec.Code, rec.Header().Get("Location"))
	}
	if p.hits.Load() != 0 {
		t.Error("redirect mode fetched the stream itself")
	}
}

func TestLivePlaybackInfoServerModeTSRelaysBytes(t *testing.T) {
	p := newLiveProvider(t)
	env, logs := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/101.ts", Container: "ts", Mode: "server"})
	id := liveChannelIDFor("m3u:news.hd")

	rec, info := livePlaybackInfo(t, env.router, id, liveTVProfileTV)
	if rec.Code != http.StatusOK || len(info.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo = %d %s", rec.Code, rec.Body.String())
	}
	assertNoProviderURL(t, p, "PlaybackInfo", rec.Body.String())

	rec = serveLiveMedia(t, env.router, http.MethodGet, info.MediaSources[0].DirectStreamURL)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), p.ts) {
		t.Fatalf("relay = %d, %d bytes, want 200 with %d bytes", rec.Code, rec.Body.Len(), len(p.ts))
	}
	if rec.Header().Get("Location") != "" || rec.Header().Get("Content-Type") != "video/mp2t" || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("relay headers = %v", rec.Header())
	}
	assertNoProviderURL(t, p, "logs", logs.String())
}

func TestLiveStreamWithoutPlayResolvesTS(t *testing.T) {
	p := newLiveProvider(t)
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/101.ts", Container: "ts", Mode: "server"})
	id := liveChannelIDFor("m3u:news.hd")

	rec := serveLive(t, env.router, http.MethodGet, "/Videos/"+id+"/stream.ts?Static=true", liveTokenP1)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), p.ts) {
		t.Fatalf("stream without PlaySessionId = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if f := lastStreamFormat(t, env); f != "ts" {
		t.Errorf("format = %q, want ts", f)
	}
	if rec := serveLiveMedia(t, env.router, http.MethodGet, "/Videos/"+id+"/stream.ts?Static=true"); rec.Code != http.StatusUnauthorized {
		t.Errorf("stream with no credentials = %d, want 401", rec.Code)
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/Videos/"+liveChannelIDFor("m3u:missing")+"/stream", liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel stream = %d, want 404", rec.Code)
	}
}

// relayPath resolves a relative playlist URI against the path it was served on.
func relayPath(t *testing.T, servedAt, ref string) string {
	t.Helper()
	base, _ := url.Parse(servedAt)
	r, err := url.Parse(ref)
	if err != nil {
		t.Fatal(err)
	}
	return base.ResolveReference(r).String()
}

func playlistURIs(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func TestLivePlaybackInfoBrowserGetsHLSRelay(t *testing.T) {
	p := newLiveProvider(t)
	p.playlist["/"+liveSecret+"/hls/index.m3u8"] = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg1.ts\n#EXTINF:6.0,\nseg2.ts\n"
	p.segments["/"+liveSecret+"/hls/seg1.ts"] = []byte("SEGMENT-ONE")
	p.segments["/"+liveSecret+"/hls/seg2.ts"] = []byte("SEGMENT-TWO")
	env, logs := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/hls/index.m3u8", Container: "hls", Mode: "server"})
	id := liveChannelIDFor("m3u:news.hd")

	rec, info := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser)
	if rec.Code != http.StatusOK || len(info.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo = %d %s", rec.Code, rec.Body.String())
	}
	if f := lastStreamFormat(t, env); f != "hls" {
		t.Errorf("format = %q, want hls for a browser", f)
	}
	assertNoProviderURL(t, p, "PlaybackInfo", rec.Body.String())
	src := info.MediaSources[0]
	wantURL := "/Videos/" + id + "/live.m3u8?MediaSourceId=" + id + "&PlaySessionId=" + info.PlaySessionID
	if src.Container != "hls" || src.SupportsDirectPlay || src.SupportsDirectStream || !src.SupportsTranscoding ||
		src.TranscodingURL != wantURL || src.TranscodingContainer != "ts" || src.TranscodingSubProtocol != "hls" || !src.IsInfiniteStream {
		t.Errorf("source = %+v", src)
	}

	rec = serveLiveMedia(t, env.router, http.MethodGet, src.TranscodingURL)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("playlist = %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	assertNoProviderURL(t, p, "playlist", rec.Body.String())
	if !strings.Contains(rec.Body.String(), "#EXTINF:6.0,\n") {
		t.Errorf("playlist lost its tags:\n%s", rec.Body.String())
	}
	uris := playlistURIs(rec.Body.String())
	if len(uris) != 2 {
		t.Fatalf("playlist URIs = %v", uris)
	}
	for i, want := range []string{"SEGMENT-ONE", "SEGMENT-TWO"} {
		seg := serveLiveMedia(t, env.router, http.MethodGet, relayPath(t, src.TranscodingURL, uris[i]))
		if seg.Code != http.StatusOK || seg.Body.String() != want || seg.Header().Get("Content-Type") != "video/mp2t" {
			t.Errorf("segment %d = %d %q", i, seg.Code, seg.Body.String())
		}
	}

	// The direct stream URL of an HLS play points the player at the relay.
	rec = serveLiveMedia(t, env.router, http.MethodGet, "/Videos/"+id+"/stream.ts?MediaSourceId="+id+"&PlaySessionId="+info.PlaySessionID)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != src.TranscodingURL {
		t.Errorf("stream of an HLS play = %d %q, want 302 to %s", rec.Code, rec.Header().Get("Location"), src.TranscodingURL)
	}
	assertNoProviderURL(t, p, "logs", logs.String())
}

func TestLiveHLSRelayNestedAndRedirectedPlaylists(t *testing.T) {
	// The declared host redirects to a second host that serves the master and
	// its variant; relayed children may use that final host.
	cdn := newLiveProvider(t)
	cdn.playlist["/"+liveSecret+"/master.m3u8"] = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nlow/index.m3u8\n"
	cdn.playlist["/"+liveSecret+"/low/index.m3u8"] = "#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n"
	cdn.segments["/"+liveSecret+"/low/seg1.ts"] = []byte("LOW-ONE")
	origin := newLiveProvider(t)
	origin.Config.Handler = http.RedirectHandler(cdn.URL+"/"+liveSecret+"/master.m3u8", http.StatusFound)
	env, _ := newLiveStreamEnv(t, origin, liveTVStream{URL: origin.URL + "/start.m3u8", Container: "hls", Mode: "server"})
	id := liveChannelIDFor("m3u:news.hd")

	_, info := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser)
	master := info.MediaSources[0].TranscodingURL
	rec := serveLiveMedia(t, env.router, http.MethodGet, master)
	if rec.Code != http.StatusOK {
		t.Fatalf("master = %d %s", rec.Code, rec.Body.String())
	}
	assertNoProviderURL(t, cdn, "master", rec.Body.String())
	variant := relayPath(t, master, playlistURIs(rec.Body.String())[0])
	rec = serveLiveMedia(t, env.router, http.MethodGet, variant)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("variant = %d %s", rec.Code, rec.Body.String())
	}
	assertNoProviderURL(t, cdn, "variant", rec.Body.String())
	seg := relayPath(t, variant, playlistURIs(rec.Body.String())[0])
	if !strings.HasPrefix(seg, "/Videos/"+id+"/live/") || strings.Count(seg, "/live/") != 1 {
		t.Errorf("segment path %s is not a relay path", seg)
	}
	if rec := serveLiveMedia(t, env.router, http.MethodGet, seg); rec.Code != http.StatusOK || rec.Body.String() != "LOW-ONE" {
		t.Errorf("segment = %d %q", rec.Code, rec.Body.String())
	}

	// A segment on a host that is neither declared nor the final playlist host
	// fails the whole playlist.
	cdn.playlist["/"+liveSecret+"/master.m3u8"] = "#EXTM3U\n#EXTINF:6.0,\nhttp://other.example/seg.ts\n"
	if rec := serveLiveMedia(t, env.router, http.MethodGet, master); rec.Code != http.StatusBadGateway {
		t.Errorf("playlist with a foreign host = %d, want 502", rec.Code)
	}
}

func TestLiveStreamRedirectLimit(t *testing.T) {
	p := newLiveProvider(t)
	p.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	})
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/loop", Container: "ts", Mode: "server"})
	rec := serveLive(t, env.router, http.MethodGet, "/Videos/"+liveChannelIDFor("m3u:news.hd")+"/stream", liveTokenP1)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("redirect loop = %d, want 502", rec.Code)
	}
}

func TestLivePlaybackInfoBrowserTSOnlySource(t *testing.T) {
	p := newLiveProvider(t)
	env, _ := newLiveStreamEnv(t, p, liveTVStream{})
	env.routes.setResponse(liveTestInstall, liveTVPathStream, http.StatusUnprocessableEntity, `{"error":"format unavailable"}`)
	id := liveChannelIDFor("m3u:news.hd")

	rec, info := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser)
	if rec.Code != http.StatusOK || info.ErrorCode != playbackErrorNoCompatibleStream || info.PlaySessionID != "" ||
		!strings.Contains(rec.Body.String(), `"MediaSources":[]`) {
		t.Errorf("PlaybackInfo = %d %s, want NoCompatibleStream", rec.Code, rec.Body.String())
	}

	// A provider that answers a browser's hls request with TS is no better.
	env.setStream(t, liveTVStream{URL: p.URL + "/" + liveSecret + "/101.ts", Container: "ts", Mode: "server"})
	if _, info := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser); info.ErrorCode != playbackErrorNoCompatibleStream {
		t.Errorf("TS for a browser: ErrorCode = %q, want NoCompatibleStream", info.ErrorCode)
	}
}

func TestLivePlaybackInfoHostNotAllowed(t *testing.T) {
	p := newLiveProvider(t)
	env, logs := newLiveStreamEnv(t, p, liveTVStream{URL: "http://elsewhere.example/" + liveSecret + "/101.ts", Container: "ts", Mode: "server"})

	rec, info := livePlaybackInfo(t, env.router, liveChannelIDFor("m3u:news.hd"), liveTVProfileTV)
	if rec.Code != http.StatusOK || info.ErrorCode != "NotAllowed" || len(info.MediaSources) != 0 {
		t.Errorf("PlaybackInfo = %d %s, want ErrorCode NotAllowed", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), liveSecret) || strings.Contains(logs.String(), liveSecret) {
		t.Error("the disallowed URL leaked")
	}
}

func TestLivePlaybackInfoUnknownChannel404(t *testing.T) {
	p := newLiveProvider(t)
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/x.ts", Container: "ts", Mode: "server"})
	if rec, _ := livePlaybackInfo(t, env.router, liveChannelIDFor("m3u:missing"), liveTVProfileTV); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel = %d, want 404", rec.Code)
	}
	env.routes.setResponse(liveTestInstall, liveTVPathStream, http.StatusNotFound, `{}`)
	if rec, _ := livePlaybackInfo(t, env.router, liveChannelIDFor("m3u:news.hd"), liveTVProfileTV); rec.Code != http.StatusNotFound {
		t.Errorf("provider 404 = %d, want 404", rec.Code)
	}
}

func TestLiveSegmentTokenBoundToPlaySession(t *testing.T) {
	p := newLiveProvider(t)
	p.playlist["/"+liveSecret+"/hls/index.m3u8"] = "#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n"
	p.segments["/"+liveSecret+"/hls/seg1.ts"] = []byte("SEG")
	env, _ := newLiveStreamEnv(t, p, liveTVStream{URL: p.URL + "/" + liveSecret + "/hls/index.m3u8", Container: "hls", Mode: "server"})
	id := liveChannelIDFor("m3u:news.hd")

	_, a := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser)
	_, b := livePlaybackInfo(t, env.router, id, liveTVProfileBrowser)
	if a.PlaySessionID == "" || a.PlaySessionID == b.PlaySessionID {
		t.Fatalf("play sessions %q and %q", a.PlaySessionID, b.PlaySessionID)
	}
	rec := serveLiveMedia(t, env.router, http.MethodGet, a.MediaSources[0].TranscodingURL)
	segA := relayPath(t, a.MediaSources[0].TranscodingURL, playlistURIs(rec.Body.String())[0])
	if rec := serveLiveMedia(t, env.router, http.MethodGet, segA); rec.Code != http.StatusOK {
		t.Fatalf("segment with its own play session = %d", rec.Code)
	}
	swapped := strings.Replace(segA, "PlaySessionId="+a.PlaySessionID, "PlaySessionId="+b.PlaySessionID, 1)
	if rec := serveLiveMedia(t, env.router, http.MethodGet, swapped); rec.Code != http.StatusNotFound {
		t.Errorf("token of play A with PlaySessionId B = %d, want 404", rec.Code)
	}
	if rec := serveLiveMedia(t, env.router, http.MethodGet, "/Videos/"+id+"/live/deadbeef?MediaSourceId="+id+"&PlaySessionId="+a.PlaySessionID); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", rec.Code)
	}
	// Without a play session the relay is not authorized at all.
	if rec := serveLiveMedia(t, env.router, http.MethodGet, "/Videos/"+id+"/live.m3u8"); rec.Code != http.StatusUnauthorized {
		t.Errorf("playlist without credentials = %d, want 401", rec.Code)
	}
	if rec := serveLive(t, env.router, http.MethodGet, "/Videos/"+id+"/live.m3u8", liveTokenP1); rec.Code != http.StatusNotFound {
		t.Errorf("playlist without PlaySessionId = %d, want 404", rec.Code)
	}
}

func TestLivePlaysAndRelayAreBoundedAndExpire(t *testing.T) {
	l, clock := newTestLiveTV(&fakeLiveTVRoutes{})
	for i := 0; i <= liveTVMaxPlays; i++ {
		l.putPlay(livePlay{PlaySessionID: fmt.Sprint("ps", i)})
		clock.Advance(1)
	}
	if n := len(l.plays); n != liveTVMaxPlays {
		t.Errorf("plays = %d, want capped at %d", n, liveTVMaxPlays)
	}
	if _, ok := l.play("ps0"); ok {
		t.Error("the oldest play was not evicted")
	}
	if _, ok := l.play(fmt.Sprint("ps", liveTVMaxPlays)); !ok {
		t.Error("the newest play is missing")
	}

	for i := 0; i <= liveTVMaxRelayEntries; i++ {
		if _, err := l.registerRelay(fmt.Sprint("http://h/", i), "ps", nil); err != nil {
			t.Fatal(err)
		}
		clock.Advance(1)
	}
	if n, m := len(l.relay), len(l.relayByURL); n != liveTVMaxRelayEntries || m != liveTVMaxRelayEntries {
		t.Errorf("relay = %d (by URL %d), want capped at %d", n, m, liveTVMaxRelayEntries)
	}
	tok1, _ := l.registerRelay("http://h/same", "ps", nil)
	tok2, _ := l.registerRelay("http://h/same", "ps", nil)
	tok3, _ := l.registerRelay("http://h/same", "other", nil)
	if tok1 != tok2 || tok1 == tok3 {
		t.Errorf("tokens %s %s %s: want one stable token per play and URL", tok1, tok2, tok3)
	}

	clock.Advance(liveTVPlayTTL / 2)
	if _, ok := l.relayEntry(tok1); !ok {
		t.Fatal("entry expired early")
	}
	clock.Advance(liveTVPlayTTL / 2)
	if _, ok := l.relayEntry(tok1); !ok {
		t.Error("a read did not slide the entry's expiry")
	}
	clock.Advance(liveTVPlayTTL + 1)
	if _, ok := l.relayEntry(tok1); ok {
		t.Error("entry outlived its TTL")
	}
	if _, ok := l.play(fmt.Sprint("ps", liveTVMaxPlays)); ok {
		t.Error("play outlived its TTL")
	}
}

func TestLiveStreamWrapperPassesCatalogIDsThrough(t *testing.T) {
	env := newLiveTestEnv(t, true)
	h := newLiveTVHandlers(env.bridge, "server-1", nil, nil, nil)
	codec := NewResourceIDCodec()
	session := &Session{StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}

	for _, wrapped := range []*liveTVHandlers{h, nil, {}} {
		for _, id := range []string{codec.EncodeIntID(EncodedIDMediaSource, 3), codec.EncodeStringID(EncodedIDItem, "movie-1")} {
			for route, wrap := range map[string]func(http.HandlerFunc) http.HandlerFunc{
				"/Videos/{id}/stream.{container}": wrapped.wrapVideoStream,
				"/Items/{id}/PlaybackInfo":        wrapped.wrapPlaybackInfo,
			} {
				called := false
				next := func(w http.ResponseWriter, r *http.Request) { called = true }
				path := strings.NewReplacer("{id}", id, "{container}", "ts").Replace(route)
				rec := liveWrapperRequest(t, func(r chi.Router) { r.Get(route, wrap(next)) }, path, session)
				if !called || rec.Code != http.StatusOK {
					t.Errorf("wrapper %v %s: next not called for %s (status %d)", wrapped, route, id, rec.Code)
				}
			}
		}
	}
}

func TestLivePlaybackRoutesAbsentWithoutBridge(t *testing.T) {
	router := newLiveTestRouter(t, nil)
	id := liveChannelIDFor("m3u:news.hd")
	for _, path := range []string{"/Videos/" + id + "/live.m3u8", "/Videos/" + id + "/live/abc"} {
		if rec := serveLive(t, router, http.MethodGet, path, liveTokenP1); rec.Code != http.StatusNotFound {
			t.Errorf("%s without a bridge = %d, want 404", path, rec.Code)
		}
	}
	rec, _ := livePlaybackInfo(t, router, id, liveTVProfileTV)
	if strings.Contains(rec.Body.String(), "IsInfiniteStream\":true") {
		t.Errorf("PlaybackInfo without a bridge answered as Live TV: %s", rec.Body.String())
	}
}
