export type AdminTransportHandlers = {
  message(data: string): void;
  close(): void;
};

export interface AdminTransport {
  readonly connected: boolean;
  readonly reconnect: boolean;
  connect(handlers: AdminTransportHandlers): Promise<void>;
  send(data: string): void;
  close(): void;
}

export type AdminTransportFactory = () => AdminTransport;

let transportFactory: AdminTransportFactory = () => new WebSocketAdminTransport();

export function configureAdminTransport(factory: AdminTransportFactory): void {
  transportFactory = factory;
}

export function createAdminTransport(): AdminTransport {
  return transportFactory();
}

export class WebSocketAdminTransport implements AdminTransport {
  readonly reconnect = true;
  #socket: WebSocket | null = null;

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
      socket.addEventListener("open", () => resolve(), { once: true });
      socket.addEventListener("error", () => reject(new Error("WebSocket connection failed")), { once: true });
      socket.addEventListener("message", (event) => handlers.message(String(event.data)));
      socket.addEventListener("close", () => {
        reject(new Error("WebSocket connection closed"));
        if (this.#socket !== socket) return;
        this.#socket = null;
        handlers.close();
      });
    });
  }

  send(data: string): void {
    if (!this.connected) throw new Error("Admin API is not connected");
    this.#socket?.send(data);
  }

  close(): void {
    this.#socket?.close();
  }
}

function siteBaseURL(): URL {
  const configured = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
  return new URL(configured || "./", window.location.href);
}
