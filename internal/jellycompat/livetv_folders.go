// theramindex fork: browsable channel folders built from livetv/v1 channel folders.

package jellycompat

import (
	"cmp"
	"context"
	"encoding/binary"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Each provider whose channels carry folders gets a "folders" UserView. Its
// tree is the plugin web app's folder tree ("US TV / NY / Locals / ABC"), so a
// stock client browses the same levels: folders first, then the channels filed
// directly in the folder.

// Byte layout of a channel folder UUID:
//
//	[0]    EncodedIDLiveChannel (0xF1)
//	[1]    'F'
//	[2:6]  installation id, big-endian uint32
//	[6:8]  zero
//	[8:16] first 8 bytes of sha256(folder path); the view is the path ""
//
// Like channel ids, a folder id is matched against the current tree, so no id
// map has to survive a restart.
const liveFolderTag = 'F'

// liveFolderSeparator joins the levels of a contract folder path.
const liveFolderSeparator = " / "

// Jellyfin values for the folder views and folders.
const (
	liveFolderCollectionType = "folders"
	liveFolderItemType       = "Folder"
	liveFolderViewType       = "UserView"
	liveFolderViewName       = "Channels"
)

type liveFolderKey struct {
	InstallationID int
	Hash           [8]byte
}

func encodeLiveFolderID(installationID int, path string) string {
	var raw [16]byte
	raw[0] = byte(EncodedIDLiveChannel)
	raw[1] = liveFolderTag
	binary.BigEndian.PutUint32(raw[2:6], uint32(installationID))
	hash := liveChannelHash(path)
	copy(raw[8:], hash[:])
	return uuid.UUID(raw).String()
}

// decodeLiveFolderID accepts the dashed or 32-hex form. It checks syntax only.
func decodeLiveFolderID(raw string) (liveFolderKey, bool) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return liveFolderKey{}, false
	}
	if parsed[0] != byte(EncodedIDLiveChannel) || parsed[1] != liveFolderTag || parsed[6] != 0 || parsed[7] != 0 {
		return liveFolderKey{}, false
	}
	key := liveFolderKey{InstallationID: int(binary.BigEndian.Uint32(parsed[2:6]))}
	copy(key.Hash[:], parsed[8:])
	return key, true
}

func isLiveFolderID(raw string) bool {
	_, ok := decodeLiveFolderID(raw)
	return ok
}

// liveFolderTree is one installation's folders over the channels a session
// sees. The view is the path "".
type liveFolderTree struct {
	installationID int
	children       map[string][]string      // folder path -> child folder paths
	channels       map[string][]liveChannel // folder path -> channels filed in it
	byHash         map[[8]byte]string
}

// buildLiveFolderTree files channels, all of one installation, by their
// folders. It returns false when no channel has a folder.
func buildLiveFolderTree(installationID int, channels []liveChannel) (liveFolderTree, bool) {
	tree := liveFolderTree{
		installationID: installationID,
		children:       map[string][]string{},
		channels:       map[string][]liveChannel{},
		byHash:         map[[8]byte]string{liveChannelHash(""): ""},
	}
	for _, ch := range channels {
		for _, folder := range ch.Folders {
			path := liveFolderPath(folder)
			if path == "" {
				continue
			}
			if !slices.ContainsFunc(tree.channels[path], func(c liveChannel) bool { return c.ID == ch.ID }) {
				tree.channels[path] = append(tree.channels[path], ch)
			}
			tree.addFolder(path)
		}
	}
	return tree, len(tree.byHash) > 1
}

// liveFolderPath normalizes a contract folder path: levels are trimmed and
// empty ones dropped.
func liveFolderPath(folder string) string {
	var levels []string
	for _, level := range strings.Split(folder, liveFolderSeparator) {
		if level = strings.TrimSpace(level); level != "" {
			levels = append(levels, level)
		}
	}
	return strings.Join(levels, liveFolderSeparator)
}

