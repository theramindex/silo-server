// theramindex fork: the Live TV guide for Jellyfin clients: programs, the
// Programs tab rows, program items and images, and channels' current programs.

package jellycompat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	liveProgramItemType = "Program"
	// liveGuideDefaultSpan is how far ahead a programs request with no upper
	// bound looks, and liveGuideMaxSpan the widest window ever asked for.
	liveGuideDefaultSpan = 24 * time.Hour
	liveGuideMaxSpan     = 8 * 24 * time.Hour
	// liveGuideInfoSpan is how far ahead GuideInfo offers days.
	liveGuideInfoSpan = 7 * 24 * time.Hour
	// liveCurrentProgramMaxChannels caps the channels a /LiveTv/Channels page
	// looks up current programs for.
	liveCurrentProgramMaxChannels = 500
	// liveClassChannelSpan is the window a guide category filter looks at to
	// decide which channels carry that kind of program.
	liveClassChannelSpan = 24 * time.Hour
)

// liveProgramDTO is a Jellyfin BaseItemDto for a guide Program.
type liveProgramDTO struct {
	Name                    string            `json:"Name"`
	ServerID                string            `json:"ServerId,omitempty"`
	ID                      string            `json:"Id"`
	Type                    string            `json:"Type"`
	MediaType               string            `json:"MediaType"`
	IsFolder                bool              `json:"IsFolder"`
	LocationType            string            `json:"LocationType"`
	ChannelID               string            `json:"ChannelId"`
	ChannelName             string            `json:"ChannelName,omitempty"`
	ChannelNumber           string            `json:"ChannelNumber,omitempty"`
	ChannelPrimaryImageTag  string            `json:"ChannelPrimaryImageTag,omitempty"`
	StartDate               string            `json:"StartDate"`
	EndDate                 string            `json:"EndDate"`
	RunTimeTicks            int64             `json:"RunTimeTicks"`
	Overview                string            `json:"Overview,omitempty"`
	Genres                  []string          `json:"Genres"`
	ImageTags               map[string]string `json:"ImageTags"`
	BackdropImageTags       []string          `json:"BackdropImageTags"`
	PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio,omitempty"`
	IsMovie                 bool              `json:"IsMovie"`
	IsSeries                bool              `json:"IsSeries"`
	IsSports                bool              `json:"IsSports"`
	IsNews                  bool              `json:"IsNews"`
	IsKids                  bool              `json:"IsKids"`
	IsLive                  bool              `json:"IsLive"`
	IsPremiere              bool              `json:"IsPremiere"`
	IsRepeat                bool              `json:"IsRepeat"`
	PlayAccess              string            `json:"PlayAccess"`
	UserData                *itemUserDataDTO  `json:"UserData,omitempty"`
}

type liveProgramQueryResult struct {
	Items            []liveProgramDTO `json:"Items"`
	TotalRecordCount int              `json:"TotalRecordCount"`
	StartIndex       int              `json:"StartIndex"`
}

func formatLiveTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// liveProgramImageAspect is the aspect Jellyfin assumes for guide artwork.
var liveProgramImageAspect = 16.0 / 9.0

func mapLiveProgram(p liveGuideProgram, serverID string) liveProgramDTO {
	dto := liveProgramDTO{
		Name:              p.Title,
		ServerID:          serverID,
		ID:                p.EncodedID,
		Type:              liveProgramItemType,
		MediaType:         liveTVMediaType,
		LocationType:      liveTVLocationRemote,
		ChannelID:         p.Channel.EncodedID,
		ChannelName:       p.Channel.Name,
		ChannelNumber:     p.Channel.Number,
		StartDate:         formatLiveTime(p.StartUnix),
		EndDate:           formatLiveTime(p.EndUnix),
		RunTimeTicks:      (p.EndUnix - p.StartUnix) * 10_000_000,
		Overview:          p.Summary,
		Genres:            slices.Clone(p.Categories),
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		IsMovie:           p.IsMovie,
		IsSeries:          p.IsSeries,
		IsSports:          p.IsSports,
		IsNews:            p.IsNews,
		IsKids:            p.IsKids,
		IsLive:            p.IsLive,
		PlayAccess:        liveTVPlayAccess,
		UserData:          &itemUserDataDTO{Key: p.EncodedID, ItemID: p.EncodedID},
	}
	if dto.Genres == nil {
		dto.Genres = []string{}
	}
	if liveLogoUsable(p.Channel.LogoURL) {
		dto.ChannelPrimaryImageTag = liveLogoTag(p.Channel.LogoURL)
	}
	if liveLogoUsable(p.ImageURL) {
		dto.ImageTags[compatImagePrimary] = liveLogoTag(p.ImageURL)
		dto.PrimaryImageAspectRatio = &liveProgramImageAspect
	}
	return dto
}

