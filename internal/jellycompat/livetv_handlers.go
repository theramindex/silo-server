// theramindex fork: Live TV listings, view, item and favorite wrappers, and channel logos.

package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// liveLogoTimeout bounds one channel logo fetch.
const liveLogoTimeout = 15 * time.Second

// liveTVHandlers serves Live TV to Jellyfin clients from a LiveTV bridge.
type liveTVHandlers struct {
	bridge   *LiveTV
	serverID string
	items    *ItemsHandler
	userData *UserDataHandler
	playback *PlaybackHandler

	// logoTransport fetches logos on a provider's declared streamHosts; nil is
	// http.DefaultTransport. publicLogoClient fetches every other logo and
	// refuses private addresses when it dials.
	logoTransport    http.RoundTripper
	publicLogoClient *http.Client
}

// newLiveTVHandlers returns nil when bridge is nil, which leaves every route
// and wrapper as upstream has it.
func newLiveTVHandlers(bridge *LiveTV, serverID string, items *ItemsHandler, userData *UserDataHandler, playback *PlaybackHandler) *liveTVHandlers {
	if bridge == nil {
		return nil
	}
	return &liveTVHandlers{
		bridge:           bridge,
		serverID:         serverID,
		items:            items,
		userData:         userData,
		playback:         playback,
		publicLogoClient: netguard.NewClient(liveLogoTimeout),
	}
}

func liveCaller(s *Session) LiveTVCaller {
	return LiveTVCaller{UserID: s.StreamAppUserID, ProfileID: s.ProfileID}
}

// profilePrefs reads the session profile's preferences for one installation.
// A read failure is logged and reads as empty preferences, so it never fails a
// listing.
func (h *liveTVHandlers) profilePrefs(ctx context.Context, s *Session, installationID int) liveProfilePrefs {
	prefs, err := h.bridge.ProfilePrefs(ctx, s.StreamAppUserID, installationID, s.ProfileID)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat: reading livetv preferences failed; using none",
			"component", "jellycompat", "installation_id", installationID, "error", err)
		return liveProfilePrefs{Favorites: map[string]bool{}, Hidden: map[string]bool{}}
	}
	return prefs
}

func liveChannelHidden(ch liveChannel, prefs liveProfilePrefs) bool {
	return ch.GroupID != "" && prefs.Hidden[ch.GroupID]
}

// visibleChannels returns the session's lineup without the groups its profile
// hides, plus the preferences it read, keyed by installation. Preferences are
// read once per installation.
func (h *liveTVHandlers) visibleChannels(ctx context.Context, s *Session) ([]liveChannel, map[int]liveProfilePrefs) {
	lineup := h.bridge.Lineup(ctx, liveCaller(s))
	prefs := map[int]liveProfilePrefs{}
	out := make([]liveChannel, 0, len(lineup))
	for _, ch := range lineup {
		p, ok := prefs[ch.InstallationID]
		if !ok {
			p = h.profilePrefs(ctx, s, ch.InstallationID)
			prefs[ch.InstallationID] = p
		}
		if !liveChannelHidden(ch, p) {
			out = append(out, ch)
		}
	}
	return out, prefs
}

// view is the Live TV UserView listed in /UserViews.
func (h *liveTVHandlers) view() baseItemDTO {
	return baseItemDTO{
		ID:                   liveTVViewID,
		ServerID:             h.serverID,
		Type:                 "UserView",
		CollectionType:       "livetv",
		Name:                 "Live TV",
		SortName:             "live tv",
		IsFolder:             true,
		DisplayPreferencesID: liveTVViewID,
		ImageTags:            map[string]string{},
		UserData:             &itemUserDataDTO{Key: liveTVViewID, ItemID: liveTVViewID},
	}
}

type liveTVServiceDTO struct {
	Name               string   `json:"Name"`
	HomePageURL        string   `json:"HomePageUrl"`
	Status             string   `json:"Status"`
	StatusMessage      string   `json:"StatusMessage"`
	Version            string   `json:"Version"`
	HasUpdateAvailable bool     `json:"HasUpdateAvailable"`
	IsVisible          bool     `json:"IsVisible"`
	Tuners             []string `json:"Tuners"`
}

type liveTVInfoDTO struct {
	Services     []liveTVServiceDTO `json:"Services"`
	IsEnabled    bool               `json:"IsEnabled"`
	EnabledUsers []string           `json:"EnabledUsers"`
}

