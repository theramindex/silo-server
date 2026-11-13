import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Premiere } from "@/hooks/queries/premieres";
import PremiereShortcut from "./PremiereShortcut";

const mocks = vi.hoisted(() => ({
  events: [] as Premiere[],
  now: 0,
  navigate: vi.fn(),
  refetch: vi.fn(),
}));
vi.mock("@/hooks/queries/premieres", () => ({
  usePremiereEvents: () => ({
    data: mocks.events,
    now: mocks.now,
    available: true,
    isError: false,
    refetch: mocks.refetch,
  }),
}));
vi.mock("@/lib/buildPluginHref", () => ({ navigateToPluginRoute: mocks.navigate }));
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  mocks.navigate.mockClear();
  mocks.refetch.mockClear();
});

it("groups fifteen feeds into one action and opens only the chosen channel", () => {
  const now = Date.now();
  mocks.now = now;
  mocks.events = [
    {
      episode_id: "episode",
      series_id: "series",
      title: "Example",
      episode_title: "New episode",
      season: 2,
      episode: 4,
      starts_at: new Date(now - 60_000).toISOString(),
      ends_at: new Date(now + 60_000).toISOString(),
      active: true,
      broadcaster: "FX",
      channels: Array.from({ length: 15 }, (_, i) => ({
        id: String(i),
        name: `FX ${i + 1}`,
        provider: "Provider",
        group: "US",
        url: `/api/v2/plugin-content/plugins/1/dispatcharr#/watch/${i}`,
      })),
    },
  ];
  render(<PremiereShortcut itemId="episode" />);
  expect(screen.getAllByRole("button", { name: "Watch on FX" })).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Watch on FX" }));
  expect(mocks.navigate).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "FX 7 Provider US" }));
  expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith(
    "/api/v2/plugin-content/plugins/1/dispatcharr#/watch/6",
  );
});

it("rechecks expiry when clicked and does not open an ended broadcast", () => {
  const now = Date.now();
  mocks.now = now;
  mocks.events = [
    {
      episode_id: "episode",
      series_id: "series",
      title: "Example",
      episode_title: "New episode",
      season: 2,
      episode: 4,
      starts_at: new Date(now - 60_000).toISOString(),
      ends_at: new Date(now + 1000).toISOString(),
      active: true,
      broadcaster: "FX",
      channels: [
        {
          id: "one",
          name: "FX",
          provider: "Provider",
          group: "US",
          url: "/api/v2/plugin-content/plugins/1/dispatcharr#/watch/one",
        },
      ],
    },
  ];
  render(<PremiereShortcut itemId="episode" />);
  vi.spyOn(Date, "now").mockReturnValue(now + 2000);
  fireEvent.click(screen.getByRole("button", { name: "Watch on FX" }));
  expect(mocks.navigate).not.toHaveBeenCalled();
  expect(mocks.refetch).toHaveBeenCalledOnce();
});
