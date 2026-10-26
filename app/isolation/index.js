// Keep this dependency-free. Validate each new IPC message here before it can
// cross from the frontend into the native process.
window.__TAURI_ISOLATION_HOOK__ = (payload) => payload;
