// theramindex fork: tests for the channel folder views.

package jellycompat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The channels fixture files news.hd in US TV / News, xtream:42 in US TV / News
// and International TV / UK / News, and sports.one in US Sports.

type liveFolderTestItem struct {
	ID             string           `json:"Id"`
	Name           string           `json:"Name"`
	Type           string           `json:"Type"`
	CollectionType string           `json:"CollectionType"`
	IsFolder       bool             `json:"IsFolder"`
	ParentID       string           `json:"ParentId"`
	ChildCount     int              `json:"ChildCount"`
	UserData       *itemUserDataDTO `json:"UserData"`
}

type liveFolderTestResult struct {
	Items            []liveFolderTestItem `json:"Items"`
	TotalRecordCount int                  `json:"TotalRecordCount"`
}

func liveFolderIDFor(path string) string { return encodeLiveFolderID(liveTestInstall, path) }

func (e *liveTestEnv) folderItems(t *testing.T, query string) liveFolderTestResult {
	t.Helper()
	rec := serveLive(t, e.router, http.MethodGet, "/Items?"+query, liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Items?%s = %d %s", query, rec.Code, rec.Body.String())
	}
	return decodeLiveJSON[liveFolderTestResult](t, rec)
}

func liveFolderNames(res liveFolderTestResult) string {
	names := make([]string, len(res.Items))
	for i, item := range res.Items {
		names[i] = item.Type + ":" + item.Name
	}
	return strings.Join(names, "|")
}

func TestLiveTVFolderViewListed(t *testing.T) {
	env := newLiveTestEnv(t, true)
	rec := serveLive(t, env.router, http.MethodGet, "/UserViews", liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("/UserViews = %d %s", rec.Code, rec.Body.String())
	}
	var views liveFolderTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views.Items) != 2 {
		t.Fatalf("views = %+v, want Live TV and Channels", views.Items)
	}
	view := views.Items[1]
	if view.ID != liveFolderIDFor("") || view.Name != "Channels" || view.Type != "UserView" || view.CollectionType != "folders" || !view.IsFolder || view.ChildCount != 3 {
		t.Fatalf("folder view = %+v", view)
	}
}

func TestLiveTVFolderBrowsing(t *testing.T) {
	env := newLiveTestEnv(t, true)

	root := env.folderItems(t, "ParentId="+liveFolderIDFor("")+"&SortBy=SortName")
	if got := liveFolderNames(root); got != "Folder:International TV|Folder:US Sports|Folder:US TV" || root.TotalRecordCount != 3 {
		t.Fatalf("root = %s (%d)", got, root.TotalRecordCount)
	}
	usTV := root.Items[2]
	if usTV.ID != liveFolderIDFor("US TV") || usTV.ParentID != liveFolderIDFor("") || usTV.ChildCount != 1 {
		t.Fatalf("US TV folder = %+v", usTV)
	}

	news := env.folderItems(t, "ParentId="+liveFolderIDFor("US TV / News"))
	if got := liveFolderNames(news); got != "TvChannel:News HD|TvChannel:News Tonight" {
		t.Fatalf("US TV / News = %s", got)
	}
	if news.Items[0].ID != liveChannelIDFor("m3u:news.hd") {
		t.Fatalf("news channel id = %s", news.Items[0].ID)
	}

	sports := env.folderItems(t, "ParentId="+liveFolderIDFor("US Sports"))
	if got := liveFolderNames(sports); got != "TvChannel:Sports One" {
		t.Fatalf("US Sports = %s", got)
	}
}

func TestLiveTVFolderItem(t *testing.T) {
	env := newLiveTestEnv(t, true)

	rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveFolderIDFor("International TV / UK"), liveTokenP1)
	if rec.Code != http.StatusOK {
		t.Fatalf("folder item = %d %s", rec.Code, rec.Body.String())
	}
	folder := decodeLiveJSON[liveFolderTestItem](t, rec)
	if folder.Name != "UK" || folder.Type != "Folder" || !folder.IsFolder || folder.ParentID != liveFolderIDFor("International TV") || folder.ChildCount != 1 {
		t.Fatalf("folder = %+v", folder)
	}

	rec = serveLive(t, env.router, http.MethodGet, "/Items/"+liveFolderIDFor(""), liveTokenP1)
	if view := decodeLiveJSON[liveFolderTestItem](t, rec); rec.Code != http.StatusOK || view.Name != "Channels" || view.CollectionType != "folders" {
		t.Fatalf("view item = %d %+v", rec.Code, view)
	}

	if rec := serveLive(t, env.router, http.MethodGet, "/Items/"+liveFolderIDFor("Nowhere"), liveTokenP1); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown folder = %d, want 404", rec.Code)
	}
	if res := env.folderItems(t, "ParentId="+liveFolderIDFor("Nowhere")); len(res.Items) != 0 {
		t.Fatalf("unknown folder children = %s", liveFolderNames(res))
	}
}

