package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"
)

// Ramindex extension: expected metadata premieres, independent of EPG and DVR.
const DeliveryTypePremiere = "episode.premiere"

var ErrPremiereInvalid = errors.New("invalid premiere configuration")
var errPremiereSuppressed = errors.New("premiere already handled or suppressed")

type PremiereChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

type PremiereMapping struct {
	Broadcaster   string   `json:"broadcaster" minLength:"1" maxLength:"200"`
	SeriesID      string   `json:"series_id,omitempty" maxLength:"512"`
	ChannelIDs    []string `json:"channel_ids" minItems:"1" maxItems:"50"`
	WindowMinutes int      `json:"window_minutes" minimum:"15" maximum:"240"`
}

type PremierePreferences struct {
	Enabled       bool `json:"enabled"`
	WindowMinutes int  `json:"window_minutes" minimum:"15" maximum:"240"`
}

type PremiereConfiguration struct {
	Preferences       PremierePreferences `json:"preferences"`
	Mappings          []PremiereMapping   `json:"mappings"`
	Channels          []PremiereChannel   `json:"channels"`
	ChannelsAvailable bool                `json:"channels_available"`
}

type PremiereEvent struct {
	EpisodeID    string            `json:"episode_id"`
	SeriesID     string            `json:"series_id"`
	Title        string            `json:"title"`
	EpisodeTitle string            `json:"episode_title"`
	Season       int               `json:"season"`
	Episode      int               `json:"episode"`
	StartsAt     time.Time         `json:"starts_at"`
	EndsAt       time.Time         `json:"ends_at"`
	Active       bool              `json:"active"`
	Broadcaster  string            `json:"broadcaster"`
	Channels     []PremiereChannel `json:"channels"`
}

// PremiereChannels resolves only the authenticated recipient's current lineup.
type PremiereChannels func(context.Context, int, string) ([]PremiereChannel, error)

type PremiereService struct {
	pool     *pgxpool.Pool
	system   *System
	channels PremiereChannels
}

func NewPremiereService(pool *pgxpool.Pool, system *System, channels PremiereChannels) *PremiereService {
	return &PremiereService{pool: pool, system: system, channels: channels}
}

