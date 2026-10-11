# livetv/v1 plugin contract

A Silo plugin that provides live TV serves these routes. Silo calls them in-process with the
headers `X-Silo-User-Id`, `X-Silo-User-Role` (`user`|`admin`) and `X-Silo-Profile-Id` when a
user is known. Logo and lineup requests may arrive without identity headers.

## GET /silo-compat/livetv/v1/info
200 `{"contractVersion":1,"name":"<display name>","streamHosts":["host[:port]",...]}`
`streamHosts` lists every host the plugin may return in a `stream` URL. Silo refuses to fetch
any other first-hop host.

## GET /silo-compat/livetv/v1/channels
200 `[{"id","number","name","logoUrl","groupId","group"}]`, the whole lineup in display order.
`id` is the plugin's stable channel ID, the same string the web app uses as a favorites key.
`groupId` is the category ID the web app uses as a `hiddenCategories` key. `logoUrl` is an
absolute http(s) URL or empty. There is no user state.

## GET /silo-compat/livetv/v1/stream?channel_id=<id>&format=ts|hls|any
200 `{"url":"<absolute URL>","container":"ts"|"hls","mode":"redirect"|"server"}`
- `mode=server` means the URL embeds credentials. Silo must fetch it server-side and never
  pass it to a client.
- 404 `{"code":"channel_not_found"}` for an unknown channel.
- 422 `{"code":"format_unavailable"}` when the requested format cannot be produced.

## User state
Stored in Silo plugin user settings under key `preferences`, a JSON string. Per-profile state
lives at `preferences.profiles[<profileId>]` with keys `favorites` (`{channelId:true}`),
`favoriteOrder` (`[channelId]`) and `hiddenCategories` (`{groupId:true}`). When a profile has
no entry, readers use the legacy top-level `favorites`, `favoriteOrder` and
`hiddenCategories`. The first write for a profile stores a copy. Writers preserve every key
they do not own and never delete the legacy keys.
