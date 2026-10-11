// theramindex fork: tests for Live TV channel DTO mapping.

package jellycompat

import (
	"testing"
)

func TestMapLiveChannel(t *testing.T) {
	withLogo := liveChannel{
		liveTVChannel:  liveTVChannel{ID: "m3u:news.hd", Number: "101", Name: "News HD", LogoURL: "http://dispatcharr.example:9191/logos/news.png", GroupID: "7", Group: "News"},
		InstallationID: 5,
		EncodedID:      encodeLiveChannelID(5, "m3u:news.hd"),
	}
	prefs := liveProfilePrefs{Favorites: map[string]bool{"m3u:news.hd": true}}

	dto := mapLiveChannel(withLogo, "server-1", prefs)
	if dto.ID != withLogo.EncodedID || dto.ServerID != "server-1" {
		t.Errorf("Id/ServerId = %q/%q", dto.ID, dto.ServerID)
	}
	if dto.Type != "TvChannel" || dto.ChannelType != "TV" || dto.MediaType != "Video" || dto.LocationType != "Remote" || dto.PlayAccess != "Full" {
		t.Errorf("type fields = %+v", dto)
	}
	if dto.Number != "101" || dto.ChannelNumber != "101" {
		t.Errorf("number = %q/%q, want 101", dto.Number, dto.ChannelNumber)
	}
	if dto.SortName != "news hd" || dto.ParentID != liveTVViewID {
		t.Errorf("SortName/ParentId = %q/%q", dto.SortName, dto.ParentID)
	}
	if tag := dto.ImageTags["Primary"]; len(tag) != 16 || tag != liveLogoTag(withLogo.LogoURL) {
		t.Errorf("Primary tag = %q, want 16 hex chars of the logo hash", tag)
	}
	if dto.UserData == nil || !dto.UserData.IsFavorite || dto.UserData.Key != dto.ID || dto.UserData.ItemID != dto.ID {
		t.Errorf("UserData = %+v", dto.UserData)
	}
	if len(dto.Genres) != 1 || dto.Genres[0] != "News" {
		t.Errorf("Genres = %v, want [News]", dto.Genres)
	}

	noLogo := withLogo
	noLogo.LogoURL = ""
	noLogo.Group = ""
	dto = mapLiveChannel(noLogo, "server-1", liveProfilePrefs{})
	if len(dto.ImageTags) != 0 {
		t.Errorf("ImageTags = %v, want none without a logo", dto.ImageTags)
	}
	if dto.UserData.IsFavorite {
		t.Error("IsFavorite = true with empty prefs")
	}
	if dto.Genres == nil || dto.BackdropImageTags == nil {
		t.Error("Genres and BackdropImageTags must encode as [] rather than null")
	}
}

func TestLiveTVViewIDIsNotAChannelID(t *testing.T) {
	if isLiveChannelID(liveTVViewID) {
		t.Fatal("isLiveChannelID accepted the Live TV view id")
	}
	if !isLiveTVViewID(liveTVViewID) || !isLiveTVViewID("f1000000-0000-0000-0000-000000000001") {
		t.Error("isLiveTVViewID rejected the compact or dashed view id")
	}
	if isLiveTVViewID(encodeLiveChannelID(5, "m3u:news.hd")) || isLiveTVViewID("") {
		t.Error("isLiveTVViewID accepted a channel id or an empty string")
	}
}
