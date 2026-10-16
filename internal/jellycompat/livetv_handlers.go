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

	// logoTransport fetches logos on a provider's declared streamHosts. Like the
	// stream transport, it dials private addresses only for the hosts the
	// request carries (withLiveHosts) and refuses blocked addresses always.
	// publicLogoClient fetches every other logo and refuses private addresses
	// when it dials.
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
		logoTransport:    &liveStreamTransport{private: newLiveGuardedTransport(true), public: newLiveGuardedTransport(false)},
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
	prefs.Profiles = h.bridge.knownProfiles(ctx, liveCaller(s), installationID, prefs.Profiles)
	return prefs
}

// liveChannelHidden reports whether the profile hides a channel: its group is
// hidden, or a profile selection leaves out every channel profile it is in.
func liveChannelHidden(ch liveChannel, prefs liveProfilePrefs) bool {
	if ch.GroupID != "" && prefs.Hidden[ch.GroupID] {
		return true
	}
	return prefs.Profiles != nil && !slices.ContainsFunc(ch.ProfileIDs, func(id string) bool { return prefs.Profiles[id] })
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
		CollectionType:       liveTVCollectionType,
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

// HandleEmptyList answers the recording and timer listings, which the bridge
// does not serve, with an empty result. Jellyfin web loads its home sections
// with one Promise.all, so a 404 from any Live TV listing stops every library
// row on the home page from loading.
func (h *liveTVHandlers) HandleEmptyList(w http.ResponseWriter, r *http.Request) {
	if SessionFromContext(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}})
}

type liveTVGuideInfoDTO struct {
	StartDate string `json:"StartDate"`
	EndDate   string `json:"EndDate"`
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

// writeChannelList answers a channel listing. The guide's category filter
// keeps the channels airing that kind of program in the next day, and unless
// the client opts out each channel on the page carries its current program.
func (h *liveTVHandlers) writeChannelList(w http.ResponseWriter, r *http.Request, s *Session) {
	channels, prefs := h.visibleChannels(r.Context(), s)
	q := newCaseInsensitiveQuery(r.URL.Query())
	now := h.bridge.now()
	if classes := liveChannelClasses(q); len(classes) > 0 {
		keep := h.channelsWithClasses(r.Context(), s, classes, now)
		channels = slices.DeleteFunc(channels, func(ch liveChannel) bool { return !keep[ch.EncodedID] })
	}
	result := queryLiveChannels(channels, prefs, q, h.serverID)
	if add := parseLiveBool(q.Get("AddCurrentProgram")); add == nil || *add {
		byID := make(map[string]liveChannel, len(result.Items))
		for _, ch := range channels {
			byID[ch.EncodedID] = ch
		}
		h.addCurrentPrograms(r.Context(), s, result.Items, byID, now)
	}
	writeJSON(w, http.StatusOK, result)
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

// wrapItem serves the Live TV view, channels, programs and channel folders on
// GET /Items/{id} and /Users/{userId}/Items/{id}, and passes every other id to
// next.
func (h *liveTVHandlers) wrapItem(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "id")
		isView, isProgram, isFolder := isLiveTVViewID(rawID), isLiveProgramID(rawID), isLiveFolderID(rawID)
		if !isView && !isProgram && !isFolder && !isLiveChannelID(rawID) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		if !validatePseudoUser(w, chi.URLParam(r, "userId"), s) {
			return
		}
		switch {
		case isView:
			writeJSON(w, http.StatusOK, h.view())
		case isProgram:
			h.writeProgram(w, r, s, rawID)
		case isFolder:
			h.writeFolder(w, r, s, rawID)
		default:
			h.writeChannel(w, r, s, rawID)
		}
	}
}

// wrapItems lists channels for GET /Items?ParentId=<Live TV view>, a channel
// folder's contents for ParentId=<folder>, and passes every other listing to
// next.
func (h *liveTVHandlers) wrapItems(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		parentID := newCaseInsensitiveQuery(r.URL.Query()).Get("ParentId")
		isFolder := isLiveFolderID(parentID)
		if !isFolder && !isLiveTVViewID(parentID) {
			next(w, r)
			return
		}
		s := SessionFromContext(r.Context())
		// /Users/{id}/Items names the user "id".
		if !validatePseudoUser(w, firstNonEmpty(chi.URLParam(r, "userId"), chi.URLParam(r, "id")), s) {
			return
		}
		if isFolder {
			h.writeFolderItems(w, r, s, parentID)
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

// liveLogoServer is what ImagesHandler needs to answer a channel logo or a
// program image. caller is the request's session, or the zero value when the
// request has none.
type liveLogoServer interface {
	serveLiveLogo(w http.ResponseWriter, r *http.Request, caller LiveTVCaller, rawID, imageType string)
	serveLiveProgramImage(w http.ResponseWriter, r *http.Request, caller LiveTVCaller, rawID, imageType string)
}

// serveLiveLogo proxies a channel's Primary image. Logos are always proxied,
// since they are often plain http and a browser would block mixed content. The
// route is anonymous, so a logo is fetched only from one of the provider's
// streamHosts or from a host whose every address is public; anything else is
// a 404. A successful answer that is not an image is a 502, so a login page or
// error document is never served as a logo.
func (h *liveTVHandlers) serveLiveLogo(w http.ResponseWriter, r *http.Request, caller LiveTVCaller, rawID, imageType string) {
	if !strings.EqualFold(imageType, compatImagePrimary) {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	logo, ok := h.bridge.LogoURL(r.Context(), caller, rawID)
	key, keyOK := decodeLiveChannelID(rawID)
	if !ok || !keyOK {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.proxyLiveImage(w, r, caller, key.InstallationID, logo)
}

// proxyLiveImage fetches a channel logo or program image from one
// installation's provider and relays it.
func (h *liveTVHandlers) proxyLiveImage(w http.ResponseWriter, r *http.Request, caller LiveTVCaller, installationID int, imageURL string) {
	ctx, client, ok := h.logoClient(r.Context(), caller, installationID, imageURL)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
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
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 && !liveImageContentType(resp.Header.Get("Content-Type")) {
		_ = resp.Body.Close()
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	proxyImage(w, resp)
}

func liveImageContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/")
}

// logoClient picks the client allowed to fetch logo, and the context to fetch
// it with, or reports that it may not be fetched. A logo on one of caller's
// streamHosts for the provider may follow redirects only to those hosts.
func (h *liveTVHandlers) logoClient(ctx context.Context, caller LiveTVCaller, installationID int, logo string) (context.Context, *http.Client, bool) {
	target, err := parseRemoteImageURL(logo)
	if err != nil {
		return ctx, nil, false
	}
	if info, err := h.bridge.Info(ctx, caller, installationID); err == nil && liveHostAllowed(info.StreamHosts, target.Host) {
		hosts := info.StreamHosts
		return withLiveHosts(ctx, hosts), &http.Client{
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
		return ctx, h.publicLogoClient, true
	}
	return ctx, nil, false
}

func liveHostAllowed(hosts []string, host string) bool {
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, host) })
}
