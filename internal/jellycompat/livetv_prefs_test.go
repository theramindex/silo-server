// theramindex fork: tests for per-profile Live TV favorites stored in plugin settings.

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeLiveTVPrefs is a LiveTVPrefsStore backed by a map, keyed by account and
// installation. Like the real store, Set replaces all of an account's values.
// Later Live TV tests reuse it.
type fakeLiveTVPrefs struct {
	mu       sync.Mutex
	values   map[[2]int]map[string]string
	setDelay time.Duration // slept at the start of Set, so an unlocked read-modify-write loses an update
	setErr   error
	getErr   error
}

func (f *fakeLiveTVPrefs) seed(userID, installationID int, values map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = map[[2]int]map[string]string{}
	}
	f.values[[2]int{userID, installationID}] = maps.Clone(values)
}

func (f *fakeLiveTVPrefs) stored(userID, installationID int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.values[[2]int{userID, installationID}])
}

func (f *fakeLiveTVPrefs) Get(_ context.Context, userID, installationID int) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return maps.Clone(f.values[[2]int{userID, installationID}]), nil
}

func (f *fakeLiveTVPrefs) Set(_ context.Context, userID, installationID int, values map[string]string) error {
	if f.setDelay > 0 {
		time.Sleep(f.setDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[[2]int]map[string]string{}
	}
	f.values[[2]int{userID, installationID}] = maps.Clone(values)
	return nil
}

// livePrefsFixture returns the shared preferences fixture as the string the
// settings store holds under liveTVPrefsKey.
func livePrefsFixture(t *testing.T) string {
	t.Helper()
	var doc map[string]any
	readLiveTVFixture(t, "preferences_v1.json", &doc)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

// decodeLivePrefs decodes a preferences document into nested maps.
func decodeLivePrefs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("preferences are not JSON: %v\n%s", err, raw)
	}
	return doc
}

// livePath walks nested JSON objects.
func livePath(t *testing.T, doc map[string]any, keys ...string) any {
	t.Helper()
	var cur any = doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not under an object", keys, k)
		}
		cur = m[k]
	}
	return cur
}

func TestReadLiveProfilePrefsUsesProfileSection(t *testing.T) {
	got := readLiveProfilePrefs(livePrefsFixture(t), "p2")
	if want := map[string]bool{"xtream:42": true}; !reflect.DeepEqual(got.Favorites, want) {
		t.Errorf("favorites = %v, want %v", got.Favorites, want)
	}
	if want := []string{"xtream:42"}; !reflect.DeepEqual(got.FavoriteOrder, want) {
		t.Errorf("favoriteOrder = %v, want %v", got.FavoriteOrder, want)
	}
	if want := map[string]bool{"7": true}; !reflect.DeepEqual(got.Hidden, want) {
		t.Errorf("hidden = %v, want %v", got.Hidden, want)
	}
}

func TestReadLiveProfilePrefsFallsBackToLegacy(t *testing.T) {
	raw := livePrefsFixture(t)
	for _, profileID := range []string{"p1", ""} {
		got := readLiveProfilePrefs(raw, profileID)
		if want := map[string]bool{"m3u:news.hd": true}; !reflect.DeepEqual(got.Favorites, want) {
			t.Errorf("profile %q: favorites = %v, want %v", profileID, got.Favorites, want)
		}
		if len(got.Hidden) != 0 {
			t.Errorf("profile %q: hidden = %v, want none", profileID, got.Hidden)
		}
	}
}

func TestReadLiveProfilePrefsTolerantOfBadInput(t *testing.T) {
	for _, raw := range []string{"", "not json", "[1]", `{"favorites":"x","profiles":5}`} {
		got := readLiveProfilePrefs(raw, "p1")
		if got.Favorites == nil || got.Hidden == nil {
			t.Errorf("raw %q: nil maps in %+v", raw, got)
		}
		if len(got.Favorites) != 0 || len(got.FavoriteOrder) != 0 || len(got.Hidden) != 0 {
			t.Errorf("raw %q: want empty prefs, got %+v", raw, got)
		}
	}
}

