import { Channel, invoke } from "@tauri-apps/api/core";

import type { AdminTransport, AdminTransportHandlers } from "./admin_transport.js";

type NativeAdminEvent =
  | { type: "connection_status"; connected: boolean }
  | { type: "connection_error"; message: string }
  | { type: "message"; message: string };

export const DESKTOP_ADMIN_LISTENER_TIMEOUT_MS = 3_000;

/** The small native API that the desktop transport is permitted to use. */
export interface NativeAdminIPC {
  listen(handler: (event: unknown) => void): Promise<void>;
  send(message: string): Promise<void>;
}

/**
 * Call the two fixed Tauri commands that pass the admin protocol through the
 * Tauri IPC boundary. Command names and payload shapes cannot come from panel
 * data. Rust validates admin messages again before it writes to the transport.
 */
export class TauriAdminIPC implements NativeAdminIPC {
  listen(handler: (event: unknown) => void): Promise<void> {
    const channel = new Channel<unknown>(handler);
    return invoke("listen_admin", { channel });
  }

  send(message: string): Promise<void> {
    return invoke("send_admin_message", { message });
  }
}

/**
 * Adapt the native admin channel to the transport contract used by the SPA.
 * Native code owns endpoint access and retries. This class only forwards JSON
 * messages and reports native connection state to the existing panel logic.
 */
export class DesktopAdminTransport implements AdminTransport {
  readonly reconnect = true;
  #active = false;
  #connected = false;
  #generation = 0;
  #handlers: AdminTransportHandlers | null = null;
  #listenerTimer = 0;
  #rejectConnect: ((error: Error) => void) | null = null;

  constructor(
    private readonly ipc: NativeAdminIPC = new TauriAdminIPC(),
    private readonly listenerTimeoutMs = DESKTOP_ADMIN_LISTENER_TIMEOUT_MS,
  ) {}

  get connected(): boolean {
    return this.#connected;
  }

  connect(handlers: AdminTransportHandlers): Promise<void> {
    this.close();
    const generation = ++this.#generation;
    this.#active = true;
    this.#handlers = handlers;

    return new Promise((resolve, reject) => {
      this.#rejectConnect = reject;
      handlers.diagnostic?.("debug", "invoking the native listen_admin command");
      this.#listenerTimer = globalThis.setTimeout(() => {
        if (!this.#isCurrent(generation) || this.#rejectConnect === null) return;
        const error = new Error("Native listen_admin did not report status before the timeout");
        handlers.diagnostic?.("warn", error.message);
        reject(error);
        this.#rejectConnect = null;
        this.#finishClose();
      }, this.listenerTimeoutMs);
      this.ipc.listen((value) => {
        const event = parseNativeAdminEvent(value);
        if (!this.#isCurrent(generation)) return;
        if (event === null) {
          handlers.diagnostic?.("warn", "ignored an invalid native admin event");
          return;
        }
        this.#clearListenerTimer();
        if (event.type === "message") {
          handlers.message(event.message);
          return;
        }
        if (event.type === "connection_error") {
          handlers.diagnostic?.("warn", event.message);
          return;
        }
        this.#connected = event.connected;
        handlers.status?.(event.connected);
        handlers.diagnostic?.("debug", `native admin connection is ${event.connected ? "connected" : "disconnected"}`);
        if (event.connected) {
          this.#rejectConnect = null;
          resolve();
        } else if (this.#rejectConnect === null) {
          // After a completed connection, let the SPA reset its API session.
          // During the initial connection, keep this listener registered so
          // the native retry supervisor can later report a successful state.
          this.#finishClose();
        }
      }).then(() => {
        if (!this.#isCurrent(generation)) return;
        handlers.diagnostic?.("debug", "native listen_admin command registered");
      }).catch((error: unknown) => {
        if (!this.#isCurrent(generation)) return;
        this.#clearListenerTimer();
        const registrationError = asError(error, "register native admin listener");
        handlers.diagnostic?.("warn", registrationError.message);
        reject(registrationError);
        this.#rejectConnect = null;
        this.#finishClose();
      });
    });
  }

  send(data: string): void {
    if (!this.#connected) throw new Error("Admin API is not connected");
    const generation = this.#generation;
    void this.ipc.send(data).catch((error: unknown) => {
      if (!this.#isCurrent(generation)) return;
      const sendError = asError(error, "send native admin message");
      this.#handlers?.diagnostic?.("warn", sendError.message);
      this.#rejectConnect?.(sendError);
      this.#rejectConnect = null;
      this.#finishClose();
    });
  }

  close(): void {
    if (!this.#active) return;
    this.#rejectConnect?.(new Error("Admin API connection closed"));
    this.#rejectConnect = null;
    this.#finishClose();
  }

  #finishClose(): void {
    this.#clearListenerTimer();
    const handlers = this.#handlers;
    this.#active = false;
    this.#connected = false;
    this.#handlers = null;
    handlers?.close();
  }

  #clearListenerTimer(): void {
    globalThis.clearTimeout(this.#listenerTimer);
    this.#listenerTimer = 0;
  }

  #isCurrent(generation: number): boolean {
    return this.#active && generation === this.#generation;
  }
}

function parseNativeAdminEvent(value: unknown): NativeAdminEvent | null {
  if (!isRecord(value) || typeof value.type !== "string") return null;
  if (value.type === "connection_status" && typeof value.connected === "boolean") {
    return { type: value.type, connected: value.connected };
  }
  if (value.type === "connection_error" && typeof value.message === "string") {
    return { type: value.type, message: value.message };
  }
  if (value.type === "message" && typeof value.message === "string") {
    return { type: value.type, message: value.message };
  }
  return null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asError(value: unknown, operation: string): Error {
  return value instanceof Error ? value : new Error(`${operation}: ${String(value)}`);
}
