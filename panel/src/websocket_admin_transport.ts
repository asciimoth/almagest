import type { AdminTransport, AdminTransportHandlers } from "./admin_transport.js";

// Firefox can queue a loopback WebSocket connection for tens of seconds before
// it sends the handshake. Keep the deadline above that browser delay. A refused
// connection still reports an error immediately and uses the normal retry path.
export const ADMIN_API_CONNECT_TIMEOUT_MS = 60_000;

export class WebSocketAdminTransport implements AdminTransport {
  readonly reconnect = true;
  #socket: WebSocket | null = null;
  #closeCurrent: ((error?: Error) => void) | null = null;

  constructor(private readonly connectTimeoutMs = ADMIN_API_CONNECT_TIMEOUT_MS) {}

  get connected(): boolean {
    return this.#socket?.readyState === WebSocket.OPEN;
  }

  connect(handlers: AdminTransportHandlers): Promise<void> {
    return new Promise((resolve, reject) => {
      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const apiURL = new URL("./api/ws", siteBaseURL());
      apiURL.protocol = protocol;
      const socket = new WebSocket(apiURL);
      this.#socket = socket;
      let connectTimer = 0;
      const finishClose = (error = new Error("WebSocket connection closed")) => {
        window.clearTimeout(connectTimer);
        reject(error);
        if (this.#socket !== socket) return;
        this.#socket = null;
        this.#closeCurrent = null;
        handlers.close();
      };
      const closeSocket = (error?: Error) => {
        // Finish first because a test transport or browser can dispatch the
        // close event synchronously. A later native close event is ignored.
        finishClose(error);
        socket.close();
      };
      socket.addEventListener("open", () => {
        window.clearTimeout(connectTimer);
        resolve();
      }, { once: true });
      socket.addEventListener("error", () => {
        closeSocket(new Error("WebSocket connection failed"));
      }, { once: true });
      socket.addEventListener("message", (event) => handlers.message(String(event.data)));
      socket.addEventListener("close", () => finishClose());
      this.#closeCurrent = (error?: Error) => {
        // Do not wait for the browser to dispatch a close event. In particular,
        // a WebSocket in CONNECTING can take seconds to finish closing.
        closeSocket(error);
      };
      connectTimer = window.setTimeout(() => {
        if (this.#socket !== socket || socket.readyState !== WebSocket.CONNECTING) return;
        closeSocket(new Error("WebSocket connection timed out"));
      }, this.connectTimeoutMs);
    });
  }

  send(data: string): void {
    if (!this.connected) throw new Error("Admin API is not connected");
    this.#socket?.send(data);
  }

  close(): void {
    this.#closeCurrent?.();
  }
}

function siteBaseURL(): URL {
  const configured = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
  return new URL(configured || "./", window.location.href);
}
