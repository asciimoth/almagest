import { configureAdminTransport } from "./admin_transport.js";
import { setPanelMode } from "./panel_mode.js";
import { loadWebDemoAPI, loadWebDemoColorMap, WASMAdminTransport } from "./wasm_transport.js";

setPanelMode(document.documentElement, "webdemo");
configureAdminTransport(() => new WASMAdminTransport(loadWebDemoAPI, loadWebDemoColorMap));
await import("./app.js");
