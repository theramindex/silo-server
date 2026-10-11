// theramindex fork: plays Live TV channels: PlaybackInfo, TS redirect or relay, and HLS relay.

package jellycompat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

const (
	// liveTVPlayTTL is how long a play lives after its last playlist, segment
	// or stream request, long enough for a paused player to resume.
	liveTVPlayTTL = 30 * time.Minute
	// liveTVRelayTTL is how long a relay token lives after its last use.
	// Players refresh a live playlist every few seconds, and a refresh
	// re-registers every URI it still lists.
	liveTVRelayTTL = 2 * time.Minute
	// liveTVMaxPlays and liveTVMaxRelayEntries cap the tables; the entry that
	// expires soonest is evicted first.
	liveTVMaxPlays        = 4096
	liveTVMaxRelayEntries = 20000
	// liveTVSweepInterval is how often an insert sweeps expired entries from a
	// table that is not full.
	liveTVSweepInterval = 30 * time.Second
	// liveTVMaxPlaylistBytes bounds one playlist read.
	liveTVMaxPlaylistBytes = 8 << 20
	// liveTVStreamHeaderTimeout bounds the wait for a provider's response
	// headers. A live body has no end, so there is no overall timeout.
	liveTVStreamHeaderTimeout = 15 * time.Second
	liveTVMaxRedirects        = 5
	// liveTVFlushEvery is how many relayed TS bytes are buffered before a flush.
	liveTVFlushEvery = 64 << 10

	// liveTVPlaybackErrorNotAllowed is Jellyfin's PlaybackErrorCode for a
	// stream the server may not play.
	liveTVPlaybackErrorNotAllowed = "NotAllowed"
)

var (
	errLiveTVRedirect      = errors.New("livetv: redirect refused")
	errLiveTVBadPlaylist   = errors.New("livetv: malformed playlist")
	errLiveTVPlaylistLarge = errors.New("livetv: playlist too large")
)

// errLiveTVRelayHost names the host a playlist pointed at outside the allowed
// set. The host is safe to log; the URL is not.
type errLiveTVRelayHost struct{ host string }

func (e errLiveTVRelayHost) Error() string { return "livetv: relay host not allowed: " + e.host }

// livePlay is one PlaybackInfo negotiation for a channel. Stream.URL may embed
// credentials and never leaves the server in server mode.
type livePlay struct {
	PlaySessionID string
	Channel       liveChannel
	Stream        liveTVStream
	Caller        LiveTVCaller
	Hosts         []string // info.StreamHosts
	Expires       time.Time
}

// liveRelayEntry is a provider URL a relayed playlist refers to by token.
// Hosts are the hosts that URL's own playlist may point at.
type liveRelayEntry struct {
	URL           string
	PlaySessionID string
	Hosts         []string
	Expires       time.Time
}

// newLiveStreamClient returns the client for provider streams: no overall
// timeout, a response-header timeout, and at most liveTVMaxRedirects http(s)
// redirects. A request carries its play's allowed hosts (withLiveHosts); a
// redirect may leave them only for a host that resolves to public addresses,
// and liveStreamTransport enforces that again on every dial.
func newLiveStreamClient() *http.Client {
	return &http.Client{
		Transport: &liveStreamTransport{allowed: newLiveGuardedTransport(true), public: newLiveGuardedTransport(false)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > liveTVMaxRedirects || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
				return errLiveTVRedirect
			}
			ctx := req.Context()
			if !liveHostAllowed(liveHostsFrom(ctx), req.URL.Host) && netguard.CheckURL(ctx, req.URL.String(), false) != nil {
				return errLiveTVRedirect
			}
			return nil
		},
	}
}

type liveHostsKey struct{}

// withLiveHosts marks requests made with ctx as allowed to reach hosts, which
// may be on the server's own network.
func withLiveHosts(ctx context.Context, hosts []string) context.Context {
	return context.WithValue(ctx, liveHostsKey{}, hosts)
}

