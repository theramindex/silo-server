package jellycompat

import (
	"context"
	"testing"
)

// Protects the new cross-app navigation boundary: hidden channels and external
// destinations must never become library/calendar shortcuts.
func TestLiveTVChannelLinksRespectProfileAndLocalPaths(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"profiles":{"p1":{"hiddenCategories":{"7":true}}}}`)
	env.routes.setResponse(liveTestInstall, liveTVPathChannels, 200, `[
		{"id":"hidden","name":"Hidden","groupId":"7","watchPath":"/dispatcharr#/watch/hidden"},
		{"id":"fx","name":"FX","groupId":"9","watchPath":"/dispatcharr#/watch/fx"},
		{"id":"external","name":"External","watchPath":"//outside.example/watch"},
		{"id":"traversal","name":"Traversal","watchPath":"/%2e%2e/admin"},
		{"id":"old","name":"Old provider"}
	]`)
	links, err := env.bridge.ChannelLinks(context.Background(), LiveTVCaller{UserID: 1, ProfileID: "p1"})
	if err != nil || len(links) != 1 || links[0].Name != "FX" {
		t.Fatal(links, err)
	}
	if links[0].URL != "/api/v2/plugin-content/plugins/5/dispatcharr#/watch/fx" {
		t.Fatal("unexpected launch target", links[0].URL)
	}
}
