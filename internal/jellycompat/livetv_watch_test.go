// theramindex fork: tests for the Live TV watch history.

package jellycompat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestWriteLiveWatch(t *testing.T) {
	doc := `{"theme":"dark","recentChannels":["b","a"],"continueWatching":{"a":{"itemKind":"channel","itemId":"a","itemName":"A","playedAt":5,"plays":2}},` +
		`"profiles":{"p1":{"favorites":{"b":true}}}}`

	out, err := writeLiveWatch(doc, "p1", "a", "A HD", 100)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Theme            string                     `json:"theme"`
		Recent           []string                   `json:"recentChannels"`
		ContinueWatching map[string]map[string]any  `json:"continueWatching"`
		AutoFavorites    map[string]bool            `json:"autoFavorites"`
		Profiles         map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dark" || got.Profiles["p1"] == nil || strings.Join(got.Recent, ",") != "a,b" {
		t.Fatalf("doc = %s", out)
	}
	if w := got.ContinueWatching["a"]; w["playedAt"] != float64(100) || w["plays"] != float64(3) || w["itemName"] != "A HD" || w["itemKind"] != "channel" {
		t.Fatalf("continueWatching.a = %v", w)
	}
	if !got.AutoFavorites["a"] {
		t.Fatalf("third play of an unfavorited channel is not suggested: %s", out)
	}

	// A favorited channel is never suggested, and the history stays capped.
	out, err = writeLiveWatch(`{"continueWatching":{"b":{"playedAt":1,"plays":9}},"profiles":{"p1":{"favorites":{"b":true}}}}`, "p1", "b", "", 7)
	if err != nil || strings.Contains(out, "autoFavorites") {
		t.Fatalf("favorite suggested: %s %v", out, err)
	}
	doc = `{}`
	for i := range liveTVRecentChannels + 5 {
		if doc, err = writeLiveWatch(doc, "", string(rune('A'+i)), "", int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(doc[strings.Index(doc, `"recentChannels"`):], `","`) + 1; n != liveTVRecentChannels {
		t.Fatalf("recentChannels holds %d, want %d", n, liveTVRecentChannels)
	}

	if _, err := writeLiveWatch(`{"continueWatching":[]}`, "", "a", "", 1); err == nil {
		t.Fatal("a continueWatching that is not an object was overwritten")
	}
}

// Channels carry their last play, and SortBy=DatePlayed puts the most
// recently watched first with never-played channels after, in lineup order.
func TestLiveChannelsSortByDatePlayed(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"continueWatching":{"m3u:sports.one":{"playedAt":100,"plays":1},"xtream:42":{"playedAt":200,"plays":4}}}`)

	type item struct {
		Name     string `json:"Name"`
		UserData struct {
			PlayCount      int    `json:"PlayCount"`
			LastPlayedDate string `json:"LastPlayedDate"`
		} `json:"UserData"`
	}
	list := func(query string) []item {
		rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Channels?"+query, liveTokenP1)
		if rec.Code != http.StatusOK {
			t.Fatalf("channels = %d %s", rec.Code, rec.Body.String())
		}
		return decodeLiveJSON[struct{ Items []item }](t, rec).Items
	}
	names := func(items []item) string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Name
		}
		return strings.Join(out, "|")
	}

	recent := list("SortBy=DatePlayed&SortOrder=Descending")
	if got := names(recent); got != "News Tonight|Sports One|News HD" {
		t.Fatalf("descending = %s", got)
	}
	if recent[0].UserData.PlayCount != 4 || recent[0].UserData.LastPlayedDate != "1970-01-01T00:03:20Z" || recent[2].UserData.LastPlayedDate != "" {
		t.Fatalf("user data = %+v", recent)
	}
	if got := names(list("SortBy=DatePlayed")); got != "News HD|Sports One|News Tonight" {
		t.Fatalf("ascending = %s", got)
	}
}

// A start report records the play once in the account's watch history.
func TestLiveStartReportRecordsWatch(t *testing.T) {
	env, _, _ := newLiveSessionEnv(t)
	id := liveChannelIDFor("m3u:news.hd")
	info := livePlaybackInfoOnDevice(t, env.router, id, "tv-1")
	report := `{"ItemId":"` + id + `","PlaySessionId":"` + info.PlaySessionID + `"}`
	for range 2 {
		if rec := liveReport(t, env.router, "/Sessions/Playing", report); rec.Code != http.StatusNoContent {
			t.Fatalf("start = %d", rec.Code)
		}
	}
	prefs := readLiveProfilePrefs(env.prefs.stored(1, liveTestInstall)[liveTVPrefsKey], "p1")
	if w := prefs.Watched["m3u:news.hd"]; w.Plays != 1 || w.PlayedAt != env.bridge.now().Unix() {
		t.Fatalf("watched = %+v", prefs.Watched)
	}
}