// liveProgramsQuery is a /LiveTv/Programs or /LiveTv/Programs/Recommended
// request, from the query string or a GetProgramsDto body. A nil pointer is a
// filter the client did not set.
type liveProgramsQuery struct {
	ChannelIDs                 []string
	MinStartDate, MaxStartDate *time.Time
	MinEndDate, MaxEndDate     *time.Time
	HasAired, IsAiring         *bool
	IsMovie, IsSeries          *bool
	IsNews, IsKids, IsSports   *bool
	StartIndex, Limit          int
	SortBy                     []string
	SortOrder                  []string
}

// liveProgramsBody is Jellyfin's GetProgramsDto. encoding/json matches its
// field names case-insensitively.
type liveProgramsBody struct {
	ChannelIDs   []string `json:"ChannelIds"`
	MinStartDate *time.Time
	MaxStartDate *time.Time
	MinEndDate   *time.Time
	MaxEndDate   *time.Time
	HasAired     *bool
	IsAiring     *bool
	IsMovie      *bool
	IsSeries     *bool
	IsNews       *bool
	IsKids       *bool
	IsSports     *bool
	StartIndex   *int
	Limit        *int
	SortBy       []string
	SortOrder    []string
}

func parseLiveProgramsQuery(r *http.Request) (liveProgramsQuery, error) {
	q := newCaseInsensitiveQuery(r.URL.Query())
	out := liveProgramsQuery{
		ChannelIDs:   splitLiveList(q.Get("ChannelIds")),
		MinStartDate: parseLiveDate(q.Get("MinStartDate")),
		MaxStartDate: parseLiveDate(q.Get("MaxStartDate")),
		MinEndDate:   parseLiveDate(q.Get("MinEndDate")),
		MaxEndDate:   parseLiveDate(q.Get("MaxEndDate")),
		HasAired:     parseLiveBool(q.Get("HasAired")),
		IsAiring:     parseLiveBool(q.Get("IsAiring")),
		IsMovie:      parseLiveBool(q.Get("IsMovie")),
		IsSeries:     parseLiveBool(q.Get("IsSeries")),
		IsNews:       parseLiveBool(q.Get("IsNews")),
		IsKids:       parseLiveBool(q.Get("IsKids")),
		IsSports:     parseLiveBool(q.Get("IsSports")),
		SortBy:       splitLiveList(q.Get("SortBy")),
		SortOrder:    splitLiveList(q.Get("SortOrder")),
	}
	out.StartIndex, _ = strconv.Atoi(q.Get("StartIndex"))
	out.Limit, _ = strconv.Atoi(q.Get("Limit"))
	if r.Method != http.MethodPost || r.Body == nil {
		return out, nil
	}
	var body liveProgramsBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		return out, err
	}
	if len(body.ChannelIDs) > 0 {
		out.ChannelIDs = body.ChannelIDs
	}
	for _, f := range []struct {
		dst **time.Time
		src *time.Time
	}{{&out.MinStartDate, body.MinStartDate}, {&out.MaxStartDate, body.MaxStartDate}, {&out.MinEndDate, body.MinEndDate}, {&out.MaxEndDate, body.MaxEndDate}} {
		if f.src != nil {
			*f.dst = f.src
		}
	}
	for _, f := range []struct{ dst, src **bool }{
		{&out.HasAired, &body.HasAired}, {&out.IsAiring, &body.IsAiring}, {&out.IsMovie, &body.IsMovie}, {&out.IsSeries, &body.IsSeries},
		{&out.IsNews, &body.IsNews}, {&out.IsKids, &body.IsKids}, {&out.IsSports, &body.IsSports},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if body.StartIndex != nil {
		out.StartIndex = *body.StartIndex
	}
	if body.Limit != nil {
		out.Limit = *body.Limit
	}
	if len(body.SortBy) > 0 {
		out.SortBy = body.SortBy
	}
	if len(body.SortOrder) > 0 {
		out.SortOrder = body.SortOrder
	}
	return out, nil
}

