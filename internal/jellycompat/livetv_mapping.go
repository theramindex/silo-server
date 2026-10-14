// theramindex fork: maps Live TV channels to Jellyfin DTOs and applies /LiveTv/Channels queries.

package jellycompat

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// liveTVViewID is the Live TV UserView. Byte 1 is 0x00 rather than
// liveChannelTag, so isLiveChannelID never accepts it.
const liveTVViewID = "f1000000000000000000000000000001"

// Jellyfin DTO values the bridge emits for channels and their media sources.
const (
	liveTVCollectionType = "livetv"
	liveTVItemType       = "TvChannel"
	liveTVMediaType      = "Video"
	liveTVPlayAccess     = "Full"
	liveTVSourceType     = "Default"
	liveTVProfileMPEGTS  = "mpegts"
	liveTVLocationRemote = "Remote"
)

var liveTVViewUUID = uuid.MustParse(liveTVViewID)

// isLiveTVViewID reports whether raw names the Live TV view, in the compact or
// dashed form.
func isLiveTVViewID(raw string) bool {
	if raw == "" {
		return false
	}
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed == liveTVViewUUID
}

// liveChannelDTO is a Jellyfin BaseItemDto for a TvChannel.
type liveChannelDTO struct {
	Name              string            `json:"Name"`
	ServerID          string            `json:"ServerId,omitempty"`
	ID                string            `json:"Id"`
	ChannelNumber     string            `json:"ChannelNumber,omitempty"`
	Number            string            `json:"Number,omitempty"`
	Type              string            `json:"Type"`
	ChannelType       string            `json:"ChannelType"`
	MediaType         string            `json:"MediaType"`
	IsFolder          bool              `json:"IsFolder"`
	LocationType      string            `json:"LocationType"`
	SortName          string            `json:"SortName,omitempty"`
	ParentID          string            `json:"ParentId,omitempty"`
	ImageTags         map[string]string `json:"ImageTags"`
	BackdropImageTags []string          `json:"BackdropImageTags"`
	UserData          *itemUserDataDTO  `json:"UserData,omitempty"`
	PlayAccess        string            `json:"PlayAccess"`
	Genres            []string          `json:"Genres"`
	CurrentProgram    *liveProgramDTO   `json:"CurrentProgram,omitempty"`
}

type liveChannelQueryResult struct {
	Items            []liveChannelDTO `json:"Items"`
	TotalRecordCount int              `json:"TotalRecordCount"`
	StartIndex       int              `json:"StartIndex"`
}

// liveLogoUsable matches the check in LiveTV.LogoURL: only an absolute http(s)
// logo can be proxied, so only those get an image tag.
func liveLogoUsable(logoURL string) bool {
	return strings.HasPrefix(logoURL, "http://") || strings.HasPrefix(logoURL, "https://")
}

func liveLogoTag(logoURL string) string {
	sum := sha256.Sum256([]byte(logoURL))
	return hex.EncodeToString(sum[:])[:16]
}

func mapLiveChannel(ch liveChannel, serverID string, prefs liveProfilePrefs) liveChannelDTO {
	dto := liveChannelDTO{
		Name:              ch.Name,
		ServerID:          serverID,
		ID:                ch.EncodedID,
		ChannelNumber:     ch.Number,
		Number:            ch.Number,
		Type:              liveTVItemType,
		ChannelType:       "TV",
		MediaType:         liveTVMediaType,
		LocationType:      liveTVLocationRemote,
		SortName:          strings.ToLower(ch.Name),
		ParentID:          liveTVViewID,
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		UserData: &itemUserDataDTO{
			Key:        ch.EncodedID,
			ItemID:     ch.EncodedID,
			IsFavorite: prefs.Favorites[ch.ID],
		},
		PlayAccess: liveTVPlayAccess,
		Genres:     []string{},
	}
	if liveLogoUsable(ch.LogoURL) {
		dto.ImageTags[compatImagePrimary] = liveLogoTag(ch.LogoURL)
	}
	if ch.Group != "" {
		dto.Genres = []string{ch.Group}
	}
	if w, ok := prefs.Watched[ch.ID]; ok {
		dto.UserData.PlayCount = w.Plays
		dto.UserData.LastPlayedDate = formatCompatTime(time.Unix(w.PlayedAt, 0))
	}
	return dto
}