func liveHostsFrom(ctx context.Context) []string {
	hosts, _ := ctx.Value(liveHostsKey{}).([]string)
	return hosts
}

// liveStreamTransport sends a request for one of its play's allowed hosts
// through a pool that may dial private addresses, and every other request
// through a pool that dials public addresses only. Checking at dial time
// covers DNS answers that change after a redirect was checked, and the pools
// never share connections.
type liveStreamTransport struct {
	allowed *http.Transport
	public  *http.Transport
}

func (t *liveStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if liveHostAllowed(liveHostsFrom(req.Context()), req.URL.Host) {
		return t.allowed.RoundTrip(req)
	}
	return t.public.RoundTrip(req)
}

// newLiveGuardedTransport mirrors netguard's guarded transport with the
// stream response-header timeout. Blocked addresses are refused either way.
func newLiveGuardedTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("checking dial address: %w", err)
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("checking dial address: %w", err)
			}
			return netguard.CheckAddr(addr, allowPrivate)
		},
	}
	return &http.Transport{
		// No proxy: the dialer checks the address it connects to.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: liveTVStreamHeaderTimeout,
	}
}

// pruneLiveTable makes room for one more entry under limit. Expired entries
// are swept when the table is full or *lastSweep is liveTVSweepInterval old,
// so an insert does not always scan the table; lookups check expiry
// themselves. A full table then loses its soonest-expiring entries. onDelete
// runs for every removed entry.
func pruneLiveTable[V any](m map[string]V, now time.Time, lastSweep *time.Time, limit int, expires func(V) time.Time, onDelete func(string, V)) {
	if len(m) >= limit || now.Sub(*lastSweep) >= liveTVSweepInterval {
		*lastSweep = now
		for k, v := range m {
			if !expires(v).After(now) {
				delete(m, k)
				onDelete(k, v)
			}
		}
	}
	for len(m) >= limit {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, v := range m {
			if e := expires(v); first || e.Before(oldest) {
				oldestKey, oldest, first = k, e, false
			}
		}
		v := m[oldestKey]
		delete(m, oldestKey)
		onDelete(oldestKey, v)
	}
}

func (l *LiveTV) putPlay(p livePlay) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	now := l.now()
	pruneLiveTable(l.plays, now, &l.playsSwept, liveTVMaxPlays,
		func(p *livePlay) time.Time { return p.Expires }, func(string, *livePlay) {})
	p.Expires = now.Add(liveTVPlayTTL)
	l.plays[p.PlaySessionID] = &p
}

// play returns a live play by id and slides its expiry.
func (l *LiveTV) play(id string) (livePlay, bool) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	p, ok := l.plays[id]
	now := l.now()
	if !ok || !p.Expires.After(now) {
		return livePlay{}, false
	}
	p.Expires = now.Add(liveTVPlayTTL)
	return *p, true
}

func liveRelayKey(playSessionID, rawURL string) string { return playSessionID + "\x00" + rawURL }

// registerRelay returns the token for a provider URL within a play, reusing
// the play's existing token for the same URL so playlist refreshes keep
// stable segment URIs.
func (l *LiveTV) registerRelay(rawURL, playSessionID string, hosts []string) (string, error) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	now := l.now()
	key := liveRelayKey(playSessionID, rawURL)
	if token, ok := l.relayByURL[key]; ok {
		if e := l.relay[token]; e != nil && e.Expires.After(now) {
			e.Expires = now.Add(liveTVRelayTTL)
			return token, nil
		}
	}
	pruneLiveTable(l.relay, now, &l.relaySwept, liveTVMaxRelayEntries,
		func(e *liveRelayEntry) time.Time { return e.Expires },
		func(token string, e *liveRelayEntry) {
			if k := liveRelayKey(e.PlaySessionID, e.URL); l.relayByURL[k] == token {
				delete(l.relayByURL, k)
			}
		})
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	l.relay[token] = &liveRelayEntry{URL: rawURL, PlaySessionID: playSessionID, Hosts: hosts, Expires: now.Add(liveTVRelayTTL)}
	l.relayByURL[key] = token
	return token, nil
}