func splitLiveList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseLiveBool(raw string) *bool {
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &v
}

func parseLiveDate(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.0000000", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

// window is the provider window a query needs, in unix seconds: programs that
// end after start and begin before end.
func (q liveProgramsQuery) window(now time.Time) (start, end int64) {
	startT := now
	switch {
	case q.MinEndDate != nil:
		startT = *q.MinEndDate
	case q.MinStartDate != nil:
		startT = *q.MinStartDate
	case q.HasAired != nil && *q.HasAired:
		startT = now.Add(-liveGuideDefaultSpan)
	}
	if (q.IsAiring != nil && *q.IsAiring) || (q.HasAired != nil && !*q.HasAired) {
		if startT.Before(now) {
			startT = now
		}
	}
	endT := startT.Add(liveGuideDefaultSpan)
	if q.MaxStartDate != nil {
		endT = *q.MaxStartDate
	}
	if q.IsAiring != nil && *q.IsAiring && endT.After(now) {
		endT = now.Add(time.Second)
	}
	if endT.Sub(startT) > liveGuideMaxSpan {
		endT = startT.Add(liveGuideMaxSpan)
	}
	if !endT.After(startT) {
		endT = startT.Add(time.Second)
	}
	return startT.Unix(), endT.Unix()
}

// class is the one program class the provider can filter by, when exactly one
// flag is required.
func (q liveProgramsQuery) class() string {
	var classes []string
	for _, f := range []struct {
		want  *bool
		class string
	}{{q.IsMovie, liveTVClassMovie}, {q.IsSports, liveTVClassSports}, {q.IsNews, liveTVClassNews}, {q.IsKids, liveTVClassKids}, {q.IsSeries, liveTVClassSeries}} {
		if f.want != nil && *f.want {
			classes = append(classes, f.class)
		}
	}
	if len(classes) == 1 {
		return classes[0]
	}
	return ""
}

// matches applies the filters exactly, after the provider's coarser window.
func (q liveProgramsQuery) matches(p liveGuideProgram, now time.Time) bool {
	start, end := time.Unix(p.StartUnix, 0), time.Unix(p.EndUnix, 0)
	if q.MinStartDate != nil && start.Before(*q.MinStartDate) {
		return false
	}
	if q.MaxStartDate != nil && start.After(*q.MaxStartDate) {
		return false
	}
	if q.MinEndDate != nil && !end.After(*q.MinEndDate) {
		return false
	}
	if q.MaxEndDate != nil && end.After(*q.MaxEndDate) {
		return false
	}
	airing := !start.After(now) && end.After(now)
	if q.IsAiring != nil && airing != *q.IsAiring {
		return false
	}
	if q.HasAired != nil && !end.After(now) != *q.HasAired {
		return false
	}
	for _, f := range []struct {
		want *bool
		have bool
	}{{q.IsMovie, p.IsMovie}, {q.IsSeries, p.IsSeries}, {q.IsNews, p.IsNews}, {q.IsKids, p.IsKids}, {q.IsSports, p.IsSports}} {
		if f.want != nil && *f.want != f.have {
			return false
		}
	}
	return true
}

// guideScope is the session's visible channels, by compat id and by provider
// id per installation, with its preferences.
type guideScope struct {
	channels []liveChannel
	byID     map[string]liveChannel // EncodedID
	position map[string]int         // EncodedID -> lineup position
	prefs    map[int]liveProfilePrefs
}

func (h *liveTVHandlers) guideScope(ctx context.Context, s *Session) guideScope {
	channels, prefs := h.visibleChannels(ctx, s)
	scope := guideScope{channels: channels, byID: make(map[string]liveChannel, len(channels)), position: make(map[string]int, len(channels)), prefs: prefs}
	for i, ch := range channels {
		scope.byID[ch.EncodedID] = ch
		scope.position[ch.EncodedID] = i
	}
	return scope
}

// resolve maps requested compat channel ids to visible channels, grouped by
// installation in request order. Unknown and hidden ids are dropped.
func (scope guideScope) resolve(ids []string) map[int][]liveChannel {
	out := map[int][]liveChannel{}
	seen := map[string]bool{}
	for _, raw := range ids {
		parsed, err := uuid.Parse(raw)
		if err != nil || !isLiveChannelID(raw) {
			continue
		}
		id := parsed.String()
		ch, ok := scope.byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out[ch.InstallationID] = append(out[ch.InstallationID], ch)
	}
	return out
}

// fetch asks every installation in scope for programs and keeps those on
// visible channels that match q exactly. A failing provider is logged and
// skipped.
func (h *liveTVHandlers) fetchGuide(ctx context.Context, s *Session, scope guideScope, q liveProgramsQuery, now time.Time) []liveGuideProgram {
	start, end := q.window(now)
	req := liveProgramRequest{Start: start, End: end, Class: q.class()}
	var targets []struct {
		installationID int
		channelIDs     []string
	}
	if len(q.ChannelIDs) > 0 {
		for installationID, chans := range scope.resolve(q.ChannelIDs) {
			ids := make([]string, len(chans))
			for i, ch := range chans {
				ids[i] = ch.ID
			}
			targets = append(targets, struct {
				installationID int
				channelIDs     []string
			}{installationID, ids})
		}
	} else {
		req.Limit = liveProgramsMaxLimit
		for _, p := range h.bridge.Providers(ctx) {
			targets = append(targets, struct {
				installationID int
				channelIDs     []string
			}{p.InstallationID, nil})
		}
	}
	var requested map[string]bool
	if len(q.ChannelIDs) > 0 {
		requested = map[string]bool{}
		for _, t := range targets {
			for _, id := range t.channelIDs {
				requested[encodeLiveChannelID(t.installationID, id)] = true
			}
		}
	}
	var out []liveGuideProgram
	for _, t := range targets {
		r := req
		r.ChannelIDs = t.channelIDs
		programs, err := h.bridge.Programs(ctx, liveCaller(s), t.installationID, r)
		if err != nil {
			slog.WarnContext(ctx, "jellycompat: livetv programs failed; skipping provider",
				"component", "jellycompat", "installation_id", t.installationID, "error", err)
			continue
		}
		for _, p := range programs {
			if requested != nil && !requested[p.Channel.EncodedID] {
				continue
			}
			if _, visible := scope.byID[p.Channel.EncodedID]; visible && p.EncodedID != "" && q.matches(p, now) {
				out = append(out, p)
			}
		}
	}
	return out
}

// dedupeSimulcasts drops a program airing at the same time under the same
// title on another channel, which SD/HD and regional feeds produce.
func dedupeSimulcasts(programs []liveGuideProgram) []liveGuideProgram {
	type key struct {
		title string
		start int64
	}
	seen := map[key]bool{}
	return slices.DeleteFunc(programs, func(p liveGuideProgram) bool {
		k := key{strings.ToLower(strings.TrimSpace(p.Title)), p.StartUnix}
		if seen[k] {
			return true
		}
		seen[k] = true
		return false
	})
}

func pageLivePrograms(w http.ResponseWriter, programs []liveGuideProgram, q liveProgramsQuery, serverID string) {
	total := len(programs)
	start := min(max(q.StartIndex, 0), total)
	end := total
	if q.Limit > 0 {
		end = min(start+q.Limit, total)
	}
	items := make([]liveProgramDTO, 0, end-start)
	for _, p := range programs[start:end] {
		items = append(items, mapLiveProgram(p, serverID))
	}
	writeJSON(w, http.StatusOK, liveProgramQueryResult{Items: items, TotalRecordCount: total, StartIndex: start})
}

// HandlePrograms serves GET and POST /LiveTv/Programs: the guide grid asks for
// a page of channels over a day, and the Programs tab for upcoming programs of
// one kind across every channel.
func (h *liveTVHandlers) HandlePrograms(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	q, err := parseLiveProgramsQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid programs query")
		return
	}
	now := h.bridge.now()
	scope := h.guideScope(r.Context(), s)
	programs := h.fetchGuide(r.Context(), s, scope, q, now)
	if len(q.ChannelIDs) == 0 {
		programs = dedupeSimulcasts(programs)
	}
	descending := len(q.SortOrder) > 0 && strings.EqualFold(q.SortOrder[0], "Descending")
	// Rows across every channel lead with what the provider matched to a
	// real event, such as games from its sports guide; the guide grid keeps
	// plain time order.
	featuredFirst := len(q.ChannelIDs) == 0
	slices.SortStableFunc(programs, func(a, b liveGuideProgram) int {
		if featuredFirst && a.Featured != b.Featured {
			if a.Featured {
				return -1
			}
			return 1
		}
		c := cmp.Compare(a.StartUnix, b.StartUnix)
		if c == 0 {
			c = cmp.Compare(scope.position[a.Channel.EncodedID], scope.position[b.Channel.EncodedID])
		}
		if descending {
			return -c
		}
		return c
	})
	pageLivePrograms(w, programs, q, h.serverID)
}

