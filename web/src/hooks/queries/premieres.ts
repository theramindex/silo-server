import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { v2, type V2Result, type V2Body } from "@/api/v2/request";
import {
  captureNotificationAuthority,
  notificationScope,
  requireNotificationAuthority,
} from "@/api/v2/notifications";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

export type Premiere = V2Result<"GET /api/v2/premieres">["items"][number];
export type PremiereConfiguration = V2Result<"GET /api/v2/premieres/settings">;
export type PremiereMapping = PremiereConfiguration["mappings"][number];

export function usePremiereCapability() {
  const { hasSelectedProfile } = useCurrentProfile();
  return useQuery({
    queryKey: ["premieres", "capabilities", notificationScope()],
    enabled: hasSelectedProfile,
    queryFn: ({ signal }) => v2("GET /api/v2/premieres/capabilities", { signal }),
    staleTime: 60_000,
    retry: false,
  });
}

export function usePremiereEvents() {
  const capability = usePremiereCapability();
  const query = useQuery({
    queryKey: ["premieres", "events", notificationScope()],
    enabled: capability.data?.available === true,
    queryFn: async ({ signal }) => {
      const authority = captureNotificationAuthority();
      requireNotificationAuthority(authority);
      const result = await v2("GET /api/v2/premieres", { signal, profileContext: authority });
      requireNotificationAuthority(authority);
      return result.items;
    },
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const instant = Date.now();
    const next = query.data
      ?.flatMap((event) => [Date.parse(event.starts_at), Date.parse(event.ends_at)])
      .filter((stamp) => stamp > instant)
      .sort((a, b) => a - b)[0];
    if (next === undefined) return;
    const timer = window.setTimeout(
      () => setNow(Date.now()),
      Math.min(next - instant + 1, 2_147_483_647),
    );
    return () => window.clearTimeout(timer);
  }, [query.data, now]);
  return {
    ...query,
    now: Math.max(now, query.dataUpdatedAt),
    available: capability.data?.available === true,
  };
}

export async function getPremiereConfiguration(signal?: AbortSignal) {
  const authority = captureNotificationAuthority();
  requireNotificationAuthority(authority);
  const result = await v2("GET /api/v2/premieres/settings", { signal, profileContext: authority });
  requireNotificationAuthority(authority);
  return result;
}

export async function savePremiereConfiguration(body: V2Body<"PUT /api/v2/premieres/settings">) {
  const authority = captureNotificationAuthority();
  requireNotificationAuthority(authority);
  const result = await v2("PUT /api/v2/premieres/settings", { body, profileContext: authority });
  requireNotificationAuthority(authority);
  return result;
}
