import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import PremiereSettings from "./PremiereSettings";

afterEach(() => {
  cleanup();
  setAccessToken(null);
  setProfileId(null);
  vi.unstubAllGlobals();
});

it("persists edited channels and their window under the current profile", async () => {
  setAccessToken("premiere-settings-test");
  setProfileId("premiere-profile");
  const channels = ["FX East", "FX HD"].map((name, i) => ({
    id: String(i),
    name,
    provider: "Provider",
    group: "US",
    url: `/api/v2/plugin-content/plugins/1/tv#/watch/${i}`,
  }));
  const initial = {
    preferences: { enabled: true, window_minutes: 60 },
    mappings: [{ broadcaster: "FX", channel_ids: ["0"], window_minutes: 60 }],
    channels,
    channels_available: true,
  };
  let saved: unknown;
  let savedProfile: string | null = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (String(_input).includes("/capabilities"))
        return Response.json({ available: true, state: "available" });
      if (init?.method === "PUT") {
        saved = JSON.parse(String(init.body));
        savedProfile = new Headers(init.headers).get("X-Profile-Id");
      }
      return Response.json(initial);
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <PremiereSettings />
    </QueryClientProvider>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Premiere reminders" }));
  fireEvent.click(await screen.findByRole("button", { name: "FX" }));
  fireEvent.change(screen.getByRole("spinbutton", { name: "Expected broadcast window" }), {
    target: { value: "90" },
  });
  fireEvent.click(screen.getByRole("checkbox", { name: /FX HD/ }));
  fireEvent.click(screen.getByRole("button", { name: "Done" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(saved).toEqual({
      preferences: initial.preferences,
      mappings: [{ broadcaster: "FX", channel_ids: ["0", "1"], window_minutes: 90 }],
    }),
  );
  expect(savedProfile).toBe("premiere-profile");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  client.clear();
});
