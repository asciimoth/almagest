import { configureAdminTransport } from "./admin_transport.js";
import { loadWebDemoAPI, loadWebDemoColorMap, WASMAdminTransport } from "./wasm_transport.js";

configureAdminTransport(() => new WASMAdminTransport(loadWebDemoAPI, loadWebDemoColorMap));
await import("./app.js");