// addFolder registers path and its ancestors.
func (t liveFolderTree) addFolder(path string) {
	for path != "" {
		hash := liveChannelHash(path)
		if _, seen := t.byHash[hash]; seen {
			return
		}
		t.byHash[hash] = path
		parent := liveFolderParent(path)
		t.children[parent] = append(t.children[parent], path)
		path = parent
	}
}

func liveFolderParent(path string) string {
	i := strings.LastIndex(path, liveFolderSeparator)
	if i < 0 {
		return ""
	}
	return path[:i]
}

func liveFolderName(path string) string {
	if i := strings.LastIndex(path, liveFolderSeparator); i >= 0 {
		return path[i+len(liveFolderSeparator):]
	}
	return path
}

func (t liveFolderTree) childCount(path string) int {
	return len(t.children[path]) + len(t.channels[path])
}

// descendants returns every channel in path or below it, once each, in
// lineup order.
func (t liveFolderTree) descendants(path string, lineupOrder map[string]int) []liveChannel {
	seen := map[string]bool{}
	var out []liveChannel
	var walk func(string)
	walk = func(p string) {
		for _, ch := range t.channels[p] {
			if !seen[ch.ID] {
				seen[ch.ID] = true
				out = append(out, ch)
			}
		}
		for _, child := range t.children[p] {
			walk(child)
		}
	}
	walk(path)
	slices.SortStableFunc(out, func(a, b liveChannel) int { return cmp.Compare(lineupOrder[a.ID], lineupOrder[b.ID]) })
	return out
}

// folderTrees builds the session's folder tree for every provider that sends
// folders, in provider order, or only for installation only when it is not 0.
func (h *liveTVHandlers) folderTrees(ctx context.Context, s *Session, only int) ([]liveFolderTree, map[int]liveProfilePrefs) {
	channels, prefs := h.visibleChannels(ctx, s)
	byInstall := map[int][]liveChannel{}
	var order []int
	for _, ch := range channels {
		if only != 0 && ch.InstallationID != only {
			continue
		}
		if _, ok := byInstall[ch.InstallationID]; !ok {
			order = append(order, ch.InstallationID)
		}
		byInstall[ch.InstallationID] = append(byInstall[ch.InstallationID], ch)
	}
	var trees []liveFolderTree
	for _, id := range order {
		if tree, ok := buildLiveFolderTree(id, byInstall[id]); ok {
			trees = append(trees, tree)
		}
	}
	return trees, prefs
}

// folderTree finds the tree and folder path a folder id names.
func (h *liveTVHandlers) folderTree(ctx context.Context, s *Session, rawID string) (liveFolderTree, string, map[int]liveProfilePrefs, bool) {
	key, ok := decodeLiveFolderID(rawID)
	if !ok {
		return liveFolderTree{}, "", nil, false
	}
	if key.InstallationID == 0 {
		return liveFolderTree{}, "", nil, false
	}
	trees, prefs := h.folderTrees(ctx, s, key.InstallationID)
	if len(trees) == 0 {
		return liveFolderTree{}, "", nil, false
	}
	path, found := trees[0].byHash[key.Hash]
	return trees[0], path, prefs, found
}

// folderViewNames names each provider's folder view: "Channels" alone, or
// "<provider> Channels" when more than one provider has folders.
func (h *liveTVHandlers) folderViewNames(ctx context.Context, trees []liveFolderTree) map[int]string {
	names := make(map[int]string, len(trees))
	for _, tree := range trees {
		name := liveFolderViewName
		if len(trees) > 1 {
			provider := "Live TV " + strconv.Itoa(tree.installationID)
			if info, err := h.bridge.Info(ctx, LiveTVCaller{}, tree.installationID); err == nil && strings.TrimSpace(info.Name) != "" {
				provider = strings.TrimSpace(info.Name)
			}
			name = provider + " " + liveFolderViewName
		}
		names[tree.installationID] = name
	}
	return names
}

