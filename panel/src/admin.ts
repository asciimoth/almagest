import { configureAdminTransport } from "./admin_transport.js";
import { WebSocketAdminTransport } from "./websocket_admin_transport.js";

configureAdminTransport(() => new WebSocketAdminTransport());
await import("./app.js");
