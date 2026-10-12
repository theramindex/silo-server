// theramindex fork: wire types for the livetv/v1 plugin contract.

package jellycompat

// Routes a livetv/v1 plugin serves. docs/contracts/livetv-v1/contract.md is the
// source of truth for the shapes below.
const (
	liveTVPathInfo     = "/silo-compat/livetv/v1/info"
	liveTVPathChannels = "/silo-compat/livetv/v1/channels"
	liveTVPathStream   = "/silo-compat/livetv/v1/stream"
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

// liveTVStream is the response of liveTVPathStream. In "server" mode the URL
// embeds credentials and must never reach a client.
type liveTVStream struct {
	URL       string `json:"url"`
	Container string `json:"container"` // "ts" | "hls"
	Mode      string `json:"mode"`      // "redirect" | "server"
}
