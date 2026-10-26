const statusElement = document.querySelector("#status");
const logElement = document.querySelector("#log");
const helloButton = document.querySelector("#hello");
const pingButton = document.querySelector("#ping");
const clearButton = document.querySelector("#clear");

let connected = false;
let nextRequestID = 1;
const pending = new Map();

class Channel {
  constructor(handler) {
    this.nextIndex = 0;
    this.pending = [];
    this.endIndex = undefined;
    this.id = window.__TAURI_INTERNALS__.transformCallback((event) => {
      if ("end" in event) {
        this.endIndex = event.index;
        this.cleanupIfFinished();
        return;
      }
      if (event.index === this.nextIndex) {
        handler(event.message);
        this.nextIndex += 1;
        while (this.nextIndex in this.pending) {
          handler(this.pending[this.nextIndex]);
          delete this.pending[this.nextIndex];
          this.nextIndex += 1;
        }
        this.cleanupIfFinished();
      } else {
        this.pending[event.index] = event.message;
      }
    });
  }

  cleanupIfFinished() {
    if (this.nextIndex === this.endIndex) {
      window.__TAURI_INTERNALS__.unregisterCallback(this.id);
    }
  }

  __TAURI_TO_IPC_KEY__() {
    return `__CHANNEL__:${this.id}`;
  }
}

function invoke(command, payload) {
  return window.__TAURI_INTERNALS__.invoke(command, payload);
}

function writeLog(direction, value, error = false) {
  const item = document.createElement("li");
  item.className = error ? "error" : direction;
  item.textContent = `${new Date().toLocaleTimeString()} ${direction}: ${value}`;
  logElement.append(item);
  logElement.scrollTop = logElement.scrollHeight;
}

function setConnected(value) {
  connected = value;
  statusElement.textContent = value ? "Connected" : "Disconnected";
  statusElement.classList.toggle("disconnected", !value);
  helloButton.disabled = !value;
  pingButton.disabled = !value;
  writeLog("status", statusElement.textContent);

  if (!value) {
    for (const request of pending.values()) {
      request.reject(new Error("Admin API disconnected"));
    }
    pending.clear();
  }
}

function receive(rawMessage) {
  writeLog("receive", rawMessage);
  let message;
  try {
    message = JSON.parse(rawMessage);
  } catch {
    writeLog("error", "Rust received invalid JSON from the daemon", true);
    return;
  }
  if (message?.version !== "v1" || message.kind !== "response" || typeof message.id !== "string") {
    return;
  }
  const request = pending.get(message.id);
  if (!request) return;
  pending.delete(message.id);
  if (message.error) {
    request.reject(new Error(`${message.error.code}: ${message.error.message}`));
  } else {
    request.resolve(message.result);
  }
}

function handleNativeEvent(event) {
  if (event?.type === "connection_status" && typeof event.connected === "boolean") {
    setConnected(event.connected);
  } else if (event?.type === "message" && typeof event.message === "string") {
    receive(event.message);
  }
}

function request(surface, method, payload = {}) {
  if (!connected) return Promise.reject(new Error("Admin API is disconnected"));
  const id = String(nextRequestID++);
  const message = JSON.stringify({ version: "v1", kind: "request", id, surface, method, payload });
  writeLog("send", message);
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    invoke("send_admin_message", { message }).catch((error) => {
      pending.delete(id);
      reject(new Error(String(error)));
    });
  });
}

async function hello() {
  try {
    const result = await request("admin", "hello", { client_name: "almagest-app", version: "v1" });
    writeLog("result", `hello: daemon API ${result?.version ?? "unknown"}`);
  } catch (error) {
    writeLog("error", `hello failed: ${error.message}`, true);
  }
}

async function ping() {
  const nonce = String(Date.now());
  try {
    const result = await request("admin", "ping", { nonce });
    writeLog("result", result?.nonce === nonce ? `ping ${nonce} returned` : "ping returned a different nonce");
  } catch (error) {
    writeLog("error", `ping failed: ${error.message}`, true);
  }
}

helloButton.addEventListener("click", hello);
pingButton.addEventListener("click", ping);
clearButton.addEventListener("click", () => logElement.replaceChildren());

const channel = new Channel(handleNativeEvent);
invoke("listen_admin", { channel }).catch((error) => {
  writeLog("error", `cannot open admin message channel: ${String(error)}`, true);
});
