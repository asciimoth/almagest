// Keep this dependency-free. Only the two admin message operations can cross
// from the untrusted frontend into the native process.
window.__TAURI_ISOLATION_HOOK__ = (request) => {
  const payload = request?.payload;
  if (request?.cmd === "listen_admin" && isPlainObject(payload)) {
    const keys = Object.keys(payload);
    if (keys.length === 1 && keys[0] === "channel" && /^__CHANNEL__:\d+$/.test(payload.channel)) {
      return request;
    }
  }
  if (request?.cmd === "send_admin_message" && isPlainObject(payload)) {
    const keys = Object.keys(payload);
    if (keys.length === 1 && keys[0] === "message" && isAPIRequest(payload.message)) {
      return request;
    }
  }
  throw new Error("blocked IPC operation");
};

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isAPIRequest(text) {
  const maxMessageBytes = Math.ceil((32 * 1024 * 1024) / 3) * 4 + 1024 * 1024;
  if (typeof text !== "string" || text.length > maxMessageBytes) return false;
  try {
    const message = JSON.parse(text);
    return isPlainObject(message)
      && message.version === "v1"
      && message.kind === "request"
      && isNonEmptyString(message.id)
      && isNonEmptyString(message.surface)
      && isNonEmptyString(message.method);
  } catch {
    return false;
  }
}

function isNonEmptyString(value) {
  return typeof value === "string" && value.length > 0;
}
