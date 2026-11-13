package api

import (
	"context"
	"os"

	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func newPremiereService(deps Dependencies) *notifications.PremiereService {
	if deps.DB == nil || deps.PluginService == nil || deps.PluginUserConfig == nil || !jellycompat.LiveTVEnabledFromEnv(os.Getenv) {
		return nil
	}
	bridge := jellycompat.NewLiveTV(jellycompat.NewPluginLiveTVRoutes(deps.PluginService, plugins.NewInstallationStore(deps.DB)), deps.PluginUserConfig, nil)
	return notifications.NewPremiereService(deps.DB, deps.Notifications, func(ctx context.Context, user int, profile string) ([]notifications.PremiereChannel, error) {
		links, err := bridge.ChannelLinks(ctx, jellycompat.LiveTVCaller{UserID: user, ProfileID: profile})
		if err != nil {
			return nil, err
		}
		out := make([]notifications.PremiereChannel, 0, len(links))
		for _, link := range links {
			out = append(out, notifications.PremiereChannel{ID: link.ID, Name: link.Name, Group: link.Group, Provider: link.Provider, URL: link.URL})
		}
		return out, nil
	})
}
