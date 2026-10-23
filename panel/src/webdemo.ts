import { configureAdminTransport, type AdminTransport, type AdminTransportHandlers } from "./admin_transport.js";

class PendingWASMTransport implements AdminTransport {
  readonly connected = false;
  readonly reconnect = false;

  connect(_handlers: AdminTransportHandlers): Promise<void> {
    return Promise.reject(new Error("The browser engine is not included in this build yet"));
  }

  send(_data: string): void {
    throw new Error("The browser engine is not included in this build yet");
  }

  close(): void {}
}

configureAdminTransport(() => new PendingWASMTransport());
await import("./app.js");
