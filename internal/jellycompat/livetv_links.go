package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// LiveTVChannelLink names a channel and its plugin-owned playback page. It
// contains no upstream media URL or playback session credential.
type LiveTVChannelLink struct{ ID, Name, Group, Provider, URL string }

// ChannelLinks applies the same profile selection as the guide. Providers opt
// in by supplying a local watchPath; old providers keep working without links.
func (l *LiveTV) ChannelLinks(ctx context.Context, caller LiveTVCaller) ([]LiveTVChannelLink, error) {
	out := []LiveTVChannelLink{}
	prefs := map[int]liveProfilePrefs{}
	providers := map[int]string{}
	for _, ch := range l.Lineup(ctx, caller) {
		p, ok := prefs[ch.InstallationID]
		if !ok {
			var err error
			p, err = l.ProfilePrefs(ctx, caller.UserID, ch.InstallationID, caller.ProfileID)
			if err != nil {
				return nil, err
			}
			p.Profiles = l.knownProfiles(ctx, caller, ch.InstallationID, p.Profiles)
			prefs[ch.InstallationID] = p
		}
		if liveChannelHidden(ch, p) {
			continue
		}
		u, err := url.Parse(ch.WatchPath)
		if err != nil || !strings.HasPrefix(ch.WatchPath, "/") || strings.HasPrefix(ch.WatchPath, "//") || u.Host != "" || u.Scheme != "" || strings.ContainsAny(u.Path, "\\\r\n") || strings.Contains(u.Path, "..") {
			continue
		}
		provider, ok := providers[ch.InstallationID]
		if !ok {
			info, err := l.Info(ctx, caller, ch.InstallationID)
			if err != nil {
				return nil, err
			}
			provider = info.Name
			providers[ch.InstallationID] = provider
		}
		out = append(out, LiveTVChannelLink{ID: ch.EncodedID, Name: ch.Name, Group: ch.Group, Provider: provider, URL: fmt.Sprintf("%s/plugins/%d%s", plugins.ContentPrefix, ch.InstallationID, ch.WatchPath)})
	}
	return out, nil
}