// Recursive lists every channel below a folder once, in lineup order.
func TestLiveTVFolderRecursive(t *testing.T) {
	env := newLiveTestEnv(t, true)
	res := env.folderItems(t, "ParentId="+liveFolderIDFor("")+"&Recursive=true")
	if got := liveFolderNames(res); got != "TvChannel:News HD|TvChannel:News Tonight|TvChannel:Sports One" {
		t.Fatalf("recursive = %s", got)
	}
	if res := env.folderItems(t, "ParentId="+liveFolderIDFor("")+"&Recursive=true&IncludeItemTypes=Movie"); len(res.Items) != 0 {
		t.Fatalf("recursive movies = %s", liveFolderNames(res))
	}
}

// A hidden group's channels leave the tree, and folders left empty go too.
func TestLiveTVFolderHidesHiddenGroups(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"profiles":{"p1":{"hiddenCategories":{"8":true}}}}`)
	res := env.folderItems(t, "ParentId="+liveFolderIDFor(""))
	if got := liveFolderNames(res); got != "Folder:International TV|Folder:US TV" {
		t.Fatalf("root with Sports hidden = %s", got)
	}
}

func TestLiveTVFolderFavoritesPagingAndLetters(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.seedPrefs(`{"profiles":{"p1":{"favorites":{"xtream:42":true}}}}`)
	news := liveFolderIDFor("US TV / News")

	favorites := env.folderItems(t, "ParentId="+news+"&Filters=IsFavorite")
	if got := liveFolderNames(favorites); got != "TvChannel:News Tonight" || !favorites.Items[0].UserData.IsFavorite {
		t.Fatalf("favorites = %s", got)
	}

	page := env.folderItems(t, "ParentId="+liveFolderIDFor("")+"&StartIndex=1&Limit=1")
	if got := liveFolderNames(page); got != "Folder:US Sports" || page.TotalRecordCount != 3 {
		t.Fatalf("page = %s (%d)", got, page.TotalRecordCount)
	}

	letters := env.folderItems(t, "ParentId="+liveFolderIDFor("")+"&NameStartsWith=u")
	if got := liveFolderNames(letters); got != "Folder:US Sports|Folder:US TV" {
		t.Fatalf("NameStartsWith = %s", got)
	}
}

func TestLiveTVFolderLatestIsEmpty(t *testing.T) {
	env := newLiveTestEnv(t, true)
	for _, parent := range []string{liveFolderIDFor(""), liveTVViewID} {
		rec := serveLive(t, env.router, http.MethodGet, "/Items/Latest?ParentId="+parent, liveTokenP1)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("latest for %s = %d %s", parent, rec.Code, rec.Body.String())
		}
	}
}

// A provider that sends no folders gets no folder view.
func TestLiveTVFolderViewNeedsFolders(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.routes.setResponse(liveTestInstall, liveTVPathChannels, http.StatusOK, `[{"id":"m3u:a","number":"1","name":"A","logoUrl":"","groupId":"1","group":"G"}]`)
	if types := liveViewTypes(t, env.router); strings.Join(types, ",") != "livetv" {
		t.Fatalf("views = %v, want only livetv", types)
	}
}

// With two providers sending folders, each view is named for its provider.
func TestLiveTVFolderViewNamesWithTwoProviders(t *testing.T) {
	env := newLiveTestEnv(t, true)
	const second = liveTestInstall + 1
	env.routes.providers = append(env.routes.providers, LiveTVProviderRef{InstallationID: second, PluginID: "silo.ramindex.xtream"})
	env.routes.setResponse(second, liveTVPathInfo, http.StatusOK, `{"contractVersion":1,"name":"XC","streamHosts":[]}`)
	env.routes.setResponse(second, liveTVPathChannels, http.StatusOK, `[{"id":"xtream:1","number":"1","name":"Flo 1","logoUrl":"","groupId":"3","group":"USA | Flo","folders":["USA | Flo"]}]`)

	rec := serveLive(t, env.router, http.MethodGet, "/UserViews", liveTokenP1)
	views := decodeLiveJSON[liveFolderTestResult](t, rec)
	var names []string
	for _, view := range views.Items {
		names = append(names, view.Name)
	}
	var info struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(liveFixtureRaw(t, "info.json")), &info); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, "|"), "Live TV|"+info.Name+" Channels|XC Channels"; got != want {
		t.Fatalf("views = %s, want %s", got, want)
	}

	res := env.folderItems(t, "ParentId="+encodeLiveFolderID(second, "USA | Flo"))
	if got := liveFolderNames(res); got != "TvChannel:Flo 1" {
		t.Fatalf("second provider folder = %s", got)
	}
}

// liveFolderPrefsLineup is a provider whose channels carry profile folders and
// pipe folders.
const liveFolderPrefsLineup = `[` +
	`{"id":"abc","number":"1","name":"WABC","logoUrl":"","groupId":"1","group":"US TV | Locals | ABC","folders":["US TV / NY / Locals / ABC","US TV / NJ / Locals / ABC"],` +
	`"profileIds":["ny","nj"],"profileFolders":[{"path":"US TV / NY","profileId":"ny"},{"path":"US TV / NY / Locals / ABC","profileId":"ny"},{"path":"US TV / NJ","profileId":"nj"},{"path":"US TV / NJ / Locals / ABC","profileId":"nj"}]},` +
	`{"id":"njn","number":"2","name":"NJ News","logoUrl":"","groupId":"2","group":"US TV | News","folders":["US TV / NJ / News"],` +
	`"profileIds":["nj"],"profileFolders":[{"path":"US TV / NJ","profileId":"nj"},{"path":"US TV / NJ / News","profileId":"nj"}]},` +
	`{"id":"flo","number":"3","name":"Flo 1","logoUrl":"","groupId":"3","group":"USA | Flo","folders":["USA | Flo"],"pipeFolders":["USA / Flo"]}` +
	`]`

// The admin's layout is the default; a user's channel profile selection hides
// the channels and folders of the other profiles, and groupCategoriesByPipe
// switches to the pipe folders.
func TestLiveTVFolderUserPreferences(t *testing.T) {
	env := newLiveTestEnv(t, true)
	env.routes.setResponse(liveTestInstall, liveTVPathChannels, http.StatusOK, liveFolderPrefsLineup)
	browse := func(path string) string {
		return liveFolderNames(env.folderItems(t, "ParentId="+liveFolderIDFor(path)))
	}

	if got := browse("US TV"); got != "Folder:NJ|Folder:NY" {
		t.Fatalf("admin layout US TV = %s", got)
	}
	if got := browse(""); got != "Folder:US TV|Folder:USA | Flo" {
		t.Fatalf("admin layout root = %s", got)
	}

	env.seedPrefs(`{"profileSelection":{"mode":"selected","profileIds":["ny"]},"groupCategoriesByPipe":true}`)
	if got := browse("US TV"); got != "Folder:NY" {
		t.Fatalf("NY only US TV = %s", got)
	}
	if got := browse("US TV / NY / Locals"); got != "Folder:ABC" {
		t.Fatalf("NY only locals = %s", got)
	}
	// The channel profile selection hides NJ News everywhere; Flo is in no
	// profile, so it is hidden too, as in the web app.
	rec := serveLive(t, env.router, http.MethodGet, "/LiveTv/Channels", liveTokenP1)
	if got := decodeLiveJSON[liveFolderTestResult](t, rec); liveFolderNames(got) != "TvChannel:WABC" {
		t.Fatalf("channels with NY selected = %s", liveFolderNames(got))
	}

	env.seedPrefs(`{"groupCategoriesByPipe":true}`)
	if got := browse("USA"); got != "Folder:Flo" {
		t.Fatalf("pipe groups USA = %s", got)
	}

	// A selection naming only profiles no channel is in shows every profile.
	env.seedPrefs(`{"profileSelection":{"mode":"selected","profileIds":["gone"]}}`)
	if got := browse("US TV"); got != "Folder:NJ|Folder:NY" {
		t.Fatalf("stale selection US TV = %s", got)
	}
}
