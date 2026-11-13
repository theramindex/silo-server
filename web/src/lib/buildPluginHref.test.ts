import { afterEach, expect, it } from "vitest";
import { buildPluginHref } from "./buildPluginHref";

afterEach(() => {
  delete document.documentElement.dataset.theme;
});

it("adds the theme before a channel routing fragment", () => {
  document.documentElement.dataset.theme = "midnight-cinema";
  const result = buildPluginHref(
    "/api/v2/plugin-content/plugins/4/dispatcharr#/watch/source%3A123",
  );
  const url = new URL(result, "https://silo.example");
  expect(url.searchParams.get("theme")).toBe("midnight-cinema");
  expect(url.hash).toBe("#/watch/source%3A123");
});