// relayEntry returns a relay entry by token and slides its expiry.
func (l *LiveTV) relayEntry(token string) (liveRelayEntry, bool) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	e, ok := l.relay[token]
	now := l.now()
	if !ok || !e.Expires.After(now) {
		return liveRelayEntry{}, false
	}
	e.Expires = now.Add(liveTVRelayTTL)
	return *e, true
}

// deviceProfilePlaysTS reports whether a profile direct-plays MPEG-TS video.
func deviceProfilePlaysTS(p DeviceProfile) bool {
	for _, dp := range p.DirectPlayProfiles {
		if dp.Type != "" && !strings.EqualFold(dp.Type, "Video") {
			continue
		}
		for part := range strings.SplitSeq(dp.Container, ",") {
			if c := normalizeCompatToken(part); c == "mpegts" || c == "m2ts" {
				return true
			}
		}
	}
	return false
}

func liveMediaQuery(channelID, playSessionID string) string {
	return "MediaSourceId=" + url.QueryEscape(channelID) + "&PlaySessionId=" + url.QueryEscape(playSessionID)
}

func livePlaylistPath(channelID, playSessionID string) string {
	return "/Videos/" + channelID + "/live.m3u8?" + liveMediaQuery(channelID, playSessionID)
}

// liveMediaSource is the PlaybackInfo source for a play.
func liveMediaSource(play livePlay) mediaSourceDTO {
	id := play.Channel.EncodedID
	src := mediaSourceDTO{
		ID:                     id,
		Name:                   play.Channel.Name,
		Protocol:               "Http",
		Type:                   "Default",
		IsInfiniteStream:       true,
		Formats:                []string{},
		RequiredHTTPHeaders:    map[string]string{},
		MediaAttachments:       []map[string]any{},
		TranscodingSubProtocol: "hls",
	}
	if play.Stream.Container == "hls" {
		src.Container = "hls"
		src.SupportsTranscoding = true
		src.TranscodingURL = livePlaylistPath(id, play.PlaySessionID)
		src.TranscodingContainer = "ts"
		return src
	}
	src.Container = "ts"
	src.SupportsDirectPlay = true
	src.SupportsDirectStream = true
	src.DirectStreamURL = "/Videos/" + id + "/stream.ts?Static=true&" + liveMediaQuery(id, play.PlaySessionID)
	src.Path = src.DirectStreamURL
	return src
}

func writeLivePlaybackError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusOK, playbackInfoResponseDTO{ErrorCode: code, MediaSources: []mediaSourceDTO{}})
}

