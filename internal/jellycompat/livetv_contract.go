// theramindex fork: wire types for the livetv/v1 plugin contract.

package jellycompat

// Routes a livetv/v1 plugin serves. docs/contracts/livetv-v1/contract.md is the
// source of truth for the shapes below.
const (
	liveTVPathInfo     = "/silo-compat/livetv/v1/info"
	liveTVPathChannels = "/silo-compat/livetv/v1/channels"
	liveTVPathStream   = "/silo-compat/livetv/v1/stream"
	liveTVPathPrograms = "/silo-compat/livetv/v1/programs"
	// The session routes are optional. A provider that leases connection
	// slots serves them; see liveTVStream.Leased.
	liveTVPathSessionHeartbeat = "/silo-compat/livetv/v1/session/heartbeat"
	liveTVPathSessionStop      = "/silo-compat/livetv/v1/session/stop"
)

// Stream containers and delivery modes a livetv/v1 stream response may carry,
// and the query parameter that names the channel.
const (
	liveTVContainerTS    = "ts"
	liveTVContainerHLS   = "hls"
	liveTVModeRedirect   = "redirect"
	liveTVModeServer     = "server"
	liveTVChannelIDParam = "channel_id"
	liveTVSessionIDParam = "session_id"
)

// Error codes a provider answers stream and session requests with.
const (
	liveTVCodeConnectionLimit = "connection_limit"
	liveTVCodeSessionNotFound = "session_not_found"
)

// liveTVError is a provider's error body.
type liveTVError struct {
	Code string `json:"code"`
}

// liveTVInfo is the response of liveTVPathInfo. StreamHosts lists every
// host[:port] the plugin may return in a stream URL. PerUserLineup means every
// user has their own lineup, guide and stream hosts, so nothing the provider
// answers may be shared between users; see liveScope.
type liveTVInfo struct {
	ContractVersion int      `json:"contractVersion"`
	Name            string   `json:"name"`
	StreamHosts     []string `json:"streamHosts"`
	PerUserLineup   bool     `json:"perUserLineup"`
}

// liveTVChannel is one entry of the liveTVPathChannels response. ID is the
// plugin's stable channel id; GroupID is the category id the web app keys
// hiddenCategories by. Folders are the web app's folder paths for the channel,
// levels joined by " / ", deepest folders only: the admin's layout. The other
// fields let a user's own folder settings change it: PipeFolders replace
// Folders for a user who groups categories by "|", and ProfileIDs and
// ProfileFolders apply a user's channel profile selection.
type liveTVChannel struct {
	ID             string                `json:"id"`
	Number         string                `json:"number"`
	Name           string                `json:"name"`
	LogoURL        string                `json:"logoUrl"`
	GroupID        string                `json:"groupId"`
	Group          string                `json:"group"`
	Folders        []string              `json:"folders"`
	PipeFolders    []string              `json:"pipeFolders"`
	ProfileIDs     []string              `json:"profileIds"`
	ProfileFolders []liveTVProfileFolder `json:"profileFolders"`
}

// liveTVProfileFolder is one of a channel's folders and the channel profile it
// comes from; an empty ProfileID means it is shown whatever the selection.
// Every path is listed, not only the deepest.
type liveTVProfileFolder struct {
	Path      string `json:"path"`
	ProfileID string `json:"profileId"`
}

// Query parameters of liveTVPathPrograms.
const (
	liveTVProgramsStartParam    = "start"
	liveTVProgramsEndParam      = "end"
	liveTVProgramsChannelsParam = "channel_ids"
	liveTVProgramsClassParam    = "class"
	liveTVProgramsLimitParam    = "limit"
)

// Program classes the programs route filters by.
const (
	liveTVClassMovie  = "movie"
	liveTVClassSports = "sports"
	liveTVClassNews   = "news"
	liveTVClassKids   = "kids"
	liveTVClassSeries = "series"
)

// liveTVProgram is one entry of the liveTVPathPrograms response. ChannelID is
// a channel ID from liveTVPathChannels.
type liveTVProgram struct {
	ID         string   `json:"id"`
	ChannelID  string   `json:"channelId"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	StartUnix  int64    `json:"startUnix"`
	EndUnix    int64    `json:"endUnix"`
	ImageURL   string   `json:"imageUrl"`
	Categories []string `json:"categories"`
	IsMovie    bool     `json:"isMovie"`
	IsSports   bool     `json:"isSports"`
	IsNews     bool     `json:"isNews"`
	IsKids     bool     `json:"isKids"`
	IsSeries   bool     `json:"isSeries"`
	IsLive     bool     `json:"isLive"`
	// Featured marks a program the provider matched to a real event, such
	// as a game in its sports guide.
	Featured bool `json:"featured"`
}

// liveTVPrograms is the response of liveTVPathPrograms.
type liveTVPrograms struct {
	Programs []liveTVProgram `json:"programs"`
}

// liveTVStream is the response of liveTVPathStream. In "server" mode the URL
// embeds credentials and must never reach a client. Leased means the provider
// holds a connection slot for the request's session_id until the session
// stops or goes a session window without a heartbeat.
type liveTVStream struct {
	URL       string `json:"url"`
	Container string `json:"container"` // "ts" | "hls"
	Mode      string `json:"mode"`      // "redirect" | "server"
	Leased    bool   `json:"leased"`
}