// HandleRecommendedPrograms serves GET /LiveTv/Programs/Recommended, the "On
// Now" row: the profile's favorite channels first in their order, then
// featured programs, then programs with artwork, then the rest in lineup order.
func (h *liveTVHandlers) HandleRecommendedPrograms(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	q, err := parseLiveProgramsQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid programs query")
		return
	}
	now := h.bridge.now()
	scope := h.guideScope(r.Context(), s)

	favoriteRank := map[string]int{}
	var favoriteIDs []string
	for _, ch := range scope.channels {
		prefs := scope.prefs[ch.InstallationID]
		if !prefs.Favorites[ch.ID] {
			continue
		}
		rank := slices.Index(prefs.FavoriteOrder, ch.ID)
		if rank < 0 {
			rank = len(prefs.FavoriteOrder) + len(favoriteIDs)
		}
		favoriteRank[ch.EncodedID] = rank
		favoriteIDs = append(favoriteIDs, ch.EncodedID)
	}
	var programs []liveGuideProgram
	if len(favoriteIDs) > 0 {
		fq := q
		fq.ChannelIDs = favoriteIDs
		programs = h.fetchGuide(r.Context(), s, scope, fq, now)
	}
	if len(q.ChannelIDs) > 0 || q.Limit <= 0 || len(programs) < q.StartIndex+q.Limit {
		programs = append(programs, h.fetchGuide(r.Context(), s, scope, q, now)...)
	}
	seen := map[string]bool{}
	programs = slices.DeleteFunc(programs, func(p liveGuideProgram) bool {
		if seen[p.EncodedID] {
			return true
		}
		seen[p.EncodedID] = true
		return false
	})
	programs = dedupeSimulcasts(programs)
	slices.SortStableFunc(programs, func(a, b liveGuideProgram) int {
		ra, aFav := favoriteRank[a.Channel.EncodedID]
		rb, bFav := favoriteRank[b.Channel.EncodedID]
		switch {
		case aFav && bFav:
			if c := cmp.Compare(ra, rb); c != 0 {
				return c
			}
		case aFav:
			return -1
		case bFav:
			return 1
		}
		if a.Featured != b.Featured {
			if a.Featured {
				return -1
			}
			return 1
		}
		if ai, bi := liveLogoUsable(a.ImageURL), liveLogoUsable(b.ImageURL); ai != bi {
			if ai {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(scope.position[a.Channel.EncodedID], scope.position[b.Channel.EncodedID]); c != 0 {
			return c
		}
		return cmp.Compare(a.StartUnix, b.StartUnix)
	})
	pageLivePrograms(w, programs, q, h.serverID)
}

// HandleProgram serves GET /LiveTv/Programs/{id}.
func (h *liveTVHandlers) HandleProgram(w http.ResponseWriter, r *http.Request) {
	s := SessionFromContext(r.Context())
	if s == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	h.writeProgram(w, r, s, chi.URLParam(r, "id"))
}

// writeProgram answers one program, or 404 when it is unknown or its channel
// is hidden for the session's profile.
func (h *liveTVHandlers) writeProgram(w http.ResponseWriter, r *http.Request, s *Session, rawID string) {
	p, ok := h.bridge.ProgramByID(r.Context(), liveCaller(s), rawID)
	if !ok || liveChannelHidden(p.Channel, h.profilePrefs(r.Context(), s, p.InstallationID)) {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	writeJSON(w, http.StatusOK, mapLiveProgram(p, h.serverID))
}

// HandleGuideInfo serves GET /LiveTv/GuideInfo: the days the guide offers,
// from the current hour.
func (h *liveTVHandlers) HandleGuideInfo(w http.ResponseWriter, r *http.Request) {
	if SessionFromContext(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	start := h.bridge.now().UTC().Truncate(time.Hour)
	writeJSON(w, http.StatusOK, liveTVGuideInfoDTO{
		StartDate: formatLiveTime(start.Unix()),
		EndDate:   formatLiveTime(start.Add(liveGuideInfoSpan).Unix()),
	})
}

// channelsWithClasses returns the channels airing any of classes within
// liveClassChannelSpan, for the guide's category filter.
func (h *liveTVHandlers) channelsWithClasses(ctx context.Context, s *Session, classes []string, now time.Time) map[string]bool {
	out := map[string]bool{}
	for _, class := range classes {
		for _, p := range h.bridge.Providers(ctx) {
			programs, err := h.bridge.Programs(ctx, liveCaller(s), p.InstallationID, liveProgramRequest{
				Start: now.Unix(), End: now.Add(liveClassChannelSpan).Unix(), Class: class, Limit: liveProgramsMaxLimit,
			})
			if err != nil {
				slog.WarnContext(ctx, "jellycompat: livetv class programs failed; skipping provider",
					"component", "jellycompat", "installation_id", p.InstallationID, "error", err)
				continue
			}
			for _, prog := range programs {
				if prog.hasClass(class) {
					out[prog.Channel.EncodedID] = true
				}
			}
		}
	}
	return out
}

func (p liveTVProgram) hasClass(class string) bool {
	switch class {
	case liveTVClassMovie:
		return p.IsMovie
	case liveTVClassSports:
		return p.IsSports
	case liveTVClassNews:
		return p.IsNews
	case liveTVClassKids:
		return p.IsKids
	case liveTVClassSeries:
		return p.IsSeries
	}
	return false
}

// liveChannelClasses reads the guide category filter from a channels query:
// the classes set to true. All four content filters set means no filter.
func liveChannelClasses(q caseInsensitiveQuery) []string {
	var classes []string
	for _, f := range []struct{ param, class string }{
		{"IsMovie", liveTVClassMovie}, {"IsSports", liveTVClassSports}, {"IsNews", liveTVClassNews}, {"IsKids", liveTVClassKids}, {"IsSeries", liveTVClassSeries},
	} {
		if v := parseLiveBool(q.Get(f.param)); v != nil && *v {
			classes = append(classes, f.class)
		}
	}
	return classes
}

// addCurrentPrograms fills CurrentProgram on a channels page.
func (h *liveTVHandlers) addCurrentPrograms(ctx context.Context, s *Session, items []liveChannelDTO, channels map[string]liveChannel, now time.Time) {
	if len(items) == 0 || len(items) > liveCurrentProgramMaxChannels {
		return
	}
	byInstall := map[int][]string{}
	for _, item := range items {
		if ch, ok := channels[item.ID]; ok {
			byInstall[ch.InstallationID] = append(byInstall[ch.InstallationID], ch.ID)
		}
	}
	current := map[string]liveGuideProgram{}
	for installationID, ids := range byInstall {
		programs, err := h.bridge.Programs(ctx, liveCaller(s), installationID, liveProgramRequest{ChannelIDs: ids, Start: now.Unix(), End: now.Unix() + 1})
		if err != nil {
			slog.WarnContext(ctx, "jellycompat: livetv current programs failed",
				"component", "jellycompat", "installation_id", installationID, "error", err)
			continue
		}
		for _, p := range programs {
			if p.StartUnix <= now.Unix() && p.EndUnix > now.Unix() && p.EncodedID != "" {
				if _, taken := current[p.Channel.EncodedID]; !taken {
					current[p.Channel.EncodedID] = p
				}
			}
		}
	}
	for i := range items {
		if p, ok := current[items[i].ID]; ok {
			dto := mapLiveProgram(p, h.serverID)
			items[i].CurrentProgram = &dto
		}
	}
}

// serveLiveProgramImage proxies a program's Primary image under the same
// rules as channel logos.
func (h *liveTVHandlers) serveLiveProgramImage(w http.ResponseWriter, r *http.Request, rawID, imageType string) {
	if !strings.EqualFold(imageType, compatImagePrimary) {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	installationID, imageURL, ok := h.bridge.ProgramImage(r.Context(), rawID)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.proxyLiveImage(w, r, installationID, imageURL)
}
