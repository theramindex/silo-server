// theramindex fork: holds provider connection slots for Live TV plays.

package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	// liveTVSessionBeat is how often a play in use renews its provider slot.
	// Providers free a slot a session window (90 s) after its last renewal, so
	// a missed beat or two costs nothing.
	liveTVSessionBeat = 30 * time.Second
	// liveTVPlaybackErrorRateLimit is Jellyfin's PlaybackErrorCode for a stream
	// refused because every provider connection is in use.
	liveTVPlaybackErrorRateLimit = "RateLimitExceeded"
)

// stopSession frees a provider slot. A failure is logged: the provider frees
// the slot itself once the session window passes.
func (l *LiveTV) stopSession(ctx context.Context, caller LiveTVCaller, installationID int, sessionID string) {
	status, _, err := l.routes.Call(ctx, installationID, caller, http.MethodPost, liveTVPathSessionStop,
		map[string]string{liveTVSessionIDParam: sessionID})
	if err != nil || status/100 != 2 {
		slog.WarnContext(ctx, "jellycompat: livetv session stop failed",
			"component", "jellycompat", "installation_id", installationID, "status", status, "error", err)
	}
}

// beatPlay renews a leased play's provider slot when liveTVSessionBeat has
// passed since the last renewal. A provider that no longer knows the session,
// after a restart or a long pause, is asked for the stream again under the
// same id. If every connection is now taken, the play ends.
func (l *LiveTV) beatPlay(ctx context.Context, id string) {
	l.streamMu.Lock()
	p, ok := l.plays[id]
	now := l.now()
	if !ok || !p.Stream.Leased || !p.Expires.After(now) || now.Sub(p.beatAt) < liveTVSessionBeat {
		l.streamMu.Unlock()
		return
	}
	p.beatAt = now
	play := *p
	l.streamMu.Unlock()

	status, body, err := l.routes.Call(ctx, play.Channel.InstallationID, play.Caller, http.MethodPost, liveTVPathSessionHeartbeat,
		map[string]string{liveTVSessionIDParam: id})
	if err == nil && status/100 == 2 {
		return
	}
	if err != nil || status != http.StatusNotFound || liveErrorCode(body) != liveTVCodeSessionNotFound {
		slog.WarnContext(ctx, "jellycompat: livetv session heartbeat failed",
			"component", "jellycompat", "installation_id", play.Channel.InstallationID, "status", status, "error", err)
		return
	}

	stream, err := l.ResolveStream(ctx, play.Caller, play.Channel, play.Stream.Container, id)
	switch {
	case err == nil:
		l.streamMu.Lock()
		if p, ok := l.plays[id]; ok {
			p.Stream = stream
		}
		l.streamMu.Unlock()
	case errors.Is(err, errLiveTVConnectionLimit):
		slog.InfoContext(ctx, "jellycompat: livetv play ended: its connection slot was taken",
			"component", "jellycompat", "installation_id", play.Channel.InstallationID, "channel_id", play.Channel.EncodedID)
		l.dropPlay(id)
	default:
		slog.WarnContext(ctx, "jellycompat: livetv session renewal failed",
			"component", "jellycompat", "installation_id", play.Channel.InstallationID, "error", err)
	}
}

// dropPlay forgets a play and returns it.
func (l *LiveTV) dropPlay(id string) (livePlay, bool) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	p, ok := l.plays[id]
	if !ok {
		return livePlay{}, false
	}
	delete(l.plays, id)
	return *p, true
}

// endPlay forgets a play and frees its provider slot.
func (l *LiveTV) endPlay(ctx context.Context, id string) {
	if play, ok := l.dropPlay(id); ok && play.Stream.Leased {
		l.stopSession(ctx, play.Caller, play.Channel.InstallationID, id)
	}
}

// endDevicePlays ends the caller's plays on a device. A device plays one
// channel at a time, and clients that switch channels do not always report
// the old one stopped, which would hold its slot for a session window.
func (l *LiveTV) endDevicePlays(ctx context.Context, caller LiveTVCaller, deviceID string) {
	if deviceID == "" {
		return
	}
	var ids []string
	l.streamMu.Lock()
	for id, p := range l.plays {
		if p.Caller == caller && p.DeviceID == deviceID {
			ids = append(ids, id)
		}
	}
	l.streamMu.Unlock()
	for _, id := range ids {
		l.endPlay(ctx, id)
	}
}

// keepPlayAlive renews a play's slot every liveTVSessionBeat until the
// returned stop is called, for a relay that is one long request.
func (l *LiveTV) keepPlayAlive(ctx context.Context, id string) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(liveTVSessionBeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.beatPlay(ctx, id)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// liveReportKind is which Jellyfin playback report a route carries.
type liveReportKind int

const (
	liveReportStart liveReportKind = iota
	liveReportProgress
	liveReportStop
)

// wrapPlaybackReport answers Jellyfin's playback reports for live plays: a
// report renews the play's provider slot, a start report records the channel
// as watched, and a stop report ends the play and frees its slot. Upstream
// knows nothing of live plays, so they never reach next; every other report
// does, with its body intact.
func (h *liveTVHandlers) wrapPlaybackReport(next http.HandlerFunc, kind liveReportKind) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		s := SessionFromContext(r.Context())
		if err != nil || s == nil {
			next(w, r)
			return
		}
		var req sessionReportRequest
		_ = json.Unmarshal(body, &req)
		id := firstNonEmpty(req.PlaySessionID, newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId"))
		play, ok := h.bridge.play(id)
		if id == "" || !ok || play.Caller != liveCaller(s) {
			next(w, r)
			return
		}
		switch kind {
		case liveReportStop:
			h.bridge.endPlay(r.Context(), id)
		case liveReportStart:
			h.recordWatch(r.Context(), s, play)
			h.bridge.beatPlay(r.Context(), id)
		default:
			h.bridge.beatPlay(r.Context(), id)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// recordWatch records a play's channel as watched once per play, so a client
// that reports the start twice counts once. A failure is logged and does not
// fail the report.
func (h *liveTVHandlers) recordWatch(ctx context.Context, s *Session, play livePlay) {
	if !h.bridge.markWatched(play.PlaySessionID) {
		return
	}
	ch := play.Channel
	if err := h.bridge.RecordWatch(ctx, s.StreamAppUserID, ch.InstallationID, s.ProfileID, ch.ID, ch.Name); err != nil {
		slog.WarnContext(ctx, "jellycompat: recording a livetv watch failed",
			"component", "jellycompat", "installation_id", ch.InstallationID, "error", err)
	}
}

// markWatched marks a play as recorded and reports whether it was not yet.
func (l *LiveTV) markWatched(id string) bool {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	p, ok := l.plays[id]
	if !ok || p.watched {
		return false
	}
	p.watched = true
	return true
}
