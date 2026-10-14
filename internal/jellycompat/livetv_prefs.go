// theramindex fork: reads and writes per-profile Live TV favorites in plugin settings.

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// liveTVPrefsKey is the plugin setting that holds the Live TV preferences
// document, the same one the web app writes.
const liveTVPrefsKey = "preferences"

// liveProfilePrefs is one profile's Live TV preferences. Watched holds when
// each channel was last played, which the web app keeps for the whole account
// rather than per profile.
type liveProfilePrefs struct {
	Favorites     map[string]bool
	FavoriteOrder []string
	Hidden        map[string]bool
	Watched       map[string]liveWatch
}

// liveWatch is one continueWatching entry: when a channel was last played, in
// unix seconds, and how often.
type liveWatch struct {
	PlayedAt int64 `json:"playedAt"`
	Plays    int   `json:"plays"`
}

// liveTVRecentChannels is how many channels the web app keeps in
// recentChannels, and liveTVAutoFavoritePlays the play count at which it
// suggests a channel as a favorite.
const (
	liveTVRecentChannels    = 24
	liveTVAutoFavoritePlays = 3
)

// readLiveProfilePrefsSection decodes the preference keys of one section, which
// is either the document itself (legacy, no profile) or one entry of "profiles".
// The maps are never nil.
func readLiveProfilePrefsSection(section map[string]json.RawMessage) liveProfilePrefs {
	out := liveProfilePrefs{Favorites: map[string]bool{}, Hidden: map[string]bool{}}
	_ = json.Unmarshal(section["favorites"], &out.Favorites)
	_ = json.Unmarshal(section["favoriteOrder"], &out.FavoriteOrder)
	_ = json.Unmarshal(section["hiddenCategories"], &out.Hidden)
	if out.Favorites == nil {
		out.Favorites = map[string]bool{}
	}
	if out.Hidden == nil {
		out.Hidden = map[string]bool{}
	}
	return out
}

// readLiveProfilePrefs returns a profile's preferences from the stored
// document. A profile without its own entry, and an empty profileID, read the
// legacy top-level keys. Malformed input reads as empty preferences.
func readLiveProfilePrefs(raw, profileID string) liveProfilePrefs {
	top := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(raw), &top)
	section := top
	if profileID != "" {
		var profiles map[string]json.RawMessage
		_ = json.Unmarshal(top["profiles"], &profiles)
		var p map[string]json.RawMessage
		// A null or malformed entry is the same as a missing one.
		if json.Unmarshal(profiles[profileID], &p) == nil && p != nil {
			section = p
		}
	}
	out := readLiveProfilePrefsSection(section)
	out.Watched = readLiveWatched(top)
	return out
}

// readLiveWatched decodes the account's continueWatching map. An entry that is
// not an object, or has no play time, is left out.
func readLiveWatched(top map[string]json.RawMessage) map[string]liveWatch {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(top["continueWatching"], &raw)
	watched := make(map[string]liveWatch, len(raw))
	for id, v := range raw {
		var w liveWatch
		if json.Unmarshal(v, &w) == nil && w.PlayedAt > 0 {
			watched[id] = w
		}
	}
	return watched
}

// writeLiveWatch returns the document with a play of channelID recorded the
// way the web app records one: the channel moves to the front of
// recentChannels, its continueWatching entry gets the play time and one more
// play, and from the third play a channel the profile has not favorited is
// suggested in autoFavorites. Every other key is kept as it was.
func writeLiveWatch(raw, profileID, channelID, channelName string, now int64) (string, error) {
	top := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			return "", fmt.Errorf("livetv: preferences are not a JSON object: %w", err)
		}
		if top == nil {
			top = map[string]json.RawMessage{}
		}
	}

	var recent []string
	_ = json.Unmarshal(top["recentChannels"], &recent)
	next := []string{channelID}
	for _, id := range recent {
		if id != channelID && len(next) < liveTVRecentChannels {
			next = append(next, id)
		}
	}

	var watching map[string]json.RawMessage
	if v, has := top["continueWatching"]; has {
		if err := json.Unmarshal(v, &watching); err != nil {
			return "", fmt.Errorf("livetv: preferences continueWatching is not a JSON object: %w", err)
		}
	}
	if watching == nil {
		watching = map[string]json.RawMessage{}
	}
	var old liveWatch
	_ = json.Unmarshal(watching[channelID], &old)
	plays := old.Plays + 1
	if channelName == "" {
		channelName = channelID
	}
	entry, err := json.Marshal(struct {
		ItemKind string `json:"itemKind"`
		ItemID   string `json:"itemId"`
		ItemName string `json:"itemName"`
		PlayedAt int64  `json:"playedAt"`
		Plays    int    `json:"plays"`
	}{"channel", channelID, channelName, now, plays})
	if err != nil {
		return "", err
	}
	watching[channelID] = entry

	if plays >= liveTVAutoFavoritePlays && !readLiveProfilePrefs(raw, profileID).Favorites[channelID] {
		var auto map[string]json.RawMessage
		if v, has := top["autoFavorites"]; has {
			if err := json.Unmarshal(v, &auto); err != nil {
				return "", fmt.Errorf("livetv: preferences autoFavorites is not a JSON object: %w", err)
			}
		}
		if auto == nil {
			auto = map[string]json.RawMessage{}
		}
		auto[channelID] = json.RawMessage("true")
		if top["autoFavorites"], err = json.Marshal(auto); err != nil {
			return "", err
		}
	}

	if top["recentChannels"], err = json.Marshal(next); err != nil {
		return "", err
	}
	if top["continueWatching"], err = json.Marshal(watching); err != nil {
		return "", err
	}
	out, err := json.Marshal(top)
	return string(out), err
}