func TestReadLiveProfilePrefsNullIsMissing(t *testing.T) {
	if got := readLiveProfilePrefs("null", "p1"); got.Favorites == nil || len(got.Favorites) != 0 {
		t.Errorf("null document: favorites = %v, want empty", got.Favorites)
	}
	// A null profile entry reads like a missing one: the legacy keys.
	raw := `{"favorites":{"legacy":true},"favoriteOrder":["legacy"],"profiles":{"p1":null,"p2":{"favorites":{"own":true}}}}`
	got := readLiveProfilePrefs(raw, "p1")
	if want := map[string]bool{"legacy": true}; !reflect.DeepEqual(got.Favorites, want) {
		t.Errorf("null profile entry: favorites = %v, want %v", got.Favorites, want)
	}
	if want := []string{"legacy"}; !reflect.DeepEqual(got.FavoriteOrder, want) {
		t.Errorf("null profile entry: favoriteOrder = %v, want %v", got.FavoriteOrder, want)
	}
	// Other profiles are unaffected.
	if got := readLiveProfilePrefs(raw, "p2"); !reflect.DeepEqual(got.Favorites, map[string]bool{"own": true}) {
		t.Errorf("p2 favorites = %v, want own", got.Favorites)
	}
}

func TestWriteLiveFavoriteSeedsFromLegacyAndPreservesKeys(t *testing.T) {
	// The fixture's legacy hiddenCategories is empty, which would let a dropped
	// carry-over pass; give it a value.
	var seeded map[string]any
	readLiveTVFixture(t, "preferences_v1.json", &seeded)
	seeded["hiddenCategories"] = map[string]any{"9": true}
	seededRaw, err := json.Marshal(seeded)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writeLiveFavorite(string(seededRaw), "p1", "sports", true)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)

	if got, want := livePath(t, doc, "profiles", "p1", "favoriteOrder"), []any{"m3u:news.hd", "sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.favoriteOrder = %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "profiles", "p1", "favorites"), map[string]any{"m3u:news.hd": true, "sports": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.favorites = %v, want %v", got, want)
	}
	// The profile section starts from the legacy keys, so hidden categories carry over.
	if got, want := livePath(t, doc, "profiles", "p1", "hiddenCategories"), map[string]any{"9": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.hiddenCategories = %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "hiddenCategories"), map[string]any{"9": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("top-level hiddenCategories changed: %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "favorites"), map[string]any{"m3u:news.hd": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("top-level favorites changed: %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "favoriteOrder"), []any{"m3u:news.hd"}; !reflect.DeepEqual(got, want) {
		t.Errorf("top-level favoriteOrder changed: %v, want %v", got, want)
	}
	if got := livePath(t, doc, "playback", "volume"); got != 0.5 {
		t.Errorf("playback.volume = %v, want 0.5", got)
	}

	var fixture map[string]any
	readLiveTVFixture(t, "preferences_v1.json", &fixture)
	if got, want := livePath(t, doc, "profiles", "p2"), livePath(t, fixture, "profiles", "p2"); !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p2 changed: %v, want %v", got, want)
	}
}

func TestWriteLiveFavoriteRemove(t *testing.T) {
	out, err := writeLiveFavorite(livePrefsFixture(t), "p2", "xtream:42", false)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)
	if got, want := livePath(t, doc, "profiles", "p2", "favorites"), map[string]any{}; !reflect.DeepEqual(got, want) {
		t.Errorf("favorites = %v, want empty", got)
	}
	if got, want := livePath(t, doc, "profiles", "p2", "favoriteOrder"), []any{}; !reflect.DeepEqual(got, want) {
		t.Errorf("favoriteOrder = %v, want empty", got)
	}
	// Removal leaves the rest of the section alone.
	if got, want := livePath(t, doc, "profiles", "p2", "hiddenCategories"), map[string]any{"7": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("hiddenCategories = %v, want %v", got, want)
	}
}

func TestWriteLiveFavoriteMovesReaddedChannelToEnd(t *testing.T) {
	raw, err := writeLiveFavorite(livePrefsFixture(t), "p2", "second", true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writeLiveFavorite(raw, "p2", "xtream:42", true)
	if err != nil {
		t.Fatal(err)
	}
	got := readLiveProfilePrefs(out, "p2")
	if want := []string{"second", "xtream:42"}; !reflect.DeepEqual(got.FavoriteOrder, want) {
		t.Errorf("favoriteOrder = %v, want %v (no duplicates)", got.FavoriteOrder, want)
	}
}

func TestWriteLiveFavoriteLegacyWhenProfileEmpty(t *testing.T) {
	out, err := writeLiveFavorite(livePrefsFixture(t), "", "sports", true)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)
	if got, want := livePath(t, doc, "favoriteOrder"), []any{"m3u:news.hd", "sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("favoriteOrder = %v, want %v", got, want)
	}
	if got := livePath(t, doc, "profiles", "p2", "favoriteOrder"); !reflect.DeepEqual(got, []any{"xtream:42"}) {
		t.Errorf("profiles.p2 changed: %v", got)
	}
}

