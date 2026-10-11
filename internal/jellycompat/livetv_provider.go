// theramindex fork: discovers livetv/v1 plugin providers and merges their lineups.

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// liveTVCacheTTL bounds how long the provider list, info and lineups are
	// reused before the next call refreshes them.
	liveTVCacheTTL = 60 * time.Second
	// liveTVFetchTimeout bounds a shared refresh. Waiters share one provider
	// call, so it must not die with whichever request happened to start it.
	liveTVFetchTimeout = 15 * time.Second
	// liveTVFailureBackoff is how long an installation is left alone after a
	// failed info or channels refresh, so a down provider is not retried by
	// every request.
	liveTVFailureBackoff = 10 * time.Second
	// liveTVForcedRefreshInterval limits how often a channel-id miss may force a
	// lineup refresh for one installation, however many callers miss.
	liveTVForcedRefreshInterval = 10 * time.Second
)

var (
	errLiveTVFormatUnavailable = errors.New("livetv: format unavailable")
	errLiveTVChannelNotFound   = errors.New("livetv: channel not found")
	errLiveTVHostNotAllowed    = errors.New("livetv: stream host not allowed")
)

// LiveTVProviderRef names one installed plugin that serves livetv/v1.
type LiveTVProviderRef struct {
	InstallationID int
	PluginID       string
}

// LiveTVCaller is the identity forwarded to a provider. The zero value is
// anonymous.
type LiveTVCaller struct {
	UserID    int
	Admin     bool
	ProfileID string
}

// LiveTVRouteCaller is the host side of the bridge: it finds the plugin
// installations that serve livetv/v1 and calls their HTTP routes in-process.
type LiveTVRouteCaller interface {
	ListProviders(ctx context.Context) ([]LiveTVProviderRef, error)
	Call(ctx context.Context, installationID int, caller LiveTVCaller, method, path string, query map[string]string) (status int, body []byte, err error)
}

// LiveTVPrefsStore reads and writes a user's plugin settings for one
// installation.
type LiveTVPrefsStore interface {
	Get(ctx context.Context, userID, installationID int) (map[string]string, error)
	Set(ctx context.Context, userID, installationID int, values map[string]string) error
}

// liveChannel is a provider channel tagged with the installation that serves
// it and the compat id clients see.
type liveChannel struct {
	liveTVChannel
	InstallationID int
	EncodedID      string // encodeLiveChannelID(InstallationID, ID)
}

// liveLineup is one installation's channels in provider order plus an index
// for id lookups. It is never mutated after it is built.
type liveLineup struct {
	channels []liveChannel
	index    map[liveChannelKey]liveChannel
}

type liveCacheEntry[T any] struct {
	val T
	at  time.Time
	ok  bool
	// failedAt and failErr record the last failed refresh. The entry is not
	// refreshed again until liveTVFailureBackoff has passed.
	failedAt time.Time
	failErr  error
}

type liveInstall struct {
	info   liveCacheEntry[liveTVInfo]
	lineup liveCacheEntry[liveLineup]
	// lastForced is when a channel-id miss last forced a lineup refresh.
	lastForced time.Time
}

// LiveTV merges the lineups of every installed livetv/v1 provider and resolves
// stream URLs against them.
type LiveTV struct {
	routes     LiveTVRouteCaller
	prefs      LiveTVPrefsStore
	httpClient *http.Client // nil means a default client
	now        func() time.Time

	group singleflight.Group

	mu        sync.Mutex
	providers liveCacheEntry[[]LiveTVProviderRef]
	installs  map[int]*liveInstall

	// prefLocks maps a [2]int{userID, installationID} to the *sync.Mutex that
	// serializes preference writes for that account.
	prefLocks sync.Map

	// streamClient fetches provider streams and playlists; see
	// newLiveStreamClient. plays and relay hold negotiated plays and relayed
	// HLS URLs by token, relayByURL maps a play and URL to its token, and the
	// *Swept times record each table's last expiry sweep. All are guarded by
	// streamMu; see livetv_stream.go.
	streamClient *http.Client
	streamMu     sync.Mutex
	plays        map[string]*livePlay
	relay        map[string]*liveRelayEntry
	relayByURL   map[string]string
	playsSwept   time.Time
	relaySwept   time.Time
}

// NewLiveTV returns a LiveTV over routes. httpClient fetches provider stream
// URLs; nil means a default client.
func NewLiveTV(routes LiveTVRouteCaller, prefs LiveTVPrefsStore, httpClient *http.Client) *LiveTV {
	return &LiveTV{
		routes:       routes,
		prefs:        prefs,
		httpClient:   httpClient,
		now:          time.Now,
		installs:     map[int]*liveInstall{},
		streamClient: newLiveStreamClient(httpClient),
		plays:        map[string]*livePlay{},
		relay:        map[string]*liveRelayEntry{},
		relayByURL:   map[string]string{},
	}
}

