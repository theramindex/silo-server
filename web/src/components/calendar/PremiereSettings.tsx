import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { premiereText as t } from "@/lib/premiereText";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { notificationScope } from "@/api/v2/notifications";
import {
  getPremiereConfiguration,
  savePremiereConfiguration,
  usePremiereCapability,
  type PremiereConfiguration,
  type PremiereMapping,
} from "@/hooks/queries/premieres";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

export default function PremiereSettings({
  seriesId,
  broadcaster,
}: {
  seriesId?: string;
  broadcaster?: string;
}) {
  const capability = usePremiereCapability();
  const [open, setOpen] = useState(false);
  const scope = notificationScope();
  const query = useQuery({
    queryKey: ["premieres", "settings", scope],
    queryFn: ({ signal }) => getPremiereConfiguration(signal),
    enabled: open && capability.data?.available === true,
  });
  if (capability.data?.available !== true) return null;
  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
        <Bell className="size-4" aria-hidden="true" />
        {t("web.premieres.configure")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[90dvh] max-w-2xl overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("web.premieres.configure")}</DialogTitle>
            <DialogDescription>{t("web.premieres.description")}</DialogDescription>
          </DialogHeader>
          {query.isPending ? (
            <p>{t("common.loading")}</p>
          ) : query.isError ? (
            <div role="alert">
              <p>{t("web.premieres.failed")}</p>
              <Button variant="outline" onClick={() => void query.refetch()}>
                {t("common.retry")}
              </Button>
            </div>
          ) : (
            <PremiereEditor
              key={scope + String(query.dataUpdatedAt)}
              initial={query.data}
              seriesId={seriesId}
              broadcaster={broadcaster}
              onClose={() => setOpen(false)}
            />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

function PremiereEditor({
  initial,
  seriesId,
  broadcaster,
  onClose,
}: {
  initial: PremiereConfiguration;
  seriesId?: string;
  broadcaster?: string;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const scope = notificationScope();
  const [preferences, setPreferences] = useState(initial.preferences);
  const [mappings, setMappings] = useState(initial.mappings);
  const [draft, setDraft] = useState<PremiereMapping | null>(null);
  const [editing, setEditing] = useState<number | null>(null);
  const [search, setSearch] = useState("");
  const mutation = useMutation({
    mutationFn: savePremiereConfiguration,
    onSuccess: (value) => {
      client.setQueryData(["premieres", "settings", scope], value);
      void client.invalidateQueries({ queryKey: ["premieres", "events", scope] });
      if (scope === notificationScope()) {
        toast.success(t("web.premieres.saved"));
        onClose();
      }
    },
  });
  const apply = () => {
    if (!draft?.broadcaster.trim() || draft.channel_ids.length === 0) {
      toast.error(t("web.premieres.mappingRequired"));
      return;
    }
    setMappings((rows) =>
      editing === null
        ? [...rows, draft]
        : rows.map((row, index) => (index === editing ? draft : row)),
    );
    setDraft(null);
    setEditing(null);
    setSearch("");
  };
  const choices = initial.channels.filter(
    (channel) =>
      !search ||
      `${channel.name} ${channel.group} ${channel.provider}`
        .toLocaleLowerCase()
        .includes(search.toLocaleLowerCase()),
  );
  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between gap-4">
        <Label htmlFor="premiere-enabled">{t("web.premieres.enabled")}</Label>
        <Switch
          id="premiere-enabled"
          checked={preferences.enabled}
          onCheckedChange={(enabled) => setPreferences({ ...preferences, enabled })}
        />
      </div>
      <p className="text-muted-foreground text-sm">{t("web.premieres.profileOnly")}</p>
      {!initial.channels_available && (
        <p role="status" className="text-muted-foreground text-sm">
          {t("web.premieres.channelsUnavailable")}
        </p>
      )}
      <div className="space-y-2">
        {mappings.length === 0 && (
          <p className="text-muted-foreground text-sm">{t("web.premieres.empty")}</p>
        )}
        {mappings.map((mapping, index) => (
          <div
            className="flex items-center gap-2 border-b py-2"
            key={`${mapping.broadcaster}:${mapping.series_id}`}
          >
            <Button
              variant="ghost"
              className="min-w-0 flex-1 justify-start text-start"
              onClick={() => {
                setDraft({ ...mapping });
                setEditing(index);
              }}
            >
              <span className="truncate">
                <MappingLabel mapping={mapping} />
              </span>
            </Button>
            <span className="text-muted-foreground shrink-0 text-xs">
              {t("web.premieres.windowMinutes", { minutes: mapping.window_minutes })}
            </span>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("web.premieres.remove")}
              onClick={() => setMappings((rows) => rows.filter((_, row) => row !== index))}
            >
              <Trash2 className="size-4" />
            </Button>
          </div>
        ))}
      </div>
      {draft ? (
        <div className="space-y-4 rounded-xl border p-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="premiere-broadcaster">{t("web.premieres.broadcaster")}</Label>
              <Input
                id="premiere-broadcaster"
                value={draft.broadcaster}
                maxLength={200}
                placeholder={t("web.premieres.broadcasterPlaceholder")}
                onChange={(event) => setDraft({ ...draft, broadcaster: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="premiere-window">{t("web.premieres.window")}</Label>
              <Input
                id="premiere-window"
                type="number"
                min={15}
                max={240}
                value={draft.window_minutes}
                onChange={(event) =>
                  setDraft({ ...draft, window_minutes: Number(event.target.value) })
                }
              />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">{t("web.premieres.windowDescription")}</p>
          {seriesId && (
            <div className="flex items-center gap-2">
              <Switch
                id="premiere-override"
                checked={Boolean(draft.series_id)}
                onCheckedChange={(checked) =>
                  setDraft({ ...draft, series_id: checked ? seriesId : undefined })
                }
              />
              <Label htmlFor="premiere-override">{t("web.premieres.override")}</Label>
            </div>
          )}
          <Label htmlFor="premiere-search">{t("web.premieres.availableChannels")}</Label>
          <Input
            id="premiere-search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t("web.premieres.search")}
          />
          <div className="max-h-56 space-y-1 overflow-y-auto">
            {initial.channels.length === 0 && (
              <p className="text-muted-foreground text-sm">
                {t("web.premieres.channelsUnavailable")}
              </p>
            )}
            {initial.channels.length > 0 && choices.length === 0 && (
              <p className="text-muted-foreground text-sm">{t("web.premieres.noMatches")}</p>
            )}
            {choices.slice(0, 100).map((channel) => (
              <label
                key={channel.id}
                className="hover:bg-accent flex cursor-pointer items-center gap-3 rounded-md p-2"
              >
                <input
                  type="checkbox"
                  checked={draft.channel_ids.includes(channel.id)}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      channel_ids: event.target.checked
                        ? [...draft.channel_ids, channel.id]
                        : draft.channel_ids.filter((id) => id !== channel.id),
                    })
                  }
                />
                <span className="min-w-0">
                  <span className="block truncate text-sm">{channel.name}</span>
                  <span className="text-muted-foreground block truncate text-xs">
                    {[channel.provider, channel.group].filter(Boolean).join(" · ")}
                  </span>
                </span>
              </label>
            ))}
          </div>
          <div className="flex gap-2">
            <Button onClick={apply}>{t("common.done")}</Button>
            <Button
              variant="ghost"
              onClick={() => {
                setDraft(null);
                setEditing(null);
              }}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      ) : (
        <Button
          variant="outline"
          onClick={() => {
            setEditing(null);
            setDraft({
              broadcaster: broadcaster ?? "",
              channel_ids: [],
              window_minutes: preferences.window_minutes,
              series_id: seriesId,
            });
          }}
        >
          <Plus className="size-4" />
          {t("web.premieres.newMapping")}
        </Button>
      )}
      {mutation.isError && (
        <p role="alert" className="text-destructive text-sm">
          {t("web.premieres.saveFailed")}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button
          disabled={mutation.isPending || draft !== null}
          onClick={() => mutation.mutate({ preferences, mappings })}
        >
          {t("common.save")}
        </Button>
      </div>
    </div>
  );
}

function MappingLabel({ mapping }: { mapping: PremiereMapping }) {
  const detail = useCatalogItemDetail(mapping.series_id || undefined);
  return (
    <>
      {[mapping.broadcaster, mapping.series_id ? detail.data?.title || mapping.series_id : null]
        .filter(Boolean)
        .join(" · ")}
    </>
  );
}
