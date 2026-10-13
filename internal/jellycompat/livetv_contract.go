// theramindex fork: wire types for the livetv/v1 plugin contract.

package jellycompat

// Routes a livetv/v1 plugin serves. docs/contracts/livetv-v1/contract.md is the
// source of truth for the shapes below.
const (
	liveTVPathInfo     = "/silo-compat/livetv/v1/info"
	liveTVPathChannels = "/silo-compat/livetv/v1/channels"
	liveTVPathStream   = "/silo-compat/livetv/v1/stream"
	liveTVPathPrograms = "/silo-compat/livetv/v1/programs"
)

// Stream containers and delivery modes a livetv/v1 stream response may carry,
// and the query parameter that names the channel.
const (
	liveTVContainerTS    = "ts"
	liveTVContainerHLS   = "hls"
	liveTVModeRedirect   = "redirect"
	liveTVModeServer     = "server"
	liveTVChannelIDParam = "channel_id"
)

// liveTVInfo is the response of liveTVPathInfo. StreamHosts lists every
// host[:port] the plugin may return in a stream URL.
type liveTVInfo struct {
	ContractVersion int      `json:"contractVersion"`
	Name            string   `json:"name"`
	StreamHosts     []string `json:"streamHosts"`
}

// liveTVChannel is one entry of the liveTVPathChannels response. ID is the
// plugin's stable channel id; GroupID is the category id the web app keys
// hiddenCategories by.
type liveTVChannel struct {
	ID      string `json:"id"`
	Number  string `json:"number"`
	Name    string `json:"name"`
	LogoURL string `json:"logoUrl"`
	GroupID string `json:"groupId"`
	Group   string `json:"group"`
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
// embeds credentials and must never reach a client.
type liveTVStream struct {
	URL       string `json:"url"`
	Container string `json:"container"` // "ts" | "hls"
	Mode      string `json:"mode"`      // "redirect" | "server"
}