// folderViews lists a UserView per provider with folders, for /UserViews.
func (h *liveTVHandlers) folderViews(ctx context.Context, s *Session) []baseItemDTO {
	trees, _ := h.folderTrees(ctx, s, 0)
	names := h.folderViewNames(ctx, trees)
	views := make([]baseItemDTO, 0, len(trees))
	for _, tree := range trees {
		views = append(views, h.folderDTO(tree, "", names[tree.installationID]))
	}
	return views
}

// folderDTO is the view for path "" and a Folder below it. viewName names the
// view.
func (h *liveTVHandlers) folderDTO(tree liveFolderTree, path, viewName string) baseItemDTO {
	id := encodeLiveFolderID(tree.installationID, path)
	dto := baseItemDTO{
		ID:                   id,
		ServerID:             h.serverID,
		Type:                 liveFolderItemType,
		Name:                 viewName,
		IsFolder:             true,
		ChildCount:           tree.childCount(path),
		DisplayPreferencesID: id,
		ImageTags:            map[string]string{},
		BackdropImageTags:    []string{},
		UserData:             &itemUserDataDTO{Key: id, ItemID: id},
	}
	if path == "" {
		dto.Type = liveFolderViewType
		dto.CollectionType = liveFolderCollectionType
	} else {
		dto.Name = liveFolderName(path)
		dto.ParentID = encodeLiveFolderID(tree.installationID, liveFolderParent(path))
	}
	dto.SortName = strings.ToLower(dto.Name)
	return dto
}

// writeFolder serves GET /Items/{id} for a folder id.
func (h *liveTVHandlers) writeFolder(w http.ResponseWriter, r *http.Request, s *Session, rawID string) {
	tree, path, _, ok := h.folderTree(r.Context(), s, rawID)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	name := ""
	if path == "" {
		trees, _ := h.folderTrees(r.Context(), s, 0)
		name = h.folderViewNames(r.Context(), trees)[tree.installationID]
	}
	writeJSON(w, http.StatusOK, h.folderDTO(tree, path, name))
}

type liveFolderQueryResult struct {
	Items            []any `json:"Items"`
	TotalRecordCount int   `json:"TotalRecordCount"`
	StartIndex       int   `json:"StartIndex"`
}

