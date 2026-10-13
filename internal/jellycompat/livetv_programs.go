// theramindex fork: fetches and caches livetv/v1 guide programs.

package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// liveGuideCacheTTL bounds how long one provider programs response is
	// reused. Guide pages and home sections repeat the same requests.
	liveGuideCacheTTL = 60 * time.Second
	// liveGuideCacheMax caps the cached responses; past it, expired ones are
	// dropped, and if none are, the whole cache is.
	liveGuideCacheMax = 512
	// liveProgramImageTTL and liveProgramImageMax bound the remembered program
	// images the same way.
	liveProgramImageTTL = 12 * time.Hour
	liveProgramImageMax = 100_000
	// liveProgramsMaxChannels is the contract's channel_ids limit.
	liveProgramsMaxChannels = 2000
	// liveProgramsMaxLimit is the contract's ceiling for an unscoped request.
	liveProgramsMaxLimit = 2000
)

// liveProgramRequest is one programs call to a provider. Start and End are
// unix seconds, rounded outward to whole minutes so nearby requests share a
// cache entry; callers filter the result to their exact window.
type liveProgramRequest struct {
	ChannelIDs []string // provider channel ids; nil means every channel
	Start, End int64
	Class      string
	Limit      int // 0 lets the provider decide
}

func (q liveProgramRequest) cacheKey(installationID int) string {
	return fmt.Sprintf("%d|%d|%d|%s|%d|%s", installationID, q.Start, q.End, q.Class, q.Limit, strings.Join(q.ChannelIDs, ","))
}

type liveGuideEntry struct {
	programs []liveTVProgram
	at       time.Time
}

// liveProgramImage is the image a program was listed with, and the
// installation to check its host against.
type liveProgramImage struct {
	installationID int
	url            string
	at             time.Time
}

// liveGuideProgram is a provider program tagged with its installation and the
// compat ids clients see.
type liveGuideProgram struct {
	liveTVProgram
	InstallationID int
	EncodedID      string // "" when the start cannot be encoded
	Channel        liveChannel
}

func roundDownMinute(unix int64) int64 { return unix - ((unix%60)+60)%60 }

func roundUpMinute(unix int64) int64 {
	if r := roundDownMinute(unix); r != unix {
		return r + 60
	}
	return unix
}

// Programs asks one installation for programs. Responses are cached for
// liveGuideCacheTTL and concurrent identical requests share one call. A
// provider without the route (404) has no programs. Programs on channels not in
// the installation's lineup are dropped.
func (l *LiveTV) Programs(ctx context.Context, caller LiveTVCaller, installationID int, q liveProgramRequest) ([]liveGuideProgram, error) {
	q.Start, q.End = roundDownMinute(q.Start), roundUpMinute(q.End)
	if len(q.ChannelIDs) > liveProgramsMaxChannels {
		q.ChannelIDs = q.ChannelIDs[:liveProgramsMaxChannels]
	}
	raw, err := l.fetchPrograms(ctx, caller, installationID, q)
	if err != nil {
		return nil, err
	}
	lineup, _ := l.lineupFor(ctx, caller, installationID, false)
	out := make([]liveGuideProgram, 0, len(raw))
	for _, p := range raw {
		ch, ok := lineup.index[liveChannelKey{InstallationID: installationID, Hash: liveChannelHash(p.ChannelID)}]
		if !ok || p.EndUnix <= p.StartUnix {
			continue
		}
		out = append(out, liveGuideProgram{
			liveTVProgram:  p,
			InstallationID: installationID,
			EncodedID:      encodeLiveProgramID(installationID, p.ChannelID, p.StartUnix),
			Channel:        ch,
		})
	}
	l.rememberProgramImages(out)
	return out, nil
}

