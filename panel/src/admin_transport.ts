export type AdminTransportHandlers = {
  message(data: string): void;
  close(): void;
  status?(connected: boolean): void;
  diagnostic?(level: "debug" | "warn", message: string): void;
};

export interface AdminTransport {
  readonly connected: boolean;
  readonly reconnect: boolean;
  connect(handlers: AdminTransportHandlers): Promise<void>;
  send(data: string): void;
  close(): void;
}

export type AdminTransportFactory = () => AdminTransport;

let transportFactory: AdminTransportFactory | null = null;

export function configureAdminTransport(factory: AdminTransportFactory): void {
  transportFactory = factory;
}

export function createAdminTransport(): AdminTransport {
  if (!transportFactory) throw new Error("Admin transport was not configured");
  return transportFactory();
}