// writeFolderItems serves GET /Items?ParentId=<folder>. A plain listing is the
// folder's subfolders, then the channels filed in it. Recursive=true lists every
// channel below the folder instead. SortBy SortName orders channels by name
// rather than lineup order; folders are always by name.
func (h *liveTVHandlers) writeFolderItems(w http.ResponseWriter, r *http.Request, s *Session, rawID string) {
	q := newCaseInsensitiveQuery(r.URL.Query())
	startIndex, _ := strconv.Atoi(q.Get("StartIndex"))
	startIndex = max(startIndex, 0)
	tree, path, prefs, ok := h.folderTree(r.Context(), s, rawID)
	if !ok {
		writeJSON(w, http.StatusOK, emptyQueryResult(startIndex))
		return
	}
	wantFolders, wantChannels := liveFolderItemTypes(q)
	recursive, _ := strconv.ParseBool(q.Get("Recursive"))

	lineupOrder := map[string]int{}
	for i, ch := range h.bridge.Lineup(r.Context(), liveCaller(s)) {
		if ch.InstallationID == tree.installationID {
			lineupOrder[ch.ID] = i
		}
	}
	var folders []string
	var channels []liveChannel
	if recursive {
		wantFolders = false
		channels = tree.descendants(path, lineupOrder)
	} else {
		folders = slices.Clone(tree.children[path])
		channels = slices.Clone(tree.channels[path])
		slices.SortStableFunc(channels, func(a, b liveChannel) int { return cmp.Compare(lineupOrder[a.ID], lineupOrder[b.ID]) })
	}
	if !wantFolders {
		folders = nil
	}
	if !wantChannels {
		channels = nil
	}

	prefsFor := prefs[tree.installationID]
	if liveFolderFavoritesOnly(q) {
		folders = nil
		channels = slices.DeleteFunc(channels, func(ch liveChannel) bool { return !prefsFor.Favorites[ch.ID] })
	}
	if prefix := strings.ToLower(q.Get("NameStartsWith")); prefix != "" {
		folders = slices.DeleteFunc(folders, func(p string) bool { return !strings.HasPrefix(strings.ToLower(liveFolderName(p)), prefix) })
		channels = slices.DeleteFunc(channels, func(ch liveChannel) bool { return !strings.HasPrefix(strings.ToLower(ch.Name), prefix) })
	}

	slices.SortFunc(folders, func(a, b string) int {
		return strings.Compare(strings.ToLower(liveFolderName(a)), strings.ToLower(liveFolderName(b)))
	})
	sortBy, _, _ := strings.Cut(q.Get("SortBy"), ",")
	if strings.EqualFold(strings.TrimSpace(sortBy), "SortName") {
		slices.SortStableFunc(channels, func(a, b liveChannel) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
	}
	sortOrder, _, _ := strings.Cut(q.Get("SortOrder"), ",")
	if strings.EqualFold(strings.TrimSpace(sortOrder), "Descending") {
		slices.Reverse(folders)
		slices.Reverse(channels)
	}

	total := len(folders) + len(channels)
	start, end := min(startIndex, total), total
	if limit, err := strconv.Atoi(q.Get("Limit")); err == nil && limit > 0 {
		end = min(start+limit, total)
	}
	items := make([]any, 0, end-start)
	for i := start; i < end; i++ {
		if i < len(folders) {
			items = append(items, h.folderDTO(tree, folders[i], ""))
			continue
		}
		items = append(items, mapLiveChannel(channels[i-len(folders)], h.serverID, prefsFor))
	}
	writeJSON(w, http.StatusOK, liveFolderQueryResult{Items: items, TotalRecordCount: total, StartIndex: startIndex})
}

// liveFolderItemTypes reads IncludeItemTypes and ExcludeItemTypes into whether
// folders and channels are listed.
func liveFolderItemTypes(q caseInsensitiveQuery) (folders, channels bool) {
	has := func(list, itemType string) bool {
		for _, t := range strings.Split(list, ",") {
			if strings.EqualFold(strings.TrimSpace(t), itemType) {
				return true
			}
		}
		return false
	}
	folders, channels = true, true
	if include := q.Get("IncludeItemTypes"); strings.TrimSpace(include) != "" {
		folders, channels = has(include, liveFolderItemType), has(include, liveTVItemType)
	}
	if exclude := q.Get("ExcludeItemTypes"); exclude != "" {
		folders = folders && !has(exclude, liveFolderItemType)
		channels = channels && !has(exclude, liveTVItemType)
	}
	return folders, channels
}

func liveFolderFavoritesOnly(q caseInsensitiveQuery) bool {
	if fav, err := strconv.ParseBool(q.Get("IsFavorite")); err == nil && fav {
		return true
	}
	for _, f := range strings.Split(q.Get("Filters"), ",") {
		if strings.EqualFold(strings.TrimSpace(f), "IsFavorite") {
			return true
		}
	}
	return false
}

// wrapLatest answers GET /Items/Latest for the Live TV view or a channel
// folder with no items. Upstream ignores a ParentId it cannot decode and would
// list the latest items of every library under the view's name.
func (h *liveTVHandlers) wrapLatest(next http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.bridge == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		parentID := newCaseInsensitiveQuery(r.URL.Query()).Get("ParentId")
		if !isLiveFolderID(parentID) && !isLiveTVViewID(parentID) {
			next(w, r)
			return
		}
		if !validatePseudoUser(w, chi.URLParam(r, "id"), SessionFromContext(r.Context())) {
			return
		}
		writeJSON(w, http.StatusOK, []any{})
	}
}
