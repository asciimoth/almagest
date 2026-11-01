import { configureAdminTransport } from "./admin_transport.js";
import { DesktopAdminTransport } from "./desktop_admin_transport.js";
import { invoke } from "@tauri-apps/api/core";
import { configureDesktopEventLogger } from "./desktop_event_log.js";
import { setPanelMode } from "./panel_mode.js";
import {
  rememberedDesktopPanelTab,
} from "./desktop_route_memory.js";

// Select the native adapter before app.js evaluates. The daemon and web demo
// entry points do not import this module and keep their existing transports.
setPanelMode(document.documentElement, "desktop");
configureDesktopEventLogger((event) => {
  void invoke("log_desktop_event", { event }).catch((error: unknown) => {
    console.warn(`[almagest-desktop] could not log ${event}`, error);
  });
});

// Select the saved tab before app.js paints its first API-backed frame.
const tab = rememberedDesktopPanelTab(window.localStorage);
const savedHash = tab ? `#${encodeURIComponent(tab)}` : "";
if (savedHash && window.location.hash !== savedHash) {
  window.history.replaceState(window.history.state, "", savedHash);
}
configureAdminTransport(() => new DesktopAdminTransport());
await import("./app.js");