func (l *LiveTV) fetchPrograms(ctx context.Context, caller LiveTVCaller, installationID int, q liveProgramRequest) ([]liveTVProgram, error) {
	key := q.cacheKey(installationID)
	l.guideMu.Lock()
	if e, ok := l.guide[key]; ok && l.now().Sub(e.at) < liveGuideCacheTTL {
		l.guideMu.Unlock()
		return e.programs, nil
	}
	l.guideMu.Unlock()

	resCh := l.group.DoChan("programs:"+key, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveTVFetchTimeout)
		defer cancel()
		query := map[string]string{
			liveTVProgramsStartParam: strconv.FormatInt(q.Start, 10),
			liveTVProgramsEndParam:   strconv.FormatInt(q.End, 10),
		}
		if len(q.ChannelIDs) > 0 {
			query[liveTVProgramsChannelsParam] = strings.Join(q.ChannelIDs, ",")
		}
		if q.Class != "" {
			query[liveTVProgramsClassParam] = q.Class
		}
		if q.Limit > 0 {
			query[liveTVProgramsLimitParam] = strconv.Itoa(q.Limit)
		}
		status, body, err := l.routes.Call(fetchCtx, installationID, caller, http.MethodGet, liveTVPathPrograms, query)
		if err != nil {
			return nil, fmt.Errorf("livetv: programs: %w", err)
		}
		var programs []liveTVProgram
		switch status {
		case http.StatusOK:
			var resp liveTVPrograms
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, fmt.Errorf("livetv: programs: decode: %w", err)
			}
			programs = resp.Programs
		case http.StatusNotFound:
			// A provider without guide data.
		default:
			return nil, fmt.Errorf("livetv: programs: status %d", status)
		}
		if programs == nil {
			programs = []liveTVProgram{}
		}
		l.guideMu.Lock()
		l.storeGuideLocked(key, programs)
		l.guideMu.Unlock()
		return programs, nil
	})
	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, res.Err
		}
		programs, _ := res.Val.([]liveTVProgram)
		return programs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *LiveTV) storeGuideLocked(key string, programs []liveTVProgram) {
	now := l.now()
	if len(l.guide) >= liveGuideCacheMax {
		for k, e := range l.guide {
			if now.Sub(e.at) >= liveGuideCacheTTL {
				delete(l.guide, k)
			}
		}
		if len(l.guide) >= liveGuideCacheMax {
			clear(l.guide)
		}
	}
	l.guide[key] = liveGuideEntry{programs: programs, at: now}
}

func (l *LiveTV) rememberProgramImages(programs []liveGuideProgram) {
	now := l.now()
	l.guideMu.Lock()
	defer l.guideMu.Unlock()
	if len(l.programImages)+len(programs) > liveProgramImageMax {
		for k, e := range l.programImages {
			if now.Sub(e.at) >= liveProgramImageTTL {
				delete(l.programImages, k)
			}
		}
		if len(l.programImages)+len(programs) > liveProgramImageMax {
			clear(l.programImages)
		}
	}
	for _, p := range programs {
		if p.EncodedID != "" && liveLogoUsable(p.ImageURL) {
			l.programImages[p.EncodedID] = liveProgramImage{installationID: p.InstallationID, url: p.ImageURL, at: now}
		}
	}
}

// channelByPrefix finds the channel a program id names in a cached or fetched
// lineup. force refreshes the lineup, as ChannelByID does on a miss.
func (l *LiveTV) channelByPrefix(ctx context.Context, caller LiveTVCaller, key liveProgramKey) (liveChannel, bool) {
	if !slices.ContainsFunc(l.Providers(ctx), func(p LiveTVProviderRef) bool { return p.InstallationID == key.InstallationID }) {
		return liveChannel{}, false
	}
	lookup := func(lineup liveLineup) (liveChannel, bool) {
		i, ok := lineup.byPrefix[key.Channel]
		if !ok || i < 0 {
			return liveChannel{}, false
		}
		return lineup.channels[i], true
	}
	lineup, fetched := l.lineupFor(ctx, caller, key.InstallationID, false)
	if ch, ok := lookup(lineup); ok {
		return ch, true
	}
	if fetched || !l.claimForcedRefresh(key.InstallationID) {
		return liveChannel{}, false
	}
	lineup, _ = l.lineupFor(ctx, caller, key.InstallationID, true)
	return lookup(lineup)
}

// ProgramByID resolves a program compat id by asking its channel's provider
// for the programs starting in the id's minute.
func (l *LiveTV) ProgramByID(ctx context.Context, caller LiveTVCaller, rawID string) (liveGuideProgram, bool) {
	key, ok := decodeLiveProgramID(rawID)
	if !ok {
		return liveGuideProgram{}, false
	}
	ch, ok := l.channelByPrefix(ctx, caller, key)
	if !ok {
		return liveGuideProgram{}, false
	}
	start := key.StartMinute * 60
	programs, err := l.Programs(ctx, caller, key.InstallationID, liveProgramRequest{ChannelIDs: []string{ch.ID}, Start: start, End: start + 60})
	if err != nil {
		return liveGuideProgram{}, false
	}
	for _, p := range programs {
		if p.Channel.ID == ch.ID && p.StartUnix/60 == key.StartMinute {
			return p, true
		}
	}
	return liveGuideProgram{}, false
}

// ProgramImage returns the image of a program handed out earlier, or, for an
// unknown id, resolves the program anonymously.
func (l *LiveTV) ProgramImage(ctx context.Context, rawID string) (installationID int, imageURL string, ok bool) {
	parsed, err := uuid.Parse(rawID)
	if err != nil || !isLiveProgramID(rawID) {
		return 0, "", false
	}
	l.guideMu.Lock()
	img, known := l.programImages[parsed.String()]
	l.guideMu.Unlock()
	if known {
		return img.installationID, img.url, true
	}
	p, found := l.ProgramByID(ctx, LiveTVCaller{}, rawID)
	if !found || !liveLogoUsable(p.ImageURL) {
		return 0, "", false
	}
	return p.InstallationID, p.ImageURL, true
}
