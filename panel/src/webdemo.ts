import { configureAdminTransport } from "./admin_transport.js";
import { WASMAdminTransport } from "./wasm_transport.js";

configureAdminTransport(() => new WASMAdminTransport());
await import("./app.js");