// Active reports whether at least one provider is installed.
func (l *LiveTV) Active(ctx context.Context) bool {
	return len(l.Providers(ctx)) > 0
}

// Providers returns the installed providers in host order. The list is cached
// for liveTVCacheTTL. A failed refresh keeps the previous list and still starts
// a new TTL, so clients do not see Live TV flicker while the host recovers.
func (l *LiveTV) Providers(ctx context.Context) []LiveTVProviderRef {
	if l.routes == nil {
		return nil
	}
	l.mu.Lock()
	if l.providers.ok && l.now().Sub(l.providers.at) < liveTVCacheTTL {
		list := slices.Clone(l.providers.val)
		l.mu.Unlock()
		return list
	}
	l.mu.Unlock()

	resCh := l.group.DoChan("providers", func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveTVFetchTimeout)
		defer cancel()
		list, err := l.routes.ListProviders(fetchCtx)

		l.mu.Lock()
		defer l.mu.Unlock()
		l.providers.at = l.now()
		if err != nil {
			slog.WarnContext(ctx, "jellycompat: livetv provider discovery failed; keeping the previous list",
				"component", "jellycompat", "error", err)
			// Cache even when there is no earlier list, so a failing host is
			// not asked again by every request.
			l.providers.ok = true
			return l.providers.val, nil
		}
		l.providers.val, l.providers.ok = list, true
		l.pruneInstallsLocked(list)
		return list, nil
	})
	select {
	case res := <-resCh:
		list, _ := res.Val.([]LiveTVProviderRef)
		return slices.Clone(list)
	case <-ctx.Done():
		l.mu.Lock()
		defer l.mu.Unlock()
		return slices.Clone(l.providers.val)
	}
}

// pruneInstallsLocked forgets the caches of installations that are no longer
// providers.
func (l *LiveTV) pruneInstallsLocked(providers []LiveTVProviderRef) {
	keep := make(map[int]struct{}, len(providers))
	for _, p := range providers {
		keep[p.InstallationID] = struct{}{}
	}
	for id := range l.installs {
		if _, ok := keep[id]; !ok {
			delete(l.installs, id)
		}
	}
}

func (l *LiveTV) installLocked(installationID int) *liveInstall {
	st := l.installs[installationID]
	if st == nil {
		st = &liveInstall{}
		l.installs[installationID] = st
	}
	return st
}

// loadCached returns the value cached by slot for an installation, refreshing
// it through fetch when it is older than liveTVCacheTTL or force is set.
// Concurrent refreshes of one value share a single fetch, and a waiter whose
// ctx ends stops waiting without cancelling it. fetched reports that the value
// came from a fetch made during this call. When a refresh fails, or the caller
// gives up, the stale copy is returned with a nil error; err is non-nil only
// when there is no copy at all. After a failed refresh the installation is left
// alone for liveTVFailureBackoff, forced or not.
func loadCached[T any](
	ctx context.Context,
	l *LiveTV,
	installationID int,
	what string,
	slot func(*liveInstall) *liveCacheEntry[T],
	force bool,
	fetch func(context.Context) (T, error),
) (val T, fetched bool, err error) {
	// fallback is what a caller gets when it cannot have a fresh value.
	fallback := func(failure error) (T, bool, error) {
		l.mu.Lock()
		entry := slot(l.installLocked(installationID))
		stale, haveStale := entry.val, entry.ok
		l.mu.Unlock()
		if haveStale {
			return stale, false, nil
		}
		var zero T
		return zero, false, failure
	}

	l.mu.Lock()
	entry := slot(l.installLocked(installationID))
	now := l.now()
	if !force && entry.ok && now.Sub(entry.at) < liveTVCacheTTL {
		val = entry.val
		l.mu.Unlock()
		return val, false, nil
	}
	if !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < liveTVFailureBackoff {
		failure := entry.failErr
		l.mu.Unlock()
		return fallback(failure)
	}
	l.mu.Unlock()

	resCh := l.group.DoChan(fmt.Sprintf("%s:%d", what, installationID), func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveTVFetchTimeout)
		defer cancel()
		fresh, err := fetch(fetchCtx)

		l.mu.Lock()
		entry := slot(l.installLocked(installationID))
		if err != nil {
			entry.failedAt, entry.failErr = l.now(), err
			stale := entry.ok
			l.mu.Unlock()
			slog.WarnContext(ctx, "jellycompat: livetv provider refresh failed",
				"component", "jellycompat", "installation_id", installationID, "what", what, "stale", stale, "error", err)
			return nil, err
		}
		entry.val, entry.at, entry.ok = fresh, l.now(), true
		entry.failedAt, entry.failErr = time.Time{}, nil
		l.mu.Unlock()
		return fresh, nil
	})
	select {
	case res := <-resCh:
		if res.Err != nil {
			return fallback(res.Err)
		}
		return res.Val.(T), true, nil
	case <-ctx.Done():
		return fallback(ctx.Err())
	}
}