// wrapPlaybackInfo answers PlaybackInfo for a live channel and passes every
// other id to next.
func (h *liveTVHandlers) wrapPlaybackInfo(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "id")
		if !isLiveChannelID(rawID) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		if s == nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
			return
		}
		_, profile, err := h.playback.parsePlaybackRequest(r, s.Token)
		if err != nil {
			writeDeviceProfileRequestError(w, err, "Invalid playback request")
			return
		}
		ctx := r.Context()
		caller := liveCaller(s)
		ch, ok := h.bridge.ChannelByID(ctx, caller, rawID)
		if !ok {
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		}
		format := "any"
		if !deviceProfilePlaysTS(profile) {
			format = "hls"
		}
		stream, err := h.bridge.ResolveStream(ctx, caller, ch, format)
		switch {
		case err == nil:
		case errors.Is(err, errLiveTVFormatUnavailable):
			writeLivePlaybackError(w, playbackErrorNoCompatibleStream)
			return
		case errors.Is(err, errLiveTVHostNotAllowed):
			slog.WarnContext(ctx, "jellycompat: livetv stream refused: host not allowed",
				"component", "jellycompat", "installation_id", ch.InstallationID, "channel_id", ch.EncodedID)
			writeLivePlaybackError(w, liveTVPlaybackErrorNotAllowed)
			return
		case errors.Is(err, errLiveTVChannelNotFound):
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		default:
			// ResolveStream errors never carry the stream URL.
			slog.WarnContext(ctx, "jellycompat: livetv stream unavailable",
				"component", "jellycompat", "installation_id", ch.InstallationID, "channel_id", ch.EncodedID, "error", err)
			writeLivePlaybackError(w, playbackErrorNoCompatibleStream)
			return
		}
		if format == "hls" && stream.Container != "hls" {
			writeLivePlaybackError(w, playbackErrorNoCompatibleStream)
			return
		}
		info, err := h.bridge.Info(ctx, caller, ch.InstallationID)
		if err != nil {
			slog.WarnContext(ctx, "jellycompat: livetv provider info unavailable",
				"component", "jellycompat", "installation_id", ch.InstallationID, "error", err)
			writeLivePlaybackError(w, playbackErrorNoCompatibleStream)
			return
		}

		play := livePlay{
			PlaySessionID: h.playback.codec.EncodeStringID(EncodedIDPlaySession, uuidNewString()),
			Channel:       ch,
			Stream:        stream,
			Caller:        caller,
			Hosts:         info.StreamHosts,
		}
		h.bridge.putPlay(play)

		// Registered so upstream PlaybackSessionAuth grants the stream and relay
		// URLs, which carry only MediaSourceId and PlaySessionId. The device,
		// address and peer are filled as HandlePlaybackInfo fills them.
		clientDeviceID := firstNonEmpty(
			firstMediaBrowserAuthorizationValue(r, "DeviceId"),
			newCaseInsensitiveQuery(r.URL.Query()).Get("DeviceId"),
		)
		clientDeviceID = stripCompatNUL(clientDeviceID)
		h.playback.playbackStore.PutNegotiated(PlaybackSession{
			ID:             play.PlaySessionID,
			CompatToken:    s.Token,
			ClientDeviceID: clientDeviceID,
			ClientIP:       clientip.FromContext(r.Context()),
			ClientPeer:     requestPeerHost(r),
			ItemID:         ch.EncodedID,
			RouteItemID:    ch.EncodedID,
			UserID:         s.PseudoUserID.String(),
			MediaSources:   []PlaybackMediaSource{{ID: ch.EncodedID}},
		})
		slog.DebugContext(ctx, "jellycompat: livetv play negotiated",
			"component", "jellycompat", "installation_id", ch.InstallationID, "channel_id", ch.EncodedID,
			"container", stream.Container, "mode", stream.Mode)

		writeJSON(w, http.StatusOK, playbackInfoResponseDTO{
			PlaySessionID: play.PlaySessionID,
			MediaSources:  []mediaSourceDTO{liveMediaSource(play)},
		})
	}
}

// findPlay returns the play named by the request's PlaySessionId when it is
// for the route's channel and, with a session, the session's caller.
func (h *liveTVHandlers) findPlay(r *http.Request, rawID string) (livePlay, bool) {
	id := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
	if id == "" {
		return livePlay{}, false
	}
	play, ok := h.bridge.play(id)
	if !ok || !mediaSourceIDsEqual(play.Channel.EncodedID, rawID) {
		return livePlay{}, false
	}
	if s := SessionFromContext(r.Context()); s != nil && liveCaller(s) != play.Caller {
		return livePlay{}, false
	}
	return play, true
}