// writeLiveFavorite returns the document with channelID added to or removed from
// a profile's favorites. Every other key is kept as it was. A profile with no
// entry yet starts from the legacy top-level keys, so its first write does not
// drop what it was already seeing. Adding moves the channel to the end of
// favoriteOrder. A null document is an empty one, and a null profile entry is a
// missing one. A "profiles" value or target profile entry that is not an object
// is an error rather than something to overwrite.
func writeLiveFavorite(raw, profileID, channelID string, favorite bool) (string, error) {
	top := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			return "", fmt.Errorf("livetv: preferences are not a JSON object: %w", err)
		}
		if top == nil {
			top = map[string]json.RawMessage{}
		}
	}
	section := top
	var profiles map[string]json.RawMessage
	if profileID != "" {
		if v, has := top["profiles"]; has {
			if err := json.Unmarshal(v, &profiles); err != nil {
				return "", fmt.Errorf("livetv: preferences profiles are not a JSON object: %w", err)
			}
		}
		if profiles == nil {
			profiles = map[string]json.RawMessage{}
		}
		var p map[string]json.RawMessage
		if v, has := profiles[profileID]; has {
			if err := json.Unmarshal(v, &p); err != nil {
				return "", fmt.Errorf("livetv: preferences for profile %q are not a JSON object: %w", profileID, err)
			}
		}
		if p == nil {
			p = map[string]json.RawMessage{}
			for _, k := range []string{"favorites", "favoriteOrder", "hiddenCategories"} {
				if v, has := top[k]; has {
					p[k] = v
				}
			}
		}
		section = p
	}

	cur := readLiveProfilePrefsSection(section)
	order := make([]string, 0, len(cur.FavoriteOrder)+1)
	for _, id := range cur.FavoriteOrder {
		if id != channelID {
			order = append(order, id)
		}
	}
	if favorite {
		cur.Favorites[channelID] = true
		order = append(order, channelID)
	} else {
		delete(cur.Favorites, channelID)
	}
	fav, _ := json.Marshal(cur.Favorites)
	ord, _ := json.Marshal(order)
	section["favorites"], section["favoriteOrder"] = fav, ord

	if profileID != "" {
		entry, err := json.Marshal(section)
		if err != nil {
			return "", err
		}
		profiles[profileID] = entry
		b, err := json.Marshal(profiles)
		if err != nil {
			return "", err
		}
		top["profiles"] = b
	}
	out, err := json.Marshal(top)
	return string(out), err
}

var errLiveTVPrefsUnavailable = errors.New("livetv: preferences store unavailable")

// ProfilePrefs returns a profile's Live TV preferences from one installation's
// settings for the account.
func (l *LiveTV) ProfilePrefs(ctx context.Context, userID, installationID int, profileID string) (liveProfilePrefs, error) {
	if l.prefs == nil {
		return liveProfilePrefs{}, errLiveTVPrefsUnavailable
	}
	values, err := l.prefs.Get(ctx, userID, installationID)
	if err != nil {
		return liveProfilePrefs{}, err
	}
	return readLiveProfilePrefs(values[liveTVPrefsKey], profileID), nil
}

// prefsLock returns the mutex that serializes writes to one account's settings
// for one installation.
func (l *LiveTV) prefsLock(userID, installationID int) *sync.Mutex {
	mu, _ := l.prefLocks.LoadOrStore([2]int{userID, installationID}, &sync.Mutex{})
	lock, _ := mu.(*sync.Mutex)
	return lock
}

// SetFavorite adds or removes a channel from a profile's favorites in the
// account's plugin settings for the installation.
//
// The store replaces all of an account's values on Set, so the other settings
// are written back unchanged next to the new preferences document. Writes
// through this method are serialized per account and installation. The web app
// writes through the HTTP API without this lock, so a write from there can still
// race with one from here; the window is one request.
func (l *LiveTV) SetFavorite(ctx context.Context, userID, installationID int, profileID, channelID string, favorite bool) error {
	return l.updatePrefs(ctx, userID, installationID, func(raw string) (string, error) {
		return writeLiveFavorite(raw, profileID, channelID, favorite)
	})
}

// RecordWatch records a play of a channel in the account's preferences, as the
// web app does when it starts a channel. Writes are serialized as SetFavorite's
// are.
func (l *LiveTV) RecordWatch(ctx context.Context, userID, installationID int, profileID, channelID, channelName string) error {
	now := l.now().Unix()
	return l.updatePrefs(ctx, userID, installationID, func(raw string) (string, error) {
		return writeLiveWatch(raw, profileID, channelID, channelName, now)
	})
}

// updatePrefs rewrites the account's preferences document for one installation
// with update, under the account's write lock.
func (l *LiveTV) updatePrefs(ctx context.Context, userID, installationID int, update func(raw string) (string, error)) error {
	if l.prefs == nil {
		return errLiveTVPrefsUnavailable
	}
	mu := l.prefsLock(userID, installationID)
	mu.Lock()
	defer mu.Unlock()

	values, err := l.prefs.Get(ctx, userID, installationID)
	if err != nil {
		return err
	}
	next, err := update(values[liveTVPrefsKey])
	if err != nil {
		return err
	}
	merged := make(map[string]string, len(values)+1)
	for k, v := range values {
		merged[k] = v
	}
	merged[liveTVPrefsKey] = next
	return l.prefs.Set(ctx, userID, installationID, merged)
}
