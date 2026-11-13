package notifications

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Pins missing timing, Eastern DST conversion, window boundaries and duplicate
// channel selection: all can otherwise produce a wrong-time Watch action.
func TestPremiereWindow(t *testing.T) {
	date := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	ev := catalog.CalendarEvent{Type: "episode", ContentID: "episode", SeriesID: new("series"), SeasonNumber: new(2), EpisodeNumber: new(4), AirDate: date, AirTime: new("21:00"), AirTimezone: new("America/New_York")}
	m := PremiereMapping{Broadcaster: "FX", ChannelIDs: []string{"b", "a", "b", "removed"}, WindowMinutes: 60}
	channels := []PremiereChannel{{ID: "a", URL: "/api/v2/plugin-content/plugins/1/tv#/a"}, {ID: "b", URL: "/api/v2/plugin-content/plugins/2/tv#/b"}}
	start := time.Date(2026, 3, 9, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		now          time.Time
		active, want bool
	}{
		{"before", start.Add(-time.Second), false, true},
		{"starts", start, true, true},
		{"last second", start.Add(time.Hour - time.Second), true, true},
		{"ended", start.Add(time.Hour), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := premiereWindow(ev, &m, channels, tc.now)
			if (got != nil) != tc.want {
				t.Fatalf("event=%+v", got)
			}
			if got != nil && (!got.StartsAt.Equal(start) || got.Active != tc.active || len(got.Channels) != 2 || got.Channels[0].ID != "b") {
				t.Fatalf("wrong instant, state or channel grouping: %+v", got)
			}
		})
	}
	for _, field := range []string{"time", "timezone"} {
		t.Run("missing "+field, func(t *testing.T) {
			copy := ev
			if field == "time" {
				copy.AirTime = nil
			} else {
				copy.AirTimezone = nil
			}
			if got := premiereWindow(copy, &m, channels, start); got != nil {
				t.Fatal("date-only metadata created a timed shortcut")
			}
		})
	}
	channels[0].URL = "//external.example/a"
	channels[1].URL = "https://external.example/b"
	if got := premiereWindow(ev, &m, channels, start); got != nil {
		t.Fatal("external playback destinations admitted")
	}
}

func TestPremiereBroadcasterAmbiguityAndOverride(t *testing.T) {
	mappings := []PremiereMapping{{Broadcaster: "FX"}, {Broadcaster: "Hulu"}}
	if got := selectPremiereMapping(mappings, "show", []string{" FX "}); got == nil || got.Broadcaster != "FX" {
		t.Fatal(got)
	}
	if got := selectPremiereMapping(mappings, "show", []string{"FX", "Hulu"}); got != nil {
		t.Fatal("guessed between multiple broadcasters")
	}
	mappings = append(mappings, PremiereMapping{Broadcaster: "FX", SeriesID: "show"})
	if got := selectPremiereMapping(mappings, "show", []string{"FX", "Hulu"}); got == nil || got.SeriesID != "show" {
		t.Fatal("show override did not resolve ambiguity")
	}
}