// wrapVideoStream serves /Videos/{id}/stream for a live channel and passes
// every other id to next. A TS play is redirected to the provider or relayed;
// an HLS play is sent to the relay playlist.
func (h *liveTVHandlers) wrapVideoStream(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "id")
		if !isLiveChannelID(rawID) {
			next(w, r)
			return
		}
		ctx := r.Context()
		play, ok := h.findPlay(r, rawID)
		if !ok {
			// A client that skipped PlaybackInfo, or whose play expired, gets a
			// fresh TS stream.
			s := SessionFromContext(ctx)
			if s == nil {
				writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
				return
			}
			ch, found := h.bridge.ChannelByID(ctx, liveCaller(s), rawID)
			if !found {
				writeError(w, http.StatusNotFound, "NotFound", "Item not found")
				return
			}
			stream, err := h.bridge.ResolveStream(ctx, liveCaller(s), ch, "ts")
			if errors.Is(err, errLiveTVChannelNotFound) {
				writeError(w, http.StatusNotFound, "NotFound", "Item not found")
				return
			}
			if err == nil && stream.Container != "ts" {
				err = fmt.Errorf("livetv: stream: want ts, got %q", stream.Container)
			}
			if err != nil {
				slog.WarnContext(ctx, "jellycompat: livetv stream unavailable",
					"component", "jellycompat", "installation_id", ch.InstallationID, "channel_id", ch.EncodedID, "error", err)
				writeError(w, http.StatusBadGateway, "UpstreamError", "Stream unavailable")
				return
			}
			play = livePlay{Channel: ch, Stream: stream, Caller: liveCaller(s)}
			if info, err := h.bridge.Info(ctx, play.Caller, ch.InstallationID); err == nil {
				play.Hosts = info.StreamHosts
			}
		}

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "video/mp2t")
			w.WriteHeader(http.StatusOK)
			return
		}
		switch {
		case play.Stream.Container == "ts" && play.Stream.Mode == "redirect":
			http.Redirect(w, r, play.Stream.URL, http.StatusFound)
		case play.Stream.Container == "ts":
			h.relayTS(w, r, play)
		default:
			http.Redirect(w, r, livePlaylistPath(play.Channel.EncodedID, play.PlaySessionID), http.StatusFound)
		}
	}
}

// liveFetchError strips the URL a *url.Error carries, which may hold
// credentials.
func liveFetchError(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}

func liveURLHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Host
	}
	return ""
}

// fetchLive GETs a provider URL with the stream client and requires a 2xx
// answer. The URL's own host and hosts may be on the server's network; a
// redirect elsewhere must reach a public address. Failures are logged with the
// channel and host only.
func (h *liveTVHandlers) fetchLive(r *http.Request, play livePlay, rawURL string, hosts []string) (*http.Response, bool) {
	ctx := r.Context()
	logFailure := func(attrs ...any) {
		slog.WarnContext(ctx, "jellycompat: livetv fetch failed", append([]any{
			"component", "jellycompat", "installation_id", play.Channel.InstallationID,
			"channel_id", play.Channel.EncodedID, "host", liveURLHost(rawURL),
		}, attrs...)...)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		logFailure("error", "bad url")
		return nil, false
	}
	allowed := slices.Clone(hosts)
	if !liveHostAllowed(allowed, req.URL.Host) {
		allowed = append(allowed, req.URL.Host)
	}
	req = req.WithContext(withLiveHosts(ctx, allowed))
	resp, err := h.bridge.streamClient.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			logFailure("error", liveFetchError(err))
		}
		return nil, false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		logFailure("status", resp.StatusCode)
		return nil, false
	}
	return resp, true
}

// newLiveStreamWriter wraps a relayed body's writer so a client that stops
// reading, even without closing, is cut off within the stall window instead of
// holding a provider connection until TCP gives up.
func newLiveStreamWriter(w http.ResponseWriter) http.ResponseWriter {
	return httpstream.NewRollingDeadlineWriter(w)
}

// liveFlushWriter flushes the response every liveTVFlushEvery bytes so a live
// stream reaches the player as it arrives.
type liveFlushWriter struct {
	w         io.Writer
	rc        *http.ResponseController
	unflushed int
}

func (f *liveFlushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	f.unflushed += n
	if err == nil && f.unflushed >= liveTVFlushEvery {
		f.unflushed = 0
		err = f.rc.Flush()
	}
	return n, err
}