// HandleInfo serves GET /LiveTv/Info with one service per provider.
func (h *liveTVHandlers) HandleInfo(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	out := liveTVInfoDTO{Services: []liveTVServiceDTO{}, EnabledUsers: []string{}}
	for _, p := range h.bridge.Providers(r.Context()) {
		svc := liveTVServiceDTO{Status: "Ok", IsVisible: true, Tuners: []string{}}
		info, err := h.bridge.Info(r.Context(), liveCaller(s), p.InstallationID)
		svc.Name = info.Name
		if err != nil {
			svc.Status = "Unavailable"
		}
		if svc.Name == "" {
			svc.Name = p.PluginID
		}
		out.Services = append(out.Services, svc)
	}
	if len(out.Services) > 0 {
		out.IsEnabled = true
		out.EnabledUsers = []string{s.PseudoUserID.String()}
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleChannels serves GET /LiveTv/Channels.
func (h *liveTVHandlers) HandleChannels(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	h.writeChannelList(w, r, s)
}

func (h *liveTVHandlers) writeChannelList(w http.ResponseWriter, r *http.Request, s *Session) {
	channels, prefs := h.visibleChannels(r.Context(), s)
	writeJSON(w, http.StatusOK, queryLiveChannels(channels, prefs, newCaseInsensitiveQuery(r.URL.Query()), h.serverID))
}

// HandleChannel serves GET /LiveTv/Channels/{id}.
func (h *liveTVHandlers) HandleChannel(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	h.writeChannel(w, r, s, chi.URLParam(r, "id"))
}

// writeChannel answers one channel, or 404 when it is unknown or its group is
// hidden for the session's profile.
func (h *liveTVHandlers) writeChannel(w http.ResponseWriter, r *http.Request, s *Session, rawID string) {
	ch, ok := h.bridge.ChannelByID(r.Context(), liveCaller(s), rawID)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	prefs := h.profilePrefs(r.Context(), s, ch.InstallationID)
	if liveChannelHidden(ch, prefs) {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	writeJSON(w, http.StatusOK, mapLiveChannel(ch, h.serverID, prefs))
}

// wrapItem serves the Live TV view and channels on GET /Items/{id} and
// /Users/{userId}/Items/{id}, and passes every other id to next.
func (h *liveTVHandlers) wrapItem(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "id")
		isView := isLiveTVViewID(rawID)
		if !isView && !isLiveChannelID(rawID) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		if !validatePseudoUser(w, chi.URLParam(r, "userId"), s) {
			return
		}
		if isView {
			writeJSON(w, http.StatusOK, h.view())
			return
		}
		h.writeChannel(w, r, s, rawID)
	}
}

// wrapItems lists channels for GET /Items?ParentId=<Live TV view> and passes
// every other listing to next.
func (h *liveTVHandlers) wrapItems(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLiveTVViewID(newCaseInsensitiveQuery(r.URL.Query()).Get("ParentId")) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		// /Users/{id}/Items names the user "id".
		if !validatePseudoUser(w, firstNonEmpty(chi.URLParam(r, "userId"), chi.URLParam(r, "id")), s) {
			return
		}
		h.writeChannelList(w, r, s)
	}
}

// wrapFavorite stores a channel favorite in the profile's Live TV preferences
// on the favorite routes and passes every other id to next.
func (h *liveTVHandlers) wrapFavorite(next http.HandlerFunc, favorite bool) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "itemId")
		if !isLiveChannelID(rawID) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		// The legacy /Users/{userId}/FavoriteItems routes check the user first,
		// as upstream does.
		if !validatePseudoUser(w, chi.URLParam(r, "userId"), s) {
			return
		}
		ch, ok := h.bridge.ChannelByID(r.Context(), liveCaller(s), rawID)
		if !ok {
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		}
		if err := h.bridge.SetFavorite(r.Context(), s.StreamAppUserID, ch.InstallationID, s.ProfileID, ch.ID, favorite); err != nil {
			slog.ErrorContext(r.Context(), "jellycompat: writing livetv favorite failed",
				"component", "jellycompat", "installation_id", ch.InstallationID, "error", err)
			writeError(w, http.StatusInternalServerError, "ServerError", "Failed to update favorite")
			return
		}
		writeJSON(w, http.StatusOK, itemUserDataDTO{Key: ch.EncodedID, ItemID: ch.EncodedID, IsFavorite: favorite})
	}
}

// liveLogoServer is what ImagesHandler needs to answer a channel logo.
type liveLogoServer interface {
	serveLiveLogo(w http.ResponseWriter, r *http.Request, rawID, imageType string)
}

// serveLiveLogo proxies a channel's Primary image. Logos are always proxied,
// since they are often plain http and a browser would block mixed content. The
// route is anonymous, so a logo is fetched only from one of the provider's
// streamHosts or from a host whose every address is public; anything else is
// a 404.
func (h *liveTVHandlers) serveLiveLogo(w http.ResponseWriter, r *http.Request, rawID, imageType string) {
	if !strings.EqualFold(imageType, compatImagePrimary) {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	logo, ok := h.bridge.LogoURL(r.Context(), rawID)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	client, ok := h.logoClient(r.Context(), rawID, logo)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, logo, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	copyConditionalImageRequestHeaders(req.Header, r.Header)
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	proxyImage(w, resp)
}

// logoClient picks the client allowed to fetch logo, or reports that it may
// not be fetched. A logo on one of the provider's streamHosts may follow
// redirects only to those hosts.
func (h *liveTVHandlers) logoClient(ctx context.Context, rawID, logo string) (*http.Client, bool) {
	target, err := parseRemoteImageURL(logo)
	if err != nil {
		return nil, false
	}
	key, ok := decodeLiveChannelID(rawID)
	if !ok {
		return nil, false
	}
	if info, err := h.bridge.Info(ctx, LiveTVCaller{}, key.InstallationID); err == nil && liveHostAllowed(info.StreamHosts, target.Host) {
		hosts := info.StreamHosts
		return &http.Client{
			Timeout:   liveLogoTimeout,
			Transport: h.logoTransport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 || !liveHostAllowed(hosts, req.URL.Host) {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}, true
	}
	if netguard.CheckURL(ctx, logo, false) == nil {
		return h.publicLogoClient, true
	}
	return nil, false
}

func liveHostAllowed(hosts []string, host string) bool {
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, host) })
}