func TestWriteLiveFavoriteEmptyRaw(t *testing.T) {
	out, err := writeLiveFavorite("", "p1", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)
	if got, want := livePath(t, doc, "profiles", "p1", "favorites"), map[string]any{"a": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.favorites = %v, want %v", got, want)
	}
}

func TestWriteLiveFavoriteRejectsNonObject(t *testing.T) {
	if out, err := writeLiveFavorite("[1]", "p1", "a", true); err == nil {
		t.Errorf("want an error, got %q", out)
	}
}

func TestWriteLiveFavoriteTreatsNullAsMissing(t *testing.T) {
	// A null document is an empty one.
	for _, profileID := range []string{"p1", ""} {
		out, err := writeLiveFavorite("null", profileID, "a", true)
		if err != nil {
			t.Fatalf("profile %q: %v", profileID, err)
		}
		if got := readLiveProfilePrefs(out, profileID); !reflect.DeepEqual(got.Favorites, map[string]bool{"a": true}) {
			t.Errorf("profile %q: favorites = %v, want a", profileID, got.Favorites)
		}
	}

	// A null profile entry seeds from the legacy keys, like a missing one.
	raw := `{"favorites":{"legacy":true},"favoriteOrder":["legacy"],"hiddenCategories":{"3":true},"profiles":{"p1":null}}`
	out, err := writeLiveFavorite(raw, "p1", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)
	if got, want := livePath(t, doc, "profiles", "p1", "favoriteOrder"), []any{"legacy", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.favoriteOrder = %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "profiles", "p1", "hiddenCategories"), map[string]any{"3": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.hiddenCategories = %v, want %v", got, want)
	}
}

func TestWriteLiveFavoritePreservesNonObjectSiblings(t *testing.T) {
	raw := `{"profiles":{"p2":{"favorites":{"x":true}},"p3":"x","p4":null,"p5":[1,2]}}`
	out, err := writeLiveFavorite(raw, "p1", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeLivePrefs(t, out)
	for key, want := range map[string]any{"p3": "x", "p4": nil, "p5": []any{float64(1), float64(2)}} {
		if got := livePath(t, doc, "profiles", key); !reflect.DeepEqual(got, want) {
			t.Errorf("profiles.%s = %v, want %v", key, got, want)
		}
	}
	if got, want := livePath(t, doc, "profiles", "p2", "favorites"), map[string]any{"x": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p2.favorites = %v, want %v", got, want)
	}
	if got, want := livePath(t, doc, "profiles", "p1", "favorites"), map[string]any{"a": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("profiles.p1.favorites = %v, want %v", got, want)
	}
}

func TestWriteLiveFavoriteRejectsNonObjectProfiles(t *testing.T) {
	for _, raw := range []string{
		`{"profiles":5}`,
		`{"profiles":"x"}`,
		`{"profiles":[1]}`,
		`{"profiles":{"p1":"x"}}`, // the target entry itself
	} {
		if out, err := writeLiveFavorite(raw, "p1", "a", true); err == nil {
			t.Errorf("raw %s: want an error, got %q", raw, out)
		}
	}
	// With no profile in play, "profiles" is just another key to keep.
	out, err := writeLiveFavorite(`{"profiles":5}`, "", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := livePath(t, decodeLivePrefs(t, out), "profiles"); got != float64(5) {
		t.Errorf("profiles = %v, want 5", got)
	}
}

func newPrefsLiveTV(prefs LiveTVPrefsStore) *LiveTV {
	l, _ := newTestLiveTV(&fakeLiveTVRoutes{})
	l.prefs = prefs
	return l
}

func TestSetFavoriteConcurrentWritesBothPersist(t *testing.T) {
	store := &fakeLiveTVPrefs{setDelay: 5 * time.Millisecond}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: livePrefsFixture(t)})
	l := newPrefsLiveTV(store)

	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.SetFavorite(context.Background(), 1, 10, "p2", id, true); err != nil {
				t.Errorf("SetFavorite(%s): %v", id, err)
			}
		}()
	}
	wg.Wait()

	got := readLiveProfilePrefs(store.stored(1, 10)[liveTVPrefsKey], "p2")
	for _, id := range []string{"a", "b"} {
		if !got.Favorites[id] {
			t.Errorf("favorite %q lost; favorites = %v", id, got.Favorites)
		}
	}
	if !got.Favorites["xtream:42"] {
		t.Errorf("existing favorite lost; favorites = %v", got.Favorites)
	}
}

