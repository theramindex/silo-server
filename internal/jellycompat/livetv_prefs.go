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

// liveProfilePrefs is one profile's Live TV preferences.
type liveProfilePrefs struct {
	Favorites     map[string]bool
	FavoriteOrder []string
	Hidden        map[string]bool
}

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
		var profiles map[string]map[string]json.RawMessage
		_ = json.Unmarshal(top["profiles"], &profiles)
		if p, ok := profiles[profileID]; ok {
			section = p
		}
	}
	return readLiveProfilePrefsSection(section)
}

// writeLiveFavorite returns the document with channelID added to or removed from
// a profile's favorites. Every other key is kept as it was. A profile with no
// entry yet starts from the legacy top-level keys, so its first write does not
// drop what it was already seeing. Adding moves the channel to the end of
// favoriteOrder.
func writeLiveFavorite(raw, profileID, channelID string, favorite bool) (string, error) {
	top := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			return "", fmt.Errorf("livetv: preferences are not a JSON object: %w", err)
		}
	}
	section := top
	var profiles map[string]map[string]json.RawMessage
	if profileID != "" {
		_ = json.Unmarshal(top["profiles"], &profiles)
		if profiles == nil {
			profiles = map[string]map[string]json.RawMessage{}
		}
		p, ok := profiles[profileID]
		if !ok {
			p = map[string]json.RawMessage{}
			for _, k := range []string{"favorites", "favoriteOrder", "hiddenCategories"} {
				if v, has := top[k]; has {
					p[k] = v
				}
			}
		}
		section = p
		profiles[profileID] = p
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
	return mu.(*sync.Mutex)
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
	next, err := writeLiveFavorite(values[liveTVPrefsKey], profileID, channelID, favorite)
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
