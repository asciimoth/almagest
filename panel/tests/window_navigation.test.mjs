import assert from "node:assert/strict";
import { test } from "node:test";

import {
  adminPanelWindowName,
  openAdminPanelRoute,
  openOrFocusAdminPanel,
} from "../public/build/window_navigation.js";

test("admin panel window names are isolated by origin", () => {
  assert.equal(
    adminPanelWindowName("https://almagest.test:8443"),
    "almagest-admin-panel:https://almagest.test:8443",
  );
});

test("an existing admin panel window is focused without navigation", () => {
  let replacement = "";
  let focused = false;
  let target = "";
  const panel = {
    location: {
      href: "https://almagest.test/",
      replace: (url) => { replacement = url; },
    },
    focus: () => { focused = true; },
  };
  const source = {
    location: { origin: "https://almagest.test" },
    open: (_url, name) => {
      target = name;
      return panel;
    },
  };

  assert.equal(openOrFocusAdminPanel(source, "https://almagest.test/"), true);
  assert.equal(target, "almagest-admin-panel:https://almagest.test");
  assert.equal(replacement, "");
  assert.equal(focused, true);
});

test("a new blank window is navigated to the admin panel", () => {
  let replacement = "";
  const panel = {
    location: {
      href: "about:blank",
      replace: (url) => { replacement = url; },
    },
    focus: () => {},
  };
  const source = {
    location: { origin: "https://almagest.test" },
    open: () => panel,
  };

  assert.equal(openOrFocusAdminPanel(source, "https://almagest.test/"), true);
  assert.equal(replacement, "https://almagest.test/");
});

test("a blocked pop-up reports that the link fallback is required", () => {
  const source = {
    location: { origin: "https://almagest.test" },
    open: () => null,
  };

  assert.equal(openOrFocusAdminPanel(source, "https://almagest.test/"), false);
});

test("a panel route navigates an existing named panel to the requested tab", () => {
  let openedURL = "";
  let target = "";
  let focused = false;
  const source = {
    location: { origin: "https://almagest.test" },
    open: (url, name) => {
      openedURL = url;
      target = name;
      return { focus: () => { focused = true; } };
    },
  };

  assert.equal(openAdminPanelRoute(source, "https://almagest.test/#logs"), true);
  assert.equal(openedURL, "https://almagest.test/#logs");
  assert.equal(target, "almagest-admin-panel:https://almagest.test");
  assert.equal(focused, true);
});

test("a blocked panel route keeps the anchor fallback available", () => {
  const source = {
    location: { origin: "https://almagest.test" },
    open: () => null,
  };

  assert.equal(openAdminPanelRoute(source, "https://almagest.test/#logs"), false);
});