// relayTS copies a server-mode TS stream to the client. The rolling write
// deadline ends the relay, and frees the provider stream, when the client
// stops reading.
func (h *liveTVHandlers) relayTS(w http.ResponseWriter, r *http.Request, play livePlay) {
	resp, ok := h.fetchLive(r, play, play.Stream.URL, play.Hosts)
	if !ok {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Stream unavailable")
		return
	}
	defer resp.Body.Close()
	ctx := r.Context()
	slog.DebugContext(ctx, "jellycompat: livetv relay started",
		"component", "jellycompat", "channel_id", play.Channel.EncodedID)
	rw := newLiveStreamWriter(w)
	rw.Header().Set("Content-Type", "video/mp2t")
	rw.WriteHeader(http.StatusOK)
	fw := &liveFlushWriter{w: rw, rc: http.NewResponseController(rw)}
	n, _ := io.Copy(fw, resp.Body)
	_ = fw.rc.Flush()
	slog.DebugContext(ctx, "jellycompat: livetv relay ended",
		"component", "jellycompat", "channel_id", play.Channel.EncodedID, "bytes", n)
}

// readLivePlaylist reads at most liveTVMaxPlaylistBytes of a playlist.
func readLivePlaylist(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveTVMaxPlaylistBytes+1))
	if err != nil {
		return nil, liveFetchError(err)
	}
	if len(body) > liveTVMaxPlaylistBytes {
		return nil, errLiveTVPlaylistLarge
	}
	return body, nil
}

// relayPlaylist rewrites a fetched playlist so every URI points at the relay.
// URIs may use the play's hosts or the host the playlist was finally served
// from. prefix is the path from the playlist's own relay URL to the token
// route.
func (h *liveTVHandlers) relayPlaylist(w http.ResponseWriter, r *http.Request, play livePlay, hosts []string, resp *http.Response, prefix string) {
	defer resp.Body.Close()
	body, err := readLivePlaylist(resp)
	if err == nil {
		base := resp.Request.URL
		allowed := slices.Clone(hosts)
		if !liveHostAllowed(allowed, base.Host) {
			allowed = append(allowed, base.Host)
		}
		query := liveMediaQuery(play.Channel.EncodedID, play.PlaySessionID)
		body, err = rewriteLivePlaylist(body, base, func(abs *url.URL) (string, error) {
			if (abs.Scheme != "http" && abs.Scheme != "https") || !liveHostAllowed(allowed, abs.Host) {
				return "", errLiveTVRelayHost{host: abs.Host}
			}
			token, err := h.bridge.registerRelay(abs.String(), play.PlaySessionID, allowed)
			if err != nil {
				return "", err
			}
			return prefix + token + "?" + query, nil
		})
	}
	if err != nil {
		slog.WarnContext(r.Context(), "jellycompat: livetv playlist relay failed",
			"component", "jellycompat", "installation_id", play.Channel.InstallationID,
			"channel_id", play.Channel.EncodedID, "error", err)
		writeError(w, http.StatusBadGateway, "UpstreamError", "Stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// HandleLivePlaylist serves GET /Videos/{id}/live.m3u8, the relayed playlist
// of an HLS play.
func (h *liveTVHandlers) HandleLivePlaylist(w http.ResponseWriter, r *http.Request) {
	play, ok := h.findPlay(r, chi.URLParam(r, "id"))
	if !ok || play.Stream.Container != "hls" {
		writeError(w, http.StatusNotFound, "NotFound", "Stream not found")
		return
	}
	resp, ok := h.fetchLive(r, play, play.Stream.URL, play.Hosts)
	if !ok {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Stream unavailable")
		return
	}
	h.relayPlaylist(w, r, play, play.Hosts, resp, "live/")
}

// HandleLiveSegment serves GET /Videos/{id}/live/{token}: a segment, key or
// child playlist a relayed playlist referred to.
func (h *liveTVHandlers) HandleLiveSegment(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.bridge.relayEntry(chi.URLParam(r, "token"))
	if !ok || entry.PlaySessionID != newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId") {
		writeError(w, http.StatusNotFound, "NotFound", "Stream not found")
		return
	}
	play, ok := h.findPlay(r, chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Stream not found")
		return
	}
	resp, ok := h.fetchLive(r, play, entry.URL, entry.Hosts)
	if !ok {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Stream unavailable")
		return
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") ||
		strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".m3u8") {
		// A child playlist is served from .../live/{token}, so its relayed URIs
		// are siblings of it.
		h.relayPlaylist(w, r, play, entry.Hosts, resp, "")
		return
	}
	defer resp.Body.Close()
	rw := newLiveStreamWriter(w)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		rw.Header().Set("Content-Type", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		rw.Header().Set("Content-Length", cl)
	}
	rw.WriteHeader(http.StatusOK)
	_, _ = io.Copy(rw, resp.Body)
}

// liveURITags are the playlist tags whose URI attribute names a resource. Every
// one is rewritten, since an attribute left alone could hand a client a
// provider URL.
var liveURITags = []string{
	"#EXT-X-KEY", "#EXT-X-MAP", "#EXT-X-MEDIA", "#EXT-X-I-FRAME-STREAM-INF",
	"#EXT-X-SESSION-KEY", "#EXT-X-SESSION-DATA", "#EXT-X-PART", "#EXT-X-PRELOAD-HINT", "#EXT-X-RENDITION-REPORT",
}

// rewriteLivePlaylist replaces every URI in an HLS playlist with what register
// returns for it, resolved against base. URI lines and the URI attribute of
// the liveURITags change; every other line, and every line ending, is kept
// byte for byte. Any URI register refuses fails the whole rewrite.
func rewriteLivePlaylist(body []byte, base *url.URL, register func(abs *url.URL) (string, error)) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(body))
	for rest := body; len(rest) > 0; {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		content := bytes.TrimRight(line, "\r\n")
		ending := line[len(content):]
		rewritten, err := rewriteLivePlaylistLine(content, base, register)
		if err != nil {
			return nil, err
		}
		out.Write(rewritten)
		out.Write(ending)
	}
	return out.Bytes(), nil
}

