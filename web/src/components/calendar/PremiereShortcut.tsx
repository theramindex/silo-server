import { useState } from "react";
import { Radio } from "lucide-react";
import { usePremiereEvents } from "@/hooks/queries/premieres";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { premiereText as t } from "@/lib/premiereText";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

export default function PremiereShortcut({
  itemId,
  series = false,
  explainExpired = false,
}: {
  itemId: string;
  series?: boolean;
  explainExpired?: boolean;
}) {
  const { data, now, available, isError, refetch } = usePremiereEvents();
  const [open, setOpen] = useState(false);
  const event = data?.find(
    (entry) =>
      (series ? entry.series_id === itemId : entry.episode_id === itemId) &&
      Date.parse(entry.starts_at) <= now &&
      now < Date.parse(entry.ends_at),
  );
  if (!available) return null;
  if (isError && explainExpired)
    return (
      <Button variant="ghost" onClick={() => void refetch()}>
        {t("common.retry")}
      </Button>
    );
  if (!event)
    return explainExpired && data ? (
      <p className="text-muted-foreground text-sm">{t("web.premieres.expired")}</p>
    ) : null;

  const watch = (url: string) => {
    // eslint-disable-next-line react-hooks/purity -- Check click time after a suspended tab resumes.
    if (Date.now() >= Date.parse(event.ends_at)) {
      setOpen(false);
      void refetch();
      return;
    }
    void navigateToPluginRoute(url);
  };
  const singleChannel = event.channels.length === 1 ? event.channels[0] : undefined;
  return (
    <>
      <Button
        variant="secondary"
        size="sm"
        className="max-w-full gap-2"
        onClick={() => (singleChannel ? watch(singleChannel.url) : setOpen(true))}
      >
        <Radio className="size-4 shrink-0" aria-hidden="true" />
        <span className="truncate">
          {t("web.premieres.watch", { broadcaster: event.broadcaster })}
        </span>
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("web.premieres.choose")}</DialogTitle>
            <DialogDescription>
              {[event.title, t("web.premieres.expectedNow")].join(" · ")}
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-80 space-y-2 overflow-y-auto">
            {event.channels.map((channel) => (
              <Button
                key={channel.id}
                variant="outline"
                aria-label={[channel.name, channel.provider, channel.group]
                  .filter(Boolean)
                  .join(" ")}
                className="h-auto w-full justify-start gap-3 py-3 text-start"
                onClick={() => watch(channel.url)}
              >
                <Radio className="size-4 shrink-0" aria-hidden="true" />
                <span className="min-w-0">
                  <span className="block truncate">{channel.name}</span>
                  <span className="text-muted-foreground block truncate text-xs">
                    {[channel.provider, channel.group].filter(Boolean).join(" · ")}
                  </span>
                </span>
              </Button>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