func TestSetFavoritePreservesOtherSettingKeys(t *testing.T) {
	store := &fakeLiveTVPrefs{}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: livePrefsFixture(t), "other_key": "x"})
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); err != nil {
		t.Fatal(err)
	}
	stored := store.stored(1, 10)
	if stored["other_key"] != "x" {
		t.Errorf("other_key = %q, want it preserved", stored["other_key"])
	}
	if got := readLiveProfilePrefs(stored[liveTVPrefsKey], "p1"); !got.Favorites["sports"] {
		t.Errorf("sports not stored: %+v", got)
	}
}

func TestSetFavoriteIsolatesAccountsAndInstallations(t *testing.T) {
	store := &fakeLiveTVPrefs{}
	store.seed(2, 10, map[string]string{liveTVPrefsKey: livePrefsFixture(t)})
	store.seed(1, 11, map[string]string{liveTVPrefsKey: livePrefsFixture(t)})
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][2]int{{2, 10}, {1, 11}} {
		if got := readLiveProfilePrefs(store.stored(key[0], key[1])[liveTVPrefsKey], "p1"); got.Favorites["sports"] {
			t.Errorf("account %v was written", key)
		}
	}
	if got := readLiveProfilePrefs(store.stored(1, 10)[liveTVPrefsKey], "p1"); !got.Favorites["sports"] {
		t.Errorf("account 1/10 not written: %+v", got)
	}
}

func TestSetFavoriteWriteFailureReturnsError(t *testing.T) {
	boom := errors.New("settings store down")
	store := &fakeLiveTVPrefs{setErr: boom}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: livePrefsFixture(t)})
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestSetFavoriteReadFailureWritesNothing(t *testing.T) {
	boom := errors.New("settings store down")
	store := &fakeLiveTVPrefs{getErr: boom}
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if stored := store.stored(1, 10); stored != nil {
		t.Errorf("store written after a failed read: %v", stored)
	}
}

func TestSetFavoriteCorruptPreferencesWritesNothing(t *testing.T) {
	store := &fakeLiveTVPrefs{}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: "[1]", "other_key": "x"})
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); err == nil {
		t.Fatal("want an error")
	}
	if got := store.stored(1, 10); got[liveTVPrefsKey] != "[1]" || got["other_key"] != "x" {
		t.Errorf("store changed after a failed write: %v", got)
	}
}

func TestSetFavoriteNonObjectProfilesWritesNothing(t *testing.T) {
	const raw = `{"profiles":5}`
	store := &fakeLiveTVPrefs{}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: raw, "other_key": "x"})
	l := newPrefsLiveTV(store)

	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); err == nil {
		t.Fatal("want an error")
	}
	if got := store.stored(1, 10); got[liveTVPrefsKey] != raw || got["other_key"] != "x" {
		t.Errorf("store changed after a failed write: %v", got)
	}
}

func TestSetFavoriteNullPreferencesDoNotPanic(t *testing.T) {
	for _, raw := range []string{"null", `{"profiles":{"p1":null}}`, `{"profiles":null}`} {
		store := &fakeLiveTVPrefs{}
		store.seed(1, 10, map[string]string{liveTVPrefsKey: raw})
		l := newPrefsLiveTV(store)

		if err := l.SetFavorite(context.Background(), 1, 10, "p1", "sports", true); err != nil {
			t.Errorf("raw %s: %v", raw, err)
			continue
		}
		if got := readLiveProfilePrefs(store.stored(1, 10)[liveTVPrefsKey], "p1"); !got.Favorites["sports"] {
			t.Errorf("raw %s: sports not stored: %+v", raw, got)
		}
	}
}

func TestSetFavoriteWithoutStore(t *testing.T) {
	l := newPrefsLiveTV(nil)
	if err := l.SetFavorite(context.Background(), 1, 10, "p1", "a", true); err == nil {
		t.Error("SetFavorite: want an error with no store")
	}
	if _, err := l.ProfilePrefs(context.Background(), 1, 10, "p1"); err == nil {
		t.Error("ProfilePrefs: want an error with no store")
	}
}

func TestProfilePrefsReadsStoredValue(t *testing.T) {
	store := &fakeLiveTVPrefs{}
	store.seed(1, 10, map[string]string{liveTVPrefsKey: livePrefsFixture(t)})
	l := newPrefsLiveTV(store)

	got, err := l.ProfilePrefs(context.Background(), 1, 10, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"xtream:42": true}; !reflect.DeepEqual(got.Favorites, want) {
		t.Errorf("favorites = %v, want %v", got.Favorites, want)
	}

	// An account with nothing stored has empty, non-nil prefs.
	empty, err := l.ProfilePrefs(context.Background(), 9, 10, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Favorites == nil || len(empty.Favorites) != 0 {
		t.Errorf("favorites = %v, want empty", empty.Favorites)
	}
}
