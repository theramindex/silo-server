// English copy for the premiere backport on the pre-catalog web client.
const messages = {
  "common.cancel": "Cancel",
  "common.done": "Done",
  "common.loading": "Loading",
  "common.retry": "Retry",
  "common.save": "Save",
  "web.premieres.availableChannels": "Available channels",
  "web.premieres.broadcaster": "Broadcaster",
  "web.premieres.broadcasterPlaceholder": "For example, FX",
  "web.premieres.channelsUnavailable":
    "Channels are currently unavailable. You can still turn reminders off or remove mappings.",
  "web.premieres.choose": "Choose a channel",
  "web.premieres.configure": "Premiere reminders",
  "web.premieres.description":
    "Get one reminder when a new episode is expected to premiere. Choose the channels to open for each broadcaster.",
  "web.premieres.empty": "Add a broadcaster and choose its channels to enable shortcuts.",
  "web.premieres.enabled": "Notify me about expected premieres",
  "web.premieres.expectedNow": "Premiere expected now",
  "web.premieres.expired":
    "The expected premiere window has ended or no mapped channel is available.",
  "web.premieres.failed": "Premiere settings could not be loaded. Try again.",
  "web.premieres.mappingRequired": "Enter a broadcaster and select at least one channel.",
  "web.premieres.newMapping": "Add broadcaster",
  "web.premieres.noMatches": "No channels match your search.",
  "web.premieres.override": "Use these channels for this show only",
  "web.premieres.profileOnly":
    "These settings apply to your current profile. Watching one complete episode, adding a favorite, or adding a show to your watchlist makes it eligible. Existing notification settings and series mutes still apply.",
  "web.premieres.remove": "Remove broadcaster",
  "web.premieres.saveFailed":
    "Premiere settings could not be saved. Check your channel choices and try again.",
  "web.premieres.saved": "Premiere settings saved",
  "web.premieres.search": "Search channels",
  "web.premieres.watch": "Watch on {broadcaster}",
  "web.premieres.window": "Expected broadcast window",
  "web.premieres.windowDescription":
    "The channel shortcut is available for this long after the expected start. Actual channel schedules may differ.",
  "web.premieres.windowMinutes": "{minutes} minutes",
} as const;
export function premiereText(
  key: keyof typeof messages,
  values: Record<string, string | number> = {},
): string {
  return messages[key].replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? ""));
}