// queryLiveChannels filters, sorts and pages channels for /LiveTv/Channels and
// the Live TV view's /Items listing. prefs is keyed by installation. Paging is
// applied last, and TotalRecordCount counts the matches before paging.
func queryLiveChannels(channels []liveChannel, prefs map[int]liveProfilePrefs, q caseInsensitiveQuery, serverID string) liveChannelQueryResult {
	isFavorite := func(ch liveChannel) bool { return prefs[ch.InstallationID].Favorites[ch.ID] }

	list := slices.Clone(channels)
	if want, err := strconv.ParseBool(q.Get("IsFavorite")); err == nil {
		list = slices.DeleteFunc(list, func(ch liveChannel) bool { return isFavorite(ch) != want })
	}

	sortBy, _, _ := strings.Cut(q.Get("SortBy"), ",")
	sortOrder, _, _ := strings.Cut(q.Get("SortOrder"), ",")
	descending := strings.EqualFold(strings.TrimSpace(sortOrder), "Descending")
	switch sortBy = strings.TrimSpace(sortBy); {
	case sortBy == "":
		// Provider order.
	case strings.EqualFold(sortBy, "SortName"):
		slices.SortStableFunc(list, func(a, b liveChannel) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
	case strings.EqualFold(sortBy, "DatePlayed"):
		// Android TV's "recently watched" channel order. Channels never played
		// keep provider order, after the played ones when descending and before
		// them when ascending, as Jellyfin's null dates sort.
		playedAt := func(ch liveChannel) int64 { return prefs[ch.InstallationID].Watched[ch.ID].PlayedAt }
		var played, unplayed []liveChannel
		for _, ch := range list {
			if playedAt(ch) > 0 {
				played = append(played, ch)
			} else {
				unplayed = append(unplayed, ch)
			}
		}
		slices.SortStableFunc(played, func(a, b liveChannel) int {
			if descending {
				return cmp.Compare(playedAt(b), playedAt(a))
			}
			return cmp.Compare(playedAt(a), playedAt(b))
		})
		if descending {
			list = append(played, unplayed...)
		} else {
			list = append(unplayed, played...)
		}
		descending = false
	default:
		slices.SortStableFunc(list, compareLiveChannelNumbers)
	}
	if descending {
		slices.Reverse(list)
	}

	if favSort, _ := strconv.ParseBool(q.Get("EnableFavoriteSorting")); favSort {
		rank := func(ch liveChannel) int {
			if i := slices.Index(prefs[ch.InstallationID].FavoriteOrder, ch.ID); i >= 0 {
				return i
			}
			return math.MaxInt
		}
		var favorites, rest []liveChannel
		for _, ch := range list {
			if isFavorite(ch) {
				favorites = append(favorites, ch)
			} else {
				rest = append(rest, ch)
			}
		}
		slices.SortStableFunc(favorites, func(a, b liveChannel) int { return cmp.Compare(rank(a), rank(b)) })
		list = append(favorites, rest...)
	}

	total := len(list)
	startIndex, _ := strconv.Atoi(q.Get("StartIndex"))
	startIndex = max(startIndex, 0)
	start, end := min(startIndex, total), total
	if limit, err := strconv.Atoi(q.Get("Limit")); err == nil && limit > 0 {
		end = min(start+limit, total)
	}

	items := make([]liveChannelDTO, 0, end-start)
	for _, ch := range list[start:end] {
		items = append(items, mapLiveChannel(ch, serverID, prefs[ch.InstallationID]))
	}
	return liveChannelQueryResult{Items: items, TotalRecordCount: total, StartIndex: startIndex}
}

// compareLiveChannelNumbers orders numeric channel numbers numerically, ahead
// of non-numeric ones, which compare as strings.
func compareLiveChannelNumbers(a, b liveChannel) int {
	an, aErr := strconv.ParseFloat(a.Number, 64)
	bn, bErr := strconv.ParseFloat(b.Number, 64)
	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a.Number, b.Number)
}
