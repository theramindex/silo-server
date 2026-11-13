package notifications

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Exercises the migrated persistence boundary: schedule correction, concurrent
// hosts, account isolation, completed-episode interest and inbox retention.
func TestPremiereSchedulePersistenceAndIsolation(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	for _, table := range []string{"profile_series_interest", "profile_series_notification_mutes", "premiere_preferences", "premiere_channel_mappings", "premiere_receipts"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	c := seedAccessCatalog(t, pool)
	channels := func(_ context.Context, user int, _ string) ([]PremiereChannel, error) {
		return []PremiereChannel{{ID: fmt.Sprintf("channel-%d", user), Name: "FX", URL: "/api/v2/plugin-content/plugins/1/dispatcharr#/watch/fx"}}, nil
	}
	scopes := scopeByProfile{"default": {}}
	makeService := func() *PremiereService {
		return NewPremiereService(pool, NewSystem(pool, nil, nil, scopes, nil, nil, nil, nil, nil, nil), channels)
	}
	s := makeService()
	for _, user := range []int{1, 2} {
		if err := s.SaveConfiguration(ctx, user, "default", PremierePreferences{Enabled: true, WindowMinutes: 60}, []PremiereMapping{{Broadcaster: "FX", ChannelIDs: []string{fmt.Sprintf("channel-%d", user)}, WindowMinutes: 60}}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO profile_series_interest(user_id,profile_id,library_id,series_id,favorite,continue_watching) VALUES($1,'default',$2,$3,$4,true)`, user, c.library, c.series, user == 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveConfiguration(ctx, 2, "default", PremierePreferences{Enabled: true, WindowMinutes: 60}, []PremiereMapping{{Broadcaster: "FX", ChannelIDs: []string{"channel-1"}, WindowMinutes: 60}}); !errors.Is(err, ErrPremiereInvalid) {
		t.Fatal("foreign channel accepted", err)
	}
	// Two different broadcasters must not create conflicting overrides for one series.
	if err := s.SaveConfiguration(ctx, 1, "default", PremierePreferences{Enabled: true, WindowMinutes: 60}, []PremiereMapping{
		{Broadcaster: "FX", SeriesID: c.series, ChannelIDs: []string{"channel-1"}, WindowMinutes: 60},
		{Broadcaster: "FXX", SeriesID: c.series, ChannelIDs: []string{"channel-1"}, WindowMinutes: 60},
	}); !errors.Is(err, ErrPremiereInvalid) {
		t.Fatal("conflicting series overrides accepted", err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := now.Add(-time.Minute).In(loc)
	setTime := func(at time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE media_items SET networks=ARRAY['FX'],air_time=$2,air_timezone='America/New_York' WHERE content_id=$1`, c.series, at.Format("15:04")); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE episodes SET air_date=$2::date WHERE content_id=$1`, c.episode, at.Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}
	}
	setTime(start.Add(time.Hour))
	if err := s.Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE type='episode.premiere'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("future metadata sent early", count, err)
	}
	setTime(start)
	events, err := s.Events(ctx, 1, "default", c.episode, catalog.AccessFilter{}, now)
	if err != nil || len(events) != 1 || !events[0].Active || events[0].Channels[0].ID != "channel-1" {
		t.Fatal(events, err)
	}
	hidden, err := s.Events(ctx, 1, "default", c.episode, catalog.AccessFilter{AllowedLibraryIDs: []int{}}, now)
	if err != nil || len(hidden) != 0 {
		t.Fatal("inaccessible calendar leaked", hidden, err)
	}
	// Metadata can change after listing but before transactional admission.
	setTime(start.Add(time.Hour))
	if err := s.deliver(ctx, profileKey{1, "default"}, events[0]); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM premiere_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale schedule consumed reminder", count, err)
	}
	setTime(start)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- makeService().Sweep(ctx, now) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE type='episode.premiere'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate or progress-only notification", count, err)
	}
	var owner int
	if err := pool.QueryRow(ctx, `SELECT user_id FROM notification_deliveries WHERE type='episode.premiere'`).Scan(&owner); err != nil || owner != 1 {
		t.Fatal("wrong account", owner, err)
	}
	// A completed episode establishes interest, even without favorite/watchlist.
	if _, err := pool.Exec(ctx, `UPDATE profile_series_interest SET last_completed_episode_key=10001 WHERE user_id=2`); err != nil {
		t.Fatal(err)
	}
	if err := makeService().Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM premiere_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatal("completion did not establish interest", count, err)
	}
	// Retention and a fresh service instance must not recreate the alert.
	if _, err := pool.Exec(ctx, `DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	if err := makeService().Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE type='episode.premiere'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("retention/restart duplicated reminder", count, err)
	}
	// A revoked profile scope is a denial, not a fallback to unrestricted access.
	scopes["default"] = access.Scope{AllowedLibraryIDs: []int{}}
	if _, err := pool.Exec(ctx, `DELETE FROM premiere_receipts`); err != nil {
		t.Fatal(err)
	}
	if err := makeService().Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE type='episode.premiere'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked access still notified", count, err)
	} // Unavailable channels cannot prevent a user from turning reminders off.
	offline := NewPremiereService(pool, s.system, func(context.Context, int, string) ([]PremiereChannel, error) {
		return nil, errors.New("provider offline")
	})
	cfg, err := offline.Configuration(ctx, 1, "default")
	if err != nil || cfg.ChannelsAvailable || len(cfg.Mappings) != 1 {
		t.Fatal("offline settings unreadable", cfg, err)
	}
	cfg.Preferences.Enabled = false
	if err := offline.SaveConfiguration(ctx, 1, "default", cfg.Preferences, cfg.Mappings); err != nil {
		t.Fatal("offline provider blocked disable", err)
	}
	cfg, err = offline.Configuration(ctx, 1, "default")
	if err != nil || cfg.Preferences.Enabled {
		t.Fatal("disable was not persisted", cfg, err)
	}

}