func rewriteLivePlaylistLine(line []byte, base *url.URL, register func(*url.URL) (string, error)) ([]byte, error) {
	trimmed := bytes.TrimSpace(line)
	switch {
	case len(trimmed) == 0:
		return line, nil
	case trimmed[0] != '#':
		ref, err := relayLiveURI(string(trimmed), base, register)
		return []byte(ref), err
	}
	if !slices.ContainsFunc(liveURITags, func(tag string) bool {
		return bytes.HasPrefix(trimmed, []byte(tag)) && len(trimmed) > len(tag) && trimmed[len(tag)] == ':'
	}) {
		return line, nil
	}
	const attr = `URI="`
	for from := 0; ; {
		i := bytes.Index(line[from:], []byte(attr))
		if i < 0 {
			return line, nil
		}
		i += from
		if c := line[i-1]; c != ':' && c != ',' {
			from = i + 1
			continue
		}
		start := i + len(attr)
		end := bytes.IndexByte(line[start:], '"')
		if end < 0 {
			return nil, errLiveTVBadPlaylist
		}
		end += start
		ref, err := relayLiveURI(string(line[start:end]), base, register)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(line)-(end-start)+len(ref))
		out = append(out, line[:start]...)
		out = append(out, ref...)
		return append(out, line[end:]...), nil
	}
}

func relayLiveURI(raw string, base *url.URL, register func(*url.URL) (string, error)) (string, error) {
	ref, err := url.Parse(raw)
	if err != nil {
		// url.Parse errors quote the input.
		return "", errLiveTVBadPlaylist
	}
	return register(base.ResolveReference(ref))
}
