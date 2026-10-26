import { configureAdminTransport } from "./admin_transport.js";
import { DesktopAdminTransport } from "./desktop_admin_transport.js";
import { setPanelMode } from "./panel_mode.js";

// Select the native adapter before app.js evaluates. The daemon and web demo
// entry points do not import this module and keep their existing transports.
setPanelMode(document.documentElement, "desktop");
configureAdminTransport(() => new DesktopAdminTransport());
await import("./app.js");