func (s *PremiereService) Configuration(ctx context.Context, user int, profile string) (PremiereConfiguration, error) {
	cfg := PremiereConfiguration{Preferences: PremierePreferences{Enabled: true, WindowMinutes: 60}, Mappings: []PremiereMapping{}, Channels: []PremiereChannel{}, ChannelsAvailable: s.channels != nil}
	err := s.pool.QueryRow(ctx, `SELECT enabled,window_minutes FROM premiere_preferences WHERE user_id=$1 AND profile_id=$2`, user, profile).Scan(&cfg.Preferences.Enabled, &cfg.Preferences.WindowMinutes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return cfg, err
	}
	rows, err := s.pool.Query(ctx, `SELECT broadcaster,series_id,channel_ids,window_minutes FROM premiere_channel_mappings WHERE user_id=$1 AND profile_id=$2 ORDER BY broadcaster,series_id`, user, profile)
	if err != nil {
		return cfg, err
	}
	for rows.Next() {
		var m PremiereMapping
		if err := rows.Scan(&m.Broadcaster, &m.SeriesID, &m.ChannelIDs, &m.WindowMinutes); err != nil {
			rows.Close()
			return cfg, err
		}
		cfg.Mappings = append(cfg.Mappings, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return cfg, err
	}
	if s.channels != nil {
		cfg.Channels, err = s.channels(ctx, user, profile)
		if err != nil {
			slog.WarnContext(ctx, "premiere channel lineup unavailable", "error", err)
			cfg.Channels = []PremiereChannel{}
			cfg.ChannelsAvailable = false
		}
	}
	return cfg, nil
}

// SaveConfiguration atomically replaces only this profile's mappings. Channel
// ids are validated against its own lineup, including private-provider scope.
func (s *PremiereService) SaveConfiguration(ctx context.Context, user int, profile string, prefs PremierePreferences, mappings []PremiereMapping) error {
	if prefs.WindowMinutes < 15 || prefs.WindowMinutes > 240 || len(mappings) > 100 || s.channels == nil {
		return ErrPremiereInvalid
	}
	var channels []PremiereChannel
	var err error
	// Turning reminders off must work even if a provider or saved channel vanished.
	if prefs.Enabled {
		channels, err = s.channels(ctx, user, profile)
		if err != nil {
			return err
		}
	}
	known := map[string]bool{}
	for _, ch := range channels {
		known[ch.ID] = true
	}
	seen := map[string]bool{}
	for i := range mappings {
		m := &mappings[i]
		m.Broadcaster = strings.TrimSpace(m.Broadcaster)
		key := "network:" + strings.ToLower(m.Broadcaster)
		if m.SeriesID != "" {
			key = "series:" + m.SeriesID
		}
		if m.Broadcaster == "" || len(m.Broadcaster) > 200 || len(m.SeriesID) > 512 || len(m.ChannelIDs) == 0 || len(m.ChannelIDs) > 50 || seen[key] || m.WindowMinutes < 15 || m.WindowMinutes > 240 {
			return ErrPremiereInvalid
		}
		seen[key] = true
		for _, id := range m.ChannelIDs {
			if prefs.Enabled && !known[id] {
				return ErrPremiereInvalid
			}
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// This row serializes concurrent replacements, including first saves.
	_, err = tx.Exec(ctx, `INSERT INTO premiere_preferences(user_id,profile_id,enabled,window_minutes) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,profile_id) DO UPDATE SET enabled=EXCLUDED.enabled,window_minutes=EXCLUDED.window_minutes`, user, profile, prefs.Enabled, prefs.WindowMinutes)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM premiere_channel_mappings WHERE user_id=$1 AND profile_id=$2`, user, profile); err != nil {
		return err
	}
	for _, m := range mappings {
		if _, err = tx.Exec(ctx, `INSERT INTO premiere_channel_mappings(user_id,profile_id,broadcaster,series_id,channel_ids,window_minutes) VALUES($1,$2,$3,$4,$5,$6)`, user, profile, m.Broadcaster, m.SeriesID, m.ChannelIDs, m.WindowMinutes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func selectPremiereMapping(mappings []PremiereMapping, series string, networks []string) *PremiereMapping {
	// A per-show override wins. Ambiguous broadcasters need an explicit choice.
	for i := range mappings {
		if mappings[i].SeriesID == series {
			return &mappings[i]
		}
	}
	var selected *PremiereMapping
	for i := range mappings {
		m := &mappings[i]
		if m.SeriesID != "" {
			continue
		}
		if slices.ContainsFunc(networks, func(n string) bool { return strings.EqualFold(strings.TrimSpace(n), m.Broadcaster) }) {
			if selected != nil {
				return nil
			}
			selected = m
		}
	}
	return selected
}

func premiereWindow(ev catalog.CalendarEvent, mapping *PremiereMapping, channels []PremiereChannel, now time.Time) *PremiereEvent {
	if ev.Type != "episode" || ev.SeriesID == nil || ev.SeasonNumber == nil || ev.EpisodeNumber == nil || mapping == nil {
		return nil
	}
	start := catalog.CalendarEventAirAt(ev.AirDate, ev.AirTime, ev.AirTimezone)
	if start == nil {
		return nil
	}
	end := start.Add(time.Duration(mapping.WindowMinutes) * time.Minute)
	if !now.Before(end) {
		return nil
	}
	available := []PremiereChannel{}
	seen := map[string]bool{}
	for _, id := range mapping.ChannelIDs {
		for _, ch := range channels {
			if ch.ID == id && !seen[id] && ch.URL != "" && validPremiereURL(ch.URL) {
				available = append(available, ch)
				seen[id] = true
				break
			}
		}
	}
	if len(available) == 0 {
		return nil
	}
	title := ""
	if ev.EpisodeTitle != nil {
		title = *ev.EpisodeTitle
	}
	return &PremiereEvent{EpisodeID: ev.ContentID, SeriesID: *ev.SeriesID, Title: ev.Title, EpisodeTitle: title, Season: *ev.SeasonNumber, Episode: *ev.EpisodeNumber, StartsAt: *start, EndsAt: end, Active: !now.Before(*start), Broadcaster: mapping.Broadcaster, Channels: available}
}

// Events shares the access-filtered calendar source. Re-reading the durable
// metadata schedule reconciles changes without a second copy of pending jobs.
func (s *PremiereService) Events(ctx context.Context, user int, profile, item string, af catalog.AccessFilter, now time.Time) ([]PremiereEvent, error) {
	return s.events(ctx, user, profile, item, af, now, nil)
}

func (s *PremiereService) events(ctx context.Context, user int, profile, item string, af catalog.AccessFilter, now time.Time, restrict []string) ([]PremiereEvent, error) {
	out := []PremiereEvent{}
	cfg, err := s.Configuration(ctx, user, profile)
	if err != nil {
		return nil, err
	}
	if len(cfg.Mappings) == 0 {
		return out, nil
	}
	filter := catalog.CalendarFilter{Start: now.UTC().AddDate(0, 0, -2), End: now.UTC().AddDate(0, 0, 14), AllowedLibraryIDs: af.AllowedLibraryIDs, DisabledLibraryIDs: af.DisabledLibraryIDs, MaturityLimits: af.MaturityLimits}
	if restrict != nil {
		filter.RestrictByIDs = true
		filter.RestrictToIDs = restrict
		filter.End = now.UTC().AddDate(0, 0, 1)
	}
	if item != "" {
		var series string
		err = s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT series_id FROM episodes WHERE content_id=$1),$1)`, item).Scan(&series)
		if err != nil {
			return nil, err
		}
		filter.RestrictByIDs = true
		filter.RestrictToIDs = []string{series}
	}
	events, err := catalog.NewCalendarRepository(s.pool).ListEvents(ctx, filter)
	if err != nil {
		return nil, err
	}
	networks := map[string][]string{}
	seriesIDs := []string{}
	for _, ev := range events {
		if ev.SeriesID != nil && ev.Type == "episode" {
			if _, ok := networks[*ev.SeriesID]; !ok {
				networks[*ev.SeriesID] = nil
				seriesIDs = append(seriesIDs, *ev.SeriesID)
			}
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT content_id,networks FROM media_items WHERE content_id=ANY($1::text[])`, seriesIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var names []string
		if err := rows.Scan(&id, &names); err != nil {
			rows.Close()
			return nil, err
		}
		networks[id] = names
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if ev.SeriesID == nil || (item != "" && item != ev.ContentID && item != *ev.SeriesID) {
			continue
		}
		if p := premiereWindow(ev, selectPremiereMapping(cfg.Mappings, *ev.SeriesID, networks[*ev.SeriesID]), cfg.Channels, now); p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *PremiereService) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := s.Sweep(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "premiere reminder sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *PremiereService) Sweep(ctx context.Context, now time.Time) error {
	if s.system == nil || !s.system.Settings.UIEnabled(ctx) {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT m.user_id,m.profile_id FROM premiere_channel_mappings m JOIN premiere_preferences p USING(user_id,profile_id) WHERE p.enabled AND EXISTS(SELECT 1 FROM profile_series_interest i JOIN episodes e ON e.series_id=i.series_id WHERE i.user_id=m.user_id AND i.profile_id=m.profile_id AND e.air_date BETWEEN ($1::timestamptz::date-2) AND ($1::timestamptz::date+1))`, now)
	if err != nil {
		return err
	}
	profiles := []profileKey{}
	for rows.Next() {
		var p profileKey
		if err := rows.Scan(&p.userID, &p.profileID); err != nil {
			rows.Close()
			return err
		}
		profiles = append(profiles, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var group errgroup.Group
	group.SetLimit(4)
	for _, p := range profiles {
		group.Go(func() error { return s.sweepProfile(ctx, p, now) })
	}
	return group.Wait()
}

func (s *PremiereService) sweepProfile(ctx context.Context, p profileKey, now time.Time) error {
	// Resolve only opted-in series that could have a premiere near this sweep.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT i.series_id FROM profile_series_interest i
 JOIN episodes e ON e.series_id=i.series_id
 LEFT JOIN notification_preferences np ON np.user_id=i.user_id AND np.profile_id=i.profile_id
 WHERE i.user_id=$1 AND i.profile_id=$2 AND COALESCE(np.enabled,true)
 AND e.air_date BETWEEN ($3::timestamptz::date-2) AND ($3::timestamptz::date+1)
 AND NOT EXISTS(SELECT 1 FROM profile_series_notification_mutes m WHERE m.user_id=i.user_id AND m.profile_id=i.profile_id AND m.series_id=i.series_id)
 AND ((i.favorite AND COALESCE(np.notify_favorites,true)) OR (i.watchlist AND COALESCE(np.notify_watchlist,true)) OR (i.last_completed_episode_key IS NOT NULL AND COALESCE(np.notify_next_up,true)))`, p.userID, p.profileID, now)
	if err != nil {
		return err
	}
	series := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		series = append(series, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(series) == 0 {
		return err
	}
	scope, err := newRecipientAccess(s.system.scopes).scope(ctx, p.userID, p.profileID)
	if err != nil || scope == nil {
		return err
	}
	af := catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs, MaturityLimits: scope.MaturityLimits}
	events, err := s.events(ctx, p.userID, p.profileID, "", af, now, series)
	if err != nil {
		return err
	}
	var failures []error
	for _, ev := range events {
		if !ev.Active || now.Sub(ev.StartsAt) > 5*time.Minute {
			continue
		}
		if err := s.deliver(ctx, p, ev); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *PremiereService) deliver(ctx context.Context, p profileKey, ev PremiereEvent) error {
	if s.system.stores != nil {
		store, err := s.system.stores.ForUser(ctx, p.userID)
		if err != nil {
			return err
		}
		progress, err := store.ListProgressByMediaItems(ctx, p.profileID, []string{ev.EpisodeID})
		if err != nil {
			return err
		}
		if progress[ev.EpisodeID].Completed {
			return nil
		}
		history, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{ProfileID: p.profileID, MediaItemIDs: []string{ev.EpisodeID}, Limit: 1})
		if err != nil {
			return err
		}
		if len(history) != 0 {
			return nil
		}
	}
	stamp := ev.EndsAt.UnixMilli()
	key := fmt.Sprintf("%s:S%dE%d", ev.SeriesID, ev.Season, ev.Episode)
	flags, _ := json.Marshal(premiereNotice{Source: "silo.premieres", EventID: key, Title: truncateDisplayText(ev.Title, 200), Message: fmt.Sprintf("S%02dE%02d — premiere expected now on %s", ev.Season, ev.Episode, ev.Broadcaster), URL: "/item/" + url.PathEscape(ev.EpisodeID) + "?premiere=1", ExpiresAt: &stamp})
	_, err := s.system.DispatchOperational(ctx, Delivery{ID: ulid.Make().String(), UserID: p.userID, ProfileID: p.profileID, Type: DeliveryTypePremiere, SeriesID: &ev.SeriesID, EpisodeID: &ev.EpisodeID, ReasonFlags: flags}, OperationalDispatch{admit: func(ctx context.Context, tx pgx.Tx) error {
		// Hold the metadata rows until commit so a correction cannot race admission.
		var date time.Time
		var airtime, timezone *string
		err := tx.QueryRow(ctx, `SELECT e.air_date,mi.air_time,mi.air_timezone FROM episodes e JOIN media_items mi ON mi.content_id=e.series_id WHERE e.content_id=$1 AND e.series_id=$2 AND e.air_date IS NOT NULL FOR SHARE OF e,mi`, ev.EpisodeID, ev.SeriesID).Scan(&date, &airtime, &timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			return errPremiereSuppressed
		}
		if err != nil {
			return err
		}
		start := catalog.CalendarEventAirAt(date, airtime, timezone)
		instant := time.Now()
		if start == nil || !start.Equal(ev.StartsAt) || instant.Before(*start) || instant.Sub(*start) > 5*time.Minute || !instant.Before(ev.EndsAt) {
			return errPremiereSuppressed
		}
		var enabled bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM premiere_preferences pp WHERE pp.user_id=$1 AND pp.profile_id=$2 AND pp.enabled)
		AND EXISTS(SELECT 1 FROM premiere_channel_mappings pm WHERE pm.user_id=$1 AND pm.profile_id=$2 AND pm.broadcaster=$4 AND (pm.series_id=$3 OR pm.series_id=''))
 AND COALESCE((SELECT enabled FROM notification_preferences WHERE user_id=$1 AND profile_id=$2),true)
		AND NOT EXISTS(SELECT 1 FROM profile_series_notification_mutes WHERE user_id=$1 AND profile_id=$2 AND series_id=$3)
		AND EXISTS(SELECT 1 FROM profile_series_interest i LEFT JOIN notification_preferences np ON np.user_id=i.user_id AND np.profile_id=i.profile_id WHERE i.user_id=$1 AND i.profile_id=$2 AND i.series_id=$3 AND ((i.favorite AND COALESCE(np.notify_favorites,true)) OR (i.watchlist AND COALESCE(np.notify_watchlist,true)) OR (i.last_completed_episode_key IS NOT NULL AND COALESCE(np.notify_next_up,true))))`, p.userID, p.profileID, ev.SeriesID, ev.Broadcaster).Scan(&enabled)
		if err != nil {
			return err
		}
		if !enabled {
			return errPremiereSuppressed
		}
		result, err := tx.Exec(ctx, `INSERT INTO premiere_receipts(user_id,profile_id,episode_key) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.userID, p.profileID, key)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return errPremiereSuppressed
		}
		return nil
	}})
	if errors.Is(err, errPremiereSuppressed) {
		return nil
	}
	return err
}
