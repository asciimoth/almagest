import { configureAdminTransport } from "./admin_transport.js";
import { DesktopAdminTransport } from "./desktop_admin_transport.js";
import { setPanelMode } from "./panel_mode.js";
import {
  rememberedDesktopPanelTab,
} from "./desktop_route_memory.js";

// Select the native adapter before app.js evaluates. The daemon and web demo
// entry points do not import this module and keep their existing transports.
setPanelMode(document.documentElement, "desktop");

// Select the saved tab before app.js paints its first API-backed frame.
const tab = rememberedDesktopPanelTab(window.localStorage);
const savedHash = tab ? `#${encodeURIComponent(tab)}` : "";
if (savedHash && window.location.hash !== savedHash) {
  window.history.replaceState(window.history.state, "", savedHash);
}
configureAdminTransport(() => new DesktopAdminTransport());
await import("./app.js");
