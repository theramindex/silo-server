# livetv/v1 plugin contract

A Silo plugin that provides live TV serves these routes. Silo calls them in-process with the
headers `X-Silo-User-Id`, `X-Silo-User-Role` (`user`|`admin`) and `X-Silo-Profile-Id` when a
user is known. Logo and lineup requests may arrive without identity headers.

## Route access
Plugins MUST declare `info` and `stream` with access `admin`: `stream` URLs may carry
credentials and `info` reveals provider hosts, and Silo's plugin HTTP proxy serves every
declared route to any user the access level admits. `channels` and `programs` are declared `authenticated`;
Silo discovers providers by it and it carries no secrets. Silo calls all three routes
in-process regardless of the declared access.

## GET /silo-compat/livetv/v1/info
200 `{"contractVersion":1,"name":"<display name>","streamHosts":["host[:port]",...]}`
`streamHosts` lists every host the plugin may return in a `stream` URL. Silo refuses to fetch
any other first-hop host.

## GET /silo-compat/livetv/v1/channels
200 `[{"id","number","name","logoUrl","groupId","group","folders"}]`, the whole lineup in display order.
`id` is the plugin's stable channel ID, the same string the web app uses as a favorites key.
`groupId` is the category ID the web app uses as a `hiddenCategories` key. `logoUrl` is an
absolute http(s) URL or empty. There is no user state.
`folders` (optional) lists the folders the web app shows the channel in, each a path whose
levels are joined by `" / "` (`"US TV / NY / Locals / ABC"`). Only the deepest folders are
listed; their parents are implied. A channel without folders is in none. These are the admin's
layout; the optional fields below let Silo apply a user's own folder settings:
- `pipeFolders`: the channel's folders for a user whose `groupCategoriesByPipe` is true, when
  they differ from `folders`. Absent means the same as `folders`.
- `profileIds`: the provider channel profiles the channel is in.
- `profileFolders`: `[{"path","profileId"}]`, every folder path of the channel (not only the
  deepest) with the channel profile it comes from, or `""` for a path shown whatever the
  selection. A path two profiles share is listed once for each. Sent when the folders are
  built from channel profiles.

## GET /silo-compat/livetv/v1/stream?channel_id=<id>&format=ts|hls|any&session_id=<id>
200 `{"url":"<absolute URL>","container":"ts"|"hls","mode":"redirect"|"server","leased":bool}`
- `mode=server` means the URL embeds credentials. Silo must fetch it server-side and never
  pass it to a client.
- `session_id` (optional) is an opaque id of at most 128 characters for one play. A provider
  that limits connections counts the stream against its limits under that id and answers
  `"leased":true`; asking again with the same id keeps the same slot. Without `session_id`, or
  with `leased` false or absent, nothing is held.
- 404 `{"code":"channel_not_found"}` for an unknown channel.
- 409 `{"code":"connection_limit"}` when every connection the provider may use is taken.
- 422 `{"code":"format_unavailable"}` when the requested format cannot be produced.

## POST /silo-compat/livetv/v1/session/heartbeat?session_id=<id>
## POST /silo-compat/livetv/v1/session/stop?session_id=<id>
Optional; a provider that answers `leased` serves both, declared with access `admin`.
- A leased session keeps its slot for 90 s after its stream request or last heartbeat, then
  the provider frees it. Silo sends a heartbeat at most every 30 s while the play is in use.
- `heartbeat`: 200 `{}`, or 404 `{"code":"session_not_found"}` when the session is unknown or
  expired. Silo then asks for the stream again with the same `session_id`.
- `stop`: frees the slot. 200 `{}`, also for an unknown session.

## GET /silo-compat/livetv/v1/programs
Optional. A provider without guide data returns 404 or an empty list; Silo treats both as no
programs. Declared with access `authenticated`: it carries no secrets.

Query:
- `start`, `end` (required): unix seconds. A program is returned when it overlaps
  `[start,end)`: `startUnix < end && endUnix > start`.
- `channel_ids`: comma-separated channel `id`s, at most 2000. When absent, every channel.
- `class`: optional, one of `movie`, `sports`, `news`, `kids`, `series`. Only programs with that
  flag set are returned.
- `limit`: optional, the most programs to return. Without `channel_ids` the provider applies a
  default of 200 and a ceiling of 2000; with `channel_ids` it may return everything that
  matches.

200 `{"programs":[{"id","channelId","title","summary","startUnix","endUnix","imageUrl",
"categories","isMovie","isSports","isNews","isKids","isSeries","isLive","featured"}]}`
- Order: channels in lineup order (the `channel_ids` order when given), then `startUnix`; see
  `featured` for requests without `channel_ids`.
- `channelId` is a channel `id` from `channels`. `id` is stable for the life of the program.
- `imageUrl` is an absolute http(s) URL or empty. `categories` is a list of strings, possibly
  empty. `summary` may be empty.
- The flags classify the program for Jellyfin's Movies, Sports, News, Kids and Shows rows and
  its guide filters. A program may set none or several. `isLive` marks a live broadcast.
  Guide filler ("NO EVENT", "Off Air") sets none of them.
- `featured` marks a program the provider matched to a real event, such as a game in its
  sports guide. Without `channel_ids`, featured programs come first, then lineup order.
- A provider that loads guide data on demand may start loading the requested channels in the
  background and return what it already has.
- 400 `{"code":"bad_request"}` when `start` or `end` is missing or `class` is unknown.

## User state
Stored in Silo plugin user settings under key `preferences`, a JSON string. Per-profile state
lives at `preferences.profiles[<profileId>]` with keys `favorites` (`{channelId:true}`),
`favoriteOrder` (`[channelId]`) and `hiddenCategories` (`{groupId:true}`). When a profile has
no entry, readers use the legacy top-level `favorites`, `favoriteOrder` and
`hiddenCategories`. The first write for a profile stores a copy. Writers preserve every key
they do not own and never delete the legacy keys.

Account-wide keys Silo reads:
- `profileSelection`: `{"mode":"all"|"selected","profileIds":[...]}`. With `selected`, a channel
  is shown only when one of its `profileIds` is selected, and its folders are the deepest
  `profileFolders` paths whose `profileId` is selected or `""`. Selected ids no channel names
  are ignored; with none left, every profile is shown.
- `groupCategoriesByPipe`: when true, `pipeFolders` replace `folders`.
- `recentChannels` (`[channelId]`, newest first, at most 24) and `continueWatching`
  (`{channelId:{"itemKind":"channel","itemId","itemName","playedAt":<unix seconds>,"plays"}}`).
  Silo writes both when a Jellyfin client starts a channel, and from the third play adds an
  unfavorited channel to `autoFavorites` (`{channelId:true}`), as the web app does.