// getJSON calls a provider route and decodes a 200 response into out.
func (l *LiveTV) getJSON(ctx context.Context, caller LiveTVCaller, installationID int, path string, query map[string]string, out any) error {
	status, body, err := l.routes.Call(ctx, installationID, caller, http.MethodGet, path, query)
	if err != nil {
		return fmt.Errorf("livetv: %s: %w", path, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("livetv: %s: status %d", path, status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("livetv: %s: decode: %w", path, err)
	}
	return nil
}

func (l *LiveTV) fetchLineup(ctx context.Context, caller LiveTVCaller, installationID int) (liveLineup, error) {
	var raw []liveTVChannel
	if err := l.getJSON(ctx, caller, installationID, liveTVPathChannels, nil, &raw); err != nil {
		return liveLineup{}, err
	}
	lineup := liveLineup{
		channels: make([]liveChannel, 0, len(raw)),
		index:    make(map[liveChannelKey]liveChannel, len(raw)),
	}
	for _, c := range raw {
		if c.ID == "" {
			continue
		}
		key := liveChannelKey{InstallationID: installationID, Hash: liveChannelHash(c.ID)}
		if _, dup := lineup.index[key]; dup {
			continue
		}
		ch := liveChannel{
			liveTVChannel:  c,
			InstallationID: installationID,
			EncodedID:      encodeLiveChannelID(installationID, c.ID),
		}
		lineup.channels = append(lineup.channels, ch)
		lineup.index[key] = ch
	}
	return lineup, nil
}

// lineupFor returns one installation's lineup. A provider that fails with no
// earlier copy contributes an empty lineup.
func (l *LiveTV) lineupFor(ctx context.Context, caller LiveTVCaller, installationID int, force bool) (lineup liveLineup, fetched bool) {
	lineup, fetched, _ = loadCached(ctx, l, installationID, "channels",
		func(st *liveInstall) *liveCacheEntry[liveLineup] { return &st.lineup },
		force,
		func(ctx context.Context) (liveLineup, error) { return l.fetchLineup(ctx, caller, installationID) })
	return lineup, fetched
}

// Lineup returns every provider's channels, providers in host order and
// channels in provider order. A failing provider is skipped.
func (l *LiveTV) Lineup(ctx context.Context, caller LiveTVCaller) []liveChannel {
	var out []liveChannel
	for _, p := range l.Providers(ctx) {
		lineup, _ := l.lineupFor(ctx, caller, p.InstallationID, false)
		out = append(out, lineup.channels...)
	}
	return out
}

// decodeProviderChannelID decodes a compat channel id and requires its
// installation to be a current provider.
func (l *LiveTV) decodeProviderChannelID(ctx context.Context, rawID string) (liveChannelKey, bool) {
	key, ok := decodeLiveChannelID(rawID)
	if !ok {
		return liveChannelKey{}, false
	}
	if !slices.ContainsFunc(l.Providers(ctx), func(p LiveTVProviderRef) bool { return p.InstallationID == key.InstallationID }) {
		return liveChannelKey{}, false
	}
	return key, true
}

// claimForcedRefresh reports whether a channel-id miss may force a lineup
// refresh for the installation now, and if so starts the liveTVForcedRefreshInterval
// window. The window is shared by all callers.
func (l *LiveTV) claimForcedRefresh(installationID int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.installLocked(installationID)
	now := l.now()
	if !st.lastForced.IsZero() && now.Sub(st.lastForced) < liveTVForcedRefreshInterval {
		return false
	}
	st.lastForced = now
	return true
}

// ChannelByID resolves a compat channel id. When the provider's cached lineup
// lacks the channel it refreshes that lineup, so a channel added since the last
// refresh is still playable. Such forced refreshes are limited to one per
// installation per liveTVForcedRefreshInterval.
func (l *LiveTV) ChannelByID(ctx context.Context, caller LiveTVCaller, rawID string) (liveChannel, bool) {
	key, ok := l.decodeProviderChannelID(ctx, rawID)
	if !ok {
		return liveChannel{}, false
	}
	lineup, fetched := l.lineupFor(ctx, caller, key.InstallationID, false)
	if ch, ok := lineup.index[key]; ok {
		return ch, true
	}
	if fetched || !l.claimForcedRefresh(key.InstallationID) {
		return liveChannel{}, false
	}
	lineup, _ = l.lineupFor(ctx, caller, key.InstallationID, true)
	ch, ok := lineup.index[key]
	return ch, ok
}

// LogoURL returns the remote logo for a channel when it is an absolute http(s)
// URL. Logo requests carry no identity and may be anonymous, so they never
// refresh a lineup that was loaded before, however stale: only an installation
// with no lineup yet is fetched.
func (l *LiveTV) LogoURL(ctx context.Context, rawID string) (string, bool) {
	key, ok := l.decodeProviderChannelID(ctx, rawID)
	if !ok {
		return "", false
	}
	l.mu.Lock()
	entry := l.installLocked(key.InstallationID).lineup
	l.mu.Unlock()
	lineup := entry.val
	if !entry.ok {
		lineup, _ = l.lineupFor(ctx, LiveTVCaller{}, key.InstallationID, false)
	}
	ch, ok := lineup.index[key]
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(ch.LogoURL, "http://") && !strings.HasPrefix(ch.LogoURL, "https://") {
		return "", false
	}
	return ch.LogoURL, true
}

// Info returns a provider's info, cached for liveTVCacheTTL. A failed refresh
// returns the stale copy; with no copy it returns the error.
func (l *LiveTV) Info(ctx context.Context, caller LiveTVCaller, installationID int) (liveTVInfo, error) {
	info, _, err := loadCached(ctx, l, installationID, "info",
		func(st *liveInstall) *liveCacheEntry[liveTVInfo] { return &st.info },
		false,
		func(ctx context.Context) (liveTVInfo, error) {
			var info liveTVInfo
			err := l.getJSON(ctx, caller, installationID, liveTVPathInfo, nil, &info)
			return info, err
		})
	// The cached copy is shared, so hand out its own slice.
	info.StreamHosts = slices.Clone(info.StreamHosts)
	return info, err
}

// ResolveStream asks the channel's provider for a stream URL in the requested
// format ("ts", "hls" or "any") and checks the URL's first-hop host against the
// provider's declared streamHosts. Errors never carry the URL, which may embed
// credentials.
func (l *LiveTV) ResolveStream(ctx context.Context, caller LiveTVCaller, ch liveChannel, format string) (liveTVStream, error) {
	status, body, err := l.routes.Call(ctx, ch.InstallationID, caller, http.MethodGet, liveTVPathStream,
		map[string]string{"channel_id": ch.ID, "format": format})
	if err != nil {
		return liveTVStream{}, fmt.Errorf("livetv: stream: %w", err)
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return liveTVStream{}, errLiveTVChannelNotFound
	case http.StatusUnprocessableEntity:
		return liveTVStream{}, errLiveTVFormatUnavailable
	default:
		return liveTVStream{}, fmt.Errorf("livetv: stream: status %d", status)
	}

	var stream liveTVStream
	if err := json.Unmarshal(body, &stream); err != nil {
		return liveTVStream{}, fmt.Errorf("livetv: stream: decode: %w", err)
	}
	if stream.Container != "ts" && stream.Container != "hls" {
		return liveTVStream{}, fmt.Errorf("livetv: stream: unknown container %q", stream.Container)
	}
	if stream.Mode != "redirect" && stream.Mode != "server" {
		return liveTVStream{}, fmt.Errorf("livetv: stream: unknown mode %q", stream.Mode)
	}

	info, err := l.Info(ctx, caller, ch.InstallationID)
	if err != nil {
		return liveTVStream{}, fmt.Errorf("livetv: stream: provider info: %w", err)
	}
	// url.Parse errors quote the URL, so a parse failure is reported as a
	// disallowed host rather than wrapped.
	u, err := url.Parse(stream.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return liveTVStream{}, errLiveTVHostNotAllowed
	}
	if !slices.ContainsFunc(info.StreamHosts, func(h string) bool { return strings.EqualFold(h, u.Host) }) {
		slog.WarnContext(ctx, "jellycompat: livetv stream host not allowed",
			"component", "jellycompat", "installation_id", ch.InstallationID, "host", u.Host)
		return liveTVStream{}, errLiveTVHostNotAllowed
	}
	return stream, nil
}
