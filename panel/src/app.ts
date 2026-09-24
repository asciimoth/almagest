import {
  applyNotification,
  applyWorkspaceSnapshot,
  applyWorkerNotification,
  DEFAULT_LINK_COLOR,
  DEFAULT_NODE_TEXT_COLOR,
  DEFAULT_NODE_TITLE_COLOR,
  emptySnapshot,
  formatPosition,
  isPastaClipboard,
  linkColor,
  latestPriorityPopup,
  nodeClassByName,
  nodeMenuID,
  nodePositionMap,
  parseEngineNotificationEvent,
  parsePosition,
  parseTypePaletteMap,
  parseWorkspaceSnapshotResponse,
  positionClipboardAt,
  selectedNodeIDs,
  isKeyboardShortcut,
  setTypeColors,
  trimPopupText,
  typeColor,
  type ID,
  type EngineNotificationEvent,
  type LinkSnapshot,
  type NodeClassSnapshot,
  type NodePopup,
  type NodeSnapshot,
  type PortSnapshot,
  type TypePalette,
  type WorkerSnapshot,
  type WorkspaceNotification,
  type WorkspaceSnapshot,
  type WorkspaceSnapshotUpdate,
} from "./pasta_model.js";
import {
  createDNSCacheTitle,
  createDNSKeyTitle,
  createDNSValuePreview,
  createClassButtonContent,
  createEmptyLogItem,
  createEmptyWorkerItem,
  createKVEntryTitle,
  createKVStoreTitle,
  createKVValuePreview,
  createLogItem,
  createNodeNameForm,
  createNotificationToast,
  createSecretTitle,
  createSecretValuePreview,
  createTLSCertPreview,
  createTLSCertTitle,
  createTLSPolicyPreview,
  createTLSPolicyTitle,
  createWorkerNodeBlock,
  escapeHTMLText,
  formatKVValueMeta,
  formatSecretEntryMeta,
  renderNodeDescription,
  safeCSSColor,
  shortClass,
  textElement,
  updateWorkerAgeElements,
  type WorkerNodeBlock,
  type ClassPreviewPort,
} from "./safe_dom.js";
import {
  DNS_JSON_RENDER_LIMIT,
  DNS_RECORD_CLASSES,
  DNS_RECORD_TYPES,
  applyDNSCacheChange,
  applyDNSKeyChange,
  defaultDNSRecordInput,
  desiredDNSKeySubscriptionKeys,
  dnsCacheNodeKey,
  editableDNSRecordFromValue,
  formatDNSCacheKey,
  parseDNSCacheNodeKey,
  sortDNSCaches,
  type DNSCacheChange,
  type DNSCacheInfo,
  type DNSKeyInfo,
  type DNSKeysSnapshot,
  type DNSRecordInput,
  type DNSValue,
  type DNSValueSnapshot,
} from "./dns_model.js";
import {
  KV_TEXT_RENDER_LIMIT,
  childKVNodeKey,
  desiredKVEntrySubscriptionKeys,
  kvNodeDescendsFrom,
  kvNodeKey,
  normalizeKVStore,
  parseKVNodeKey,
  sameKVStore,
  takePendingKVSnapshot,
  type KVEntriesSnapshot,
  type KVEntryInfo,
  type KVStoreInfo,
  type KVValue,
  type KVValueSnapshot,
} from "./kv_model.js";
import {
  SNAPSHOT_ARCHIVE_MAX_BYTES,
  removeSnapshot as removeSnapshotEntry,
  renderSnapshotList,
  upsertSnapshot,
  type SnapshotInfo,
} from "./snapshots_panel.js";
import {
  FORMULAR_SECRET_SELECTOR,
  SECRET_TEXT_RENDER_LIMIT,
  applySecretChange,
  normalizeSecretNodes,
  parseSecretNodeText,
  replaceSecretNodeTextToken,
  secretNamesForNode,
  secretNodesText,
  secretNodeTextSuggestions,
  sortSecrets,
  type SecretChange,
  type SecretEntry,
  type SecretsSnapshot,
} from "./secrets_model.js";
import {
  TLS_POLICY_JSON_ROWS,
  applyTLSChange,
  emptyTLSPolicySpec,
  formatTLSCertMeta,
  formatTLSPolicyMeta,
  formatTLSPolicySpec,
  isBuiltinTLSCertName,
  isImmutableTLSCertName,
  isMutableTLSCertName,
  isMutableTLSPolicyName,
  parseTLSPolicySpec,
  sortTLSCerts,
  sortTLSPolicies,
  type TLSBuiltinCAInfo,
  type TLSBuiltinCAOptions,
  type TLSCertEntry,
  type TLSChange,
  type TLSDNSRule,
  type TLSItemKind,
  type TLSAuthorityConstraint,
  type TLSPin,
  type TLSPolicyEntry,
  type TLSPolicySpec,
  type TLSScope,
  type TLSSnapshot,
  type TLSTrustAnchor,
} from "./tls_model.js";
import { errorMessage, parseAPIMessage, type APIMessage, type Surface } from "./api_model.js";
import {
  parseDNSCacheEvent,
  parseDNSKeyEvent,
  parseDNSValueEvent,
  parseKVEntriesEvent,
  parseKVStoresEvent,
  parseKVValueEvent,
  parseKVValueSnapshot,
  parseLogEntry,
  parseProjectVersionStatus,
  parseSecretsEvent,
  parseTLSEvent,
  type LogEntry,
  type ProjectVersionStatus,
} from "./event_model.js";
import {
  parseConfigSnapshot,
  parseConfigUpdateResponse,
  parseDNSSetResponse,
  parseDNSSubscribeCachesResponse,
  parseDNSSubscribeKeysResponse,
  parseDNSSubscribeValueResponse,
  parseDataResponse,
  parseDescriptionResponse,
  parseHelloResponse,
  parseIDResponse,
  parseIDsResponse,
  parseKVSubscribeEntriesResponse,
  parseKVSubscribeStoresResponse,
  parseKVSubscribeValueResponse,
  parseSecretEntryResponse,
  parseSecretsSubscribeResponse,
  parseSnapshotExportResponse,
  parseSnapshotInfoResponse,
  parseSnapshotRestoreResponse,
  parseSnapshotsListResponse,
  parseSubscribeLogsResponse,
  parseSubscriptionIDResponse,
  parseTLSBuiltinCAResponse,
  parseTLSCertResponse,
  parseTLSPolicyResponse,
  parseTLSSnapshotResponse,
  parseTLSSubscribeResponse,
  type ConfigSnapshot,
  type ConfigUpdateResponse,
  type DNSCachesSnapshot,
  type DNSSetResponse,
  type DNSSubscribeCachesResponse,
  type DNSSubscribeKeysResponse,
  type DNSSubscribeValueResponse,
  type HelloResponse,
  type KVStoresSnapshot,
  type KVSubscribeEntriesResponse,
  type KVSubscribeStoresResponse,
  type KVSubscribeValueResponse,
  type SecretsSubscribeResponse,
  type SnapshotExportResponse,
  type SnapshotRestoreResponse,
  type SnapshotsListResponse,
  type SubscribeLogsResponse,
  type TLSSubscribeResponse,
} from "./response_model.js";

const SECRET_NODE_AUTOCOMPLETE_LIMIT = 8;
const TLS_CERT_OPENSSL_COMMAND = [
  "openssl req -x509 -newkey rsa:4096",
  "-keyout tls.key -out tls.crt",
  "-sha256 -days 365 -nodes",
  '-subj "/CN=example.test"',
].join(" ");
const TLS_BUILTIN_CA_ALGORITHMS = ["ecdsa", "ed25519", "rsa"] as const;
const TLS_BUILTIN_CA_CURVES = ["P256", "P384", "P521"] as const;
const TLS_AUTHORITY_MATCHES = ["certificate", "spki"] as const;
const TLS_PIN_KINDS = ["spki", "certificate"] as const;
const TLS_IMMUTABLE_CERT_GROUP_KEY = "immutable";
const TLS_USER_CERT_GROUP_KEY = "user";
let secNodeAutocompleteID = 0;
let tlsPolicyEditorID = 0;

declare const LiteGraph: any;
declare const LGraph: any;
declare const LGraphCanvas: any;
type FormularMenuInstance = {
  feed(message: unknown): boolean;
  destroy(): void;
};
type FormularSelectorProvider = (selector: string) => readonly string[] | null | undefined;
type FormularMenuConstructor = new (
  container: string | HTMLElement,
  menuID: string,
  send: (message: unknown) => void,
  options?: { selectors?: FormularSelectorProvider },
) => FormularMenuInstance;
// The built module runs from public/build, while vendored browser dependencies
// are served from vendor at the panel root. The build adds a content version to
// this module URL. Pass it to the dependency URL so a panel update cannot reuse
// an older Formular renderer from the browser cache.
const panelModuleURL = new URL(import.meta.url);
const formularMenuURL = new URL("../../vendor/formular-menu/formular-menu.js", panelModuleURL);
formularMenuURL.search = panelModuleURL.search;
const { FormularMenu } = (await import(formularMenuURL.href)) as {
  FormularMenu: FormularMenuConstructor;
};

type KVNoticeLevel = "info" | "error";
type KVNotice = {
  level: KVNoticeLevel;
  text: string;
};
type DNSNotice = KVNotice;
type SecNotice = KVNotice;
type TLSNotice = KVNotice;
type ConfigChanges = {
  added?: string[];
  removed?: string[];
  reinitialized?: string[];
  positions_updated?: string[];
  primary_types_updated?: string[];
  links_added?: string[];
  links_removed?: string[];
};
type DNSValuePanelState = {
  cache: string;
  key: string;
  subscriptionID: number;
  snapshot: DNSValueSnapshot | null;
  editing: boolean;
};
type DNSOpenForm = {
  cache: string;
};
type KVOpenForm = {
  nodeKey: string;
  mode: "value" | "subtree";
};
type KVValuePanelState = {
  store: KVStoreInfo;
  path: string[];
  key: string;
  subscriptionID: number;
  snapshot: KVValueSnapshot | null;
  revealHidden: boolean;
};
type SecValuePanelState = {
  key: string;
  reveal: boolean;
  editing: "value" | "nodes" | "rename" | null;
};
type TLSOpenForm = {
  kind: TLSItemKind;
};
type TLSValuePanelState = {
  kind: TLSItemKind;
  name: string;
  editing: "value" | "rename" | null;
};
type GraphViewportState = {
  scale: number;
  offset: [number, number];
};
type PendingNodePlacement = {
  className: string;
  position: string;
  id?: number;
};

type BackendResultMap = {
  hello: HelloResponse;
  colormap: Record<string, TypePalette>;
  configSnapshot: ConfigSnapshot;
  configUpdate: ConfigUpdateResponse;
  snapshotsList: SnapshotsListResponse;
  snapshotCreate: SnapshotInfo;
  snapshotExport: SnapshotExportResponse;
  snapshotImport: SnapshotInfo;
  snapshotDelete: unknown;
  snapshotRename: SnapshotInfo;
  snapshotRestore: SnapshotRestoreResponse;
  subscribeNotifications: { subscription_id: number };
  subscribeLogs: SubscribeLogsResponse;
  dnsSubscribeCaches: DNSSubscribeCachesResponse;
  dnsSubscribeKeys: DNSSubscribeKeysResponse;
  dnsSubscribeValue: DNSSubscribeValueResponse;
  dnsUnsubscribe: unknown;
  dnsSet: DNSSetResponse;
  dnsDelete: unknown;
  dnsDrop: unknown;
  tlsSubscribe: TLSSubscribeResponse;
  tlsUnsubscribe: unknown;
  tlsBuiltinCA: TLSBuiltinCAInfo;
  tlsSetBuiltinCA: TLSBuiltinCAInfo;
  tlsReloadOSCerts: TLSSnapshot;
  tlsSetCert: TLSCertEntry;
  tlsRenameCert: TLSCertEntry;
  tlsDeleteCert: unknown;
  tlsSetPolicy: TLSPolicyEntry;
  tlsRenamePolicy: TLSPolicyEntry;
  tlsDeletePolicy: unknown;
  secSubscribe: SecretsSubscribeResponse;
  secUnsubscribe: unknown;
  secSetValue: SecretEntry;
  secSetNodes: SecretEntry;
  secRename: SecretEntry;
  secDelete: unknown;
  kvSubscribeStores: KVSubscribeStoresResponse;
  kvSubscribeEntries: KVSubscribeEntriesResponse;
  kvSubscribeValue: KVSubscribeValueResponse;
  kvUnsubscribe: unknown;
  kvEnsure: unknown;
  kvSet: KVValueSnapshot;
  kvDelete: unknown;
  kvDrop: unknown;
  snapshot: WorkspaceSnapshotUpdate;
  classDescription: string;
  addNode: number;
  addLink: number;
  removeLink: unknown;
  setNodePosition: unknown;
  moveNodes: unknown;
  setNodeName: boolean;
  removeNodePopup: unknown;
  removeNodePopups: unknown;
  removeNodes: Record<string, never>;
  triggerNode: unknown;
  copy: string;
  paste: number[];
  undo: unknown;
  redo: unknown;
  subscribeNodeMenu: unknown;
  unsubscribeNodeMenu: unknown;
  formular: unknown;
};

type BackendMethod = keyof BackendResultMap;
type SecMutationMethod = "secSetValue" | "secSetNodes" | "secRename" | "secDelete";
type DNSMutationMethod = "dnsSet" | "dnsDelete" | "dnsDrop";
type TLSMutationMethod =
  | "tlsSetBuiltinCA"
  | "tlsSetCert"
  | "tlsRenameCert"
  | "tlsDeleteCert"
  | "tlsSetPolicy"
  | "tlsRenamePolicy"
  | "tlsDeletePolicy";
type KVMutationMethod = "kvEnsure" | "kvSet" | "kvDelete" | "kvDrop";

declare global {
  interface Window {
    __almagestPanel?: {
      call<M extends BackendMethod>(
        method: M,
        params?: Record<string, unknown>,
      ): Promise<BackendResultMap[M] | null>;
      snapshot(): WorkspaceSnapshot;
      selectNode(id: number): void;
      selectNodes(ids: number[]): void;
      workers(): Record<string, WorkerSnapshot>;
      renameNode(id: number, name: string): void;
      moveNode(id: number, x: number, y: number): void;
      canConnect(from: number, to: number): boolean;
      nodeScreenPosition(id: number): { x: number; y: number } | null;
      portScreenPosition(port: number): { x: number; y: number } | null;
      highlightedInputPort(): number | null;
    };
  }
}

let snapshot: WorkspaceSnapshot = emptySnapshot();
let graph: any;
let canvas: any;
let rendering = false;
let selectedMenu: { node: ID; menu: FormularMenuInstance } | null = null;
let activeTab = "graph";
let sidekickUpdate = 0;
let workerAgeTimer = 0;
let graphResizeObserver: ResizeObserver | null = null;
let localClipboard = "";
let graphShortcutsArmed = false;
let graphPointerInside = false;
let lastGraphPointer: { clientX: number; clientY: number } | null = null;
let lastPastePlacement: { key: string; count: number } | null = null;
let nativePasteEvents = 0;
let pasteShortcutActive = false;
let pasteShortcutConsumed = false;
let socket: WebSocket | null = null;
let nextRequestID = 1;
let notificationSubscriptionID = 0;
const frontendLogs: LogEntry[] = [];
let secSubscriptionID = 0;
let secSubscribePending = false;
let secScrollTop = 0;
let secNotice: SecNotice | null = null;
let secOpenForm = false;
let secValuePanel: SecValuePanelState | null = null;
const secrets: SecretEntry[] = [];
let dnsCacheSubscriptionID = 0;
let dnsCacheSubscribePending = false;
let dnsScrollTop = 0;
let dnsNotice: DNSNotice | null = null;
let dnsValuePanel: DNSValuePanelState | null = null;
let dnsOpenForm: DNSOpenForm | null = null;
let dnsValueSubscribePending = false;
const dnsCaches: DNSCacheInfo[] = [];
const dnsExpandedCaches = new Set<string>();
const dnsKeysByCache = new Map<string, DNSKeysSnapshot>();
const dnsKeySubscriptions = new Map<string, number>();
const dnsKeySubscribePending = new Set<string>();
let tlsSubscriptionID = 0;
let tlsSubscribePending = false;
let tlsReloadOSCertsPending = false;
let tlsScrollTop = 0;
let tlsNotice: TLSNotice | null = null;
let tlsOpenForm: TLSOpenForm | null = null;
let tlsValuePanel: TLSValuePanelState | null = null;
let tlsSnapshot: TLSSnapshot = { certs: [], policies: [] };
const tlsExpandedCertGroups = new Set<string>([TLS_USER_CERT_GROUP_KEY]);
let kvStoreSubscriptionID = 0;
let kvStoreSubscribePending = false;
let kvScrollTop = 0;
let kvNotice: KVNotice | null = null;
let kvOpenForm: KVOpenForm | null = null;
let kvValuePanel: KVValuePanelState | null = null;
let kvValueSubscribePending = false;
const kvStores: KVStoreInfo[] = [];
const kvExpandedNodes = new Set<string>();
const kvEntriesByNode = new Map<string, KVEntriesSnapshot>();
const kvEntrySubscriptions = new Map<string, number>();
const kvEntrySubscribePending = new Set<string>();
const kvPendingStoreSnapshots = new Map<number, KVStoresSnapshot>();
const kvPendingEntrySnapshots = new Map<number, KVEntriesSnapshot>();
const kvPendingValueSnapshots = new Map<number, KVValueSnapshot>();
let nextToastID = 1;
let liteGraphDrawGuardInstalled = false;
let liteGraphWidgetSelectionGuardInstalled = false;
let logStoreLimit = 0;
const liteLinkToPasta = new Map<number, number>();
const pendingNodePlacements: PendingNodePlacement[] = [];
const CLASS_DRAG_MIME = "application/x-almagest-node-class";
const GRAPH_VIEWPORT_STORAGE_KEY = "almagest.panel.graph.viewport.v1";
const PASTA_TRIGGER_CLASS = "pasta/Trigger";
const debugClipboard = false;
let graphViewportSaveTimeout = 0;
let configSnapshot: ConfigSnapshot | null = null;
let configDraft = "";
let configLoading = false;
let configApplying = false;
let snapshotsLoading = false;
let snapshotMutationPending = false;
let snapshotEntries: SnapshotInfo[] = [];

const el = {
  status: document.querySelector<HTMLElement>("#backend-status")!,
  projectVersion: document.querySelector<HTMLElement>("#project-version")!,
  tabs: [...document.querySelectorAll<HTMLButtonElement>("[data-tab]")],
  panels: [...document.querySelectorAll<HTMLElement>("[data-panel]")],
  graphCanvas: document.querySelector<HTMLCanvasElement>("#graph-canvas")!,
  sidekick: document.querySelector<HTMLElement>("#sidekick")!,
  secPanel: document.querySelector<HTMLElement>("[data-panel='sec']")!,
  secTree: document.querySelector<HTMLElement>("#sec-tree")!,
  secValuePanel: document.querySelector<HTMLElement>("#sec-value-panel")!,
  dnsPanel: document.querySelector<HTMLElement>("[data-panel='dns']")!,
  dnsTree: document.querySelector<HTMLElement>("#dns-tree")!,
  dnsValuePanel: document.querySelector<HTMLElement>("#dns-value-panel")!,
  tlsPanel: document.querySelector<HTMLElement>("[data-panel='tls']")!,
  tlsTree: document.querySelector<HTMLElement>("#tls-tree")!,
  tlsValuePanel: document.querySelector<HTMLElement>("#tls-value-panel")!,
  kvPanel: document.querySelector<HTMLElement>("[data-panel='kv']")!,
  kvTree: document.querySelector<HTMLElement>("#kv-tree")!,
  kvValuePanel: document.querySelector<HTMLElement>("#kv-value-panel")!,
  psPanel: document.querySelector<HTMLElement>("[data-panel='ps']")!,
  ps: document.querySelector<HTMLElement>("#ps-list")!,
  logsPanel: document.querySelector<HTMLElement>("[data-panel='logs']")!,
  logs: document.querySelector<HTMLElement>("#logs-list")!,
  configEditor: document.querySelector<HTMLTextAreaElement>("#config-editor")!,
  configReload: document.querySelector<HTMLButtonElement>("#config-reload")!,
  configApply: document.querySelector<HTMLButtonElement>("#config-apply")!,
  configStatus: document.querySelector<HTMLElement>("#config-status")!,
  snapshotName: document.querySelector<HTMLInputElement>("#snapshot-name")!,
  snapshotCreate: document.querySelector<HTMLButtonElement>("#snapshot-create")!,
  snapshotImportFile: document.querySelector<HTMLInputElement>("#snapshot-import-file")!,
  snapshotImportName: document.querySelector<HTMLInputElement>("#snapshot-import-name")!,
  snapshotImport: document.querySelector<HTMLButtonElement>("#snapshot-import")!,
  snapshotsReload: document.querySelector<HTMLButtonElement>("#snapshots-reload")!,
  snapshotsList: document.querySelector<HTMLOListElement>("#snapshots-list")!,
  snapshotsStatus: document.querySelector<HTMLElement>("#snapshots-status")!,
  notifications: document.querySelector<HTMLElement>("#notifications"),
};

function log(source: string, level: string, text: string): void {
  appendLog({
    at: new Date().toISOString(),
    source,
    level,
    message: text,
  });
}

function debugLog(message: string, details: Record<string, unknown> = {}): void {
  if (!debugClipboard) return;
  console.log(`[almagest-panel] ${message}`, details);
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function readGraphViewportState(): GraphViewportState | null {
  try {
    const raw = window.localStorage.getItem(GRAPH_VIEWPORT_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { scale?: unknown; offset?: unknown };
    if (!isFiniteNumber(parsed.scale) || !Array.isArray(parsed.offset)) return null;
    const [x, y] = parsed.offset;
    if (!isFiniteNumber(x) || !isFiniteNumber(y)) return null;
    return { scale: parsed.scale, offset: [x, y] };
  } catch (error) {
    log("frontend", "warn", `graph viewport restore failed: ${errorMessage(error)}`);
    return null;
  }
}

function applyGraphViewportState(): void {
  if (!canvas?.ds) return;
  const state = readGraphViewportState();
  if (!state) return;
  const minScale = isFiniteNumber(canvas.ds.min_scale) ? canvas.ds.min_scale : 0.1;
  const maxScale = isFiniteNumber(canvas.ds.max_scale) ? canvas.ds.max_scale : 10;
  canvas.ds.scale = clamp(state.scale, minScale, maxScale);
  canvas.ds.offset[0] = state.offset[0];
  canvas.ds.offset[1] = state.offset[1];
  canvas.setDirty(true, true);
}

function currentGraphViewportState(): GraphViewportState | null {
  if (!canvas?.ds || !isFiniteNumber(canvas.ds.scale)) return null;
  const [x, y] = canvas.ds.offset ?? [];
  if (!isFiniteNumber(x) || !isFiniteNumber(y)) return null;
  return { scale: canvas.ds.scale, offset: [x, y] };
}

function saveGraphViewportState(): void {
  window.clearTimeout(graphViewportSaveTimeout);
  graphViewportSaveTimeout = 0;
  const state = currentGraphViewportState();
  if (!state) return;
  try {
    window.localStorage.setItem(GRAPH_VIEWPORT_STORAGE_KEY, JSON.stringify(state));
  } catch (error) {
    log("frontend", "warn", `graph viewport save failed: ${errorMessage(error)}`);
  }
}

function scheduleGraphViewportSave(): void {
  window.clearTimeout(graphViewportSaveTimeout);
  graphViewportSaveTimeout = window.setTimeout(saveGraphViewportState, 100);
}

const pendingRequests = new Map<string, {
  resolve(value: unknown): void;
  reject(error: Error): void;
}>();

function websocketURL(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/ws`;
}

function connectAPI(): Promise<void> {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(websocketURL());
    socket = ws;
    ws.addEventListener("open", () => resolve(), { once: true });
    ws.addEventListener("error", () => reject(new Error("WebSocket connection failed")), { once: true });
    ws.addEventListener("message", (event) => {
      const message = parseAPIMessage(String(event.data));
      if (message) handleAPIMessage(message);
      else log("api", "warn", "ignored an invalid WebSocket message");
    });
    ws.addEventListener("close", () => {
      el.status.textContent = "Admin API disconnected";
      el.status.style.color = "#ff0000";
      for (const { reject: rejectRequest } of pendingRequests.values()) {
        rejectRequest(new Error("Admin API disconnected"));
      }
      pendingRequests.clear();
    });
  });
}

function request(surface: Surface, method: string, payload: Record<string, unknown> = {}): Promise<unknown> {
  if (!socket || socket.readyState !== WebSocket.OPEN) {
    return Promise.reject(new Error("Admin API is not connected"));
  }
  const id = String(nextRequestID++);
  const msg: APIMessage = { version: "v1", kind: "request", id, surface, method, payload };
  return new Promise<unknown>((resolve, reject) => {
    pendingRequests.set(id, { resolve, reject });
    socket?.send(JSON.stringify(msg));
  });
}

async function requestDecoded<T>(
  surface: Surface,
  method: string,
  payload: Record<string, unknown>,
  parse: (input: unknown) => T | null,
): Promise<T> {
  const result = await request(surface, method, payload);
  const parsed = parse(result);
  if (parsed === null) throw new Error(`${surface} ${method} returned an invalid result`);
  return parsed;
}

function handleAPIMessage(msg: APIMessage): void {
  if (msg.kind === "response" && msg.id) {
    const pending = pendingRequests.get(msg.id);
    if (!pending) return;
    pendingRequests.delete(msg.id);
    if (msg.error) pending.reject(new Error(`${msg.error.code}: ${msg.error.message}`));
    else pending.resolve(msg.result);
    return;
  }
  if (msg.kind === "event" && msg.surface === "engine" && msg.method === "notification") {
    const event = parseEngineNotificationEvent(msg.payload);
    if (event) handleEngineNotification(event);
    else log("api", "warn", "ignored an invalid engine notification");
    return;
  }
  if (msg.kind === "event" && msg.surface === "admin" && msg.method === "project_version_status") {
    const status = parseProjectVersionStatus(msg.payload);
    if (status) applyProjectVersionStatus(status);
    return;
  }
  if (msg.kind === "event" && msg.surface === "log" && msg.method === "entry") {
    const entry = parseLogEntry(msg.payload);
    if (entry) appendLog(entry);
    return;
  }
  if (msg.kind === "event" && msg.surface === "kv" && msg.method) {
    handleKVEvent(msg.method, msg.payload);
    return;
  }
  if (msg.kind === "event" && msg.surface === "dns" && msg.method) {
    handleDNSEvent(msg.method, msg.payload);
    return;
  }
  if (msg.kind === "event" && msg.surface === "tls" && msg.method) {
    handleTLSEvent(msg.method, msg.payload);
    return;
  }
  if (msg.kind === "event" && msg.surface === "secrets" && msg.method) {
    handleSecEvent(msg.method, msg.payload);
  }
}

function applyProjectVersionStatus(status: ProjectVersionStatus): void {
  el.projectVersion.hidden = !status.version;
  el.projectVersion.textContent = status.version;
  el.projectVersion.dataset.highlight = status.highlight;
  if (status.hover_message) {
    el.projectVersion.title = status.hover_message;
  } else {
    el.projectVersion.removeAttribute("title");
  }
}

function handleEngineNotification(event: EngineNotificationEvent): void {
  const notification = event.notification;
  if (notification.kind === "colormap" && notification.colormap) {
    applyTypePalette(notification.colormap);
    renderGraph();
    renderWorkers();
    updateWorkerAgeTimer();
    updateSidekick();
    return;
  }
  if (notification.kind === "worker" && notification.workspace_id === "main" && notification.worker_notification) {
    snapshot = applyWorkerNotification(snapshot, notification.worker_notification);
    renderWorkers();
    updateWorkerAgeTimer();
    return;
  }
  if (notification.kind !== "workspace" || notification.workspace_id !== "main" || !notification.workspace_notification) {
    return;
  }
  const workspaceNotification = notification.workspace_notification;
  if (workspaceNotification.kind === "node_menu") {
    feedMenu(workspaceNotification);
  } else {
    snapshot = applyNotification(snapshot, applyPendingNodePlacement(workspaceNotification));
    renderGraph();
    renderWorkers();
    updateWorkerAgeTimer();
  }
}

function applyPendingNodePlacement(notification: WorkspaceNotification): WorkspaceNotification {
  if (!pendingNodePlacements.length) return notification;

  // Temporary panel-side bridge for the current two-step create flow:
  // add_node emits node_added before set_node_position can run, so LiteGraph
  // renders the new node at its fallback coordinates and then visibly jumps.
  // Until the backend can create a node with its initial position atomically,
  // patch local snapshots for matching in-flight nodes with the requested
  // position. The real set_node_position notification later clears this entry.
  if (notification.node && notification.id !== undefined) {
    const node = patchPendingNodePlacement(notification.id, notification.node);
    if (node === notification.node) return notification;
    return { ...notification, node };
  }

  if (notification.nodes) {
    const nodes = patchPendingNodePlacementMap(notification.nodes);
    if (nodes === notification.nodes) return notification;
    return { ...notification, nodes };
  }

  if (notification.snapshot?.nodes) {
    const nodes = patchPendingNodePlacementMap(notification.snapshot.nodes);
    if (nodes === notification.snapshot.nodes) return notification;
    return { ...notification, snapshot: { ...notification.snapshot, nodes } };
  }

  return notification;
}

function patchPendingNodePlacementMap(nodes: Record<string, NodeSnapshot>): Record<string, NodeSnapshot> {
  let next: Record<string, NodeSnapshot> | null = null;
  for (const [idText, node] of Object.entries(nodes)) {
    const id = Number(idText);
    if (!Number.isFinite(id)) continue;
    const patched = patchPendingNodePlacement(id, node);
    if (patched === node) continue;
    next ??= { ...nodes };
    next[idText] = patched;
  }
  return next ?? nodes;
}

function patchPendingNodePlacement(id: number, node: NodeSnapshot): NodeSnapshot {
  const placement = pendingPlacementForNode(id, node);
  if (!placement) return node;
  placement.id = id;

  if (node.position === placement.position) {
    forgetPendingNodePlacement(placement);
    return node;
  }
  if (hasValidPanelPosition(node.position)) return node;

  // This intentionally edits only the panel's incoming snapshot copy. Backend
  // state still comes from set_node_position, so undo/save semantics remain the
  // current two-mutation behavior while the visible graph avoids the fallback
  // spawn position.
  return { ...node, position: placement.position };
}

function pendingPlacementForNode(id: number, node: NodeSnapshot): PendingNodePlacement | null {
  const byID = pendingNodePlacements.find((placement) => placement.id === id);
  if (byID) return byID;
  if (snapshot.nodes[String(id)]) return null;
  return pendingNodePlacements.find((placement) => placement.id === undefined && placement.className === node.class) ?? null;
}

function hasValidPanelPosition(raw: string): boolean {
  if (!raw) return false;
  try {
    const position = JSON.parse(raw) as { x?: unknown; y?: unknown };
    return isFiniteNumber(position.x) && isFiniteNumber(position.y);
  } catch {
    return false;
  }
}

function forgetPendingNodePlacement(placement: PendingNodePlacement): void {
  const index = pendingNodePlacements.indexOf(placement);
  if (index >= 0) pendingNodePlacements.splice(index, 1);
}

function applyTypePalette(colormap: Record<string, TypePalette>): void {
  const colors: Record<string, { color: string; textColor: string }> = {};
  for (const [type, palette] of Object.entries(colormap)) {
    const color = palette.type ?? palette.color;
    const textColor = palette.text ?? palette.textColor ?? "#ffffff";
    if (color) colors[type] = { color, textColor };
  }
  setTypeColors(colors);
  installLiteGraphTypeColors();
}

function backend<M extends BackendMethod>(
  method: M,
  params?: Record<string, unknown>,
): Promise<BackendResultMap[M]>;
async function backend(method: BackendMethod, params: Record<string, unknown> = {}): Promise<unknown> {
  switch (method) {
    case "hello":
      return requestDecoded(
        "admin",
        "hello",
        { client_name: "almagest-panel", version: "v1" },
        parseHelloResponse,
      );
    case "colormap": {
      return requestDecoded("admin", "colormap", {}, parseTypePaletteMap);
    }
    case "configSnapshot":
      return requestDecoded("config", "snapshot", {}, parseConfigSnapshot);
    case "configUpdate":
      return requestDecoded(
        "config",
        "update",
        { text: params.text, revision: params.revision },
        parseConfigUpdateResponse,
      );
    case "snapshotsList":
      return requestDecoded("snapshots", "list", {}, parseSnapshotsListResponse);
    case "snapshotCreate":
      return requestDecoded("snapshots", "create", { name: params.name }, parseSnapshotInfoResponse);
    case "snapshotExport":
      return requestDecoded("snapshots", "export", { uuid: params.uuid }, parseSnapshotExportResponse);
    case "snapshotImport": {
      const payload: { archive: unknown; name?: string } = { archive: params.archive };
      if (typeof params.name === "string") payload.name = params.name;
      return requestDecoded("snapshots", "import", payload, parseSnapshotInfoResponse);
    }
    case "snapshotDelete":
      return request("snapshots", "delete", { uuid: params.uuid });
    case "snapshotRename":
      return requestDecoded(
        "snapshots",
        "rename",
        { uuid: params.uuid, name: params.name },
        parseSnapshotInfoResponse,
      );
    case "snapshotRestore":
      return requestDecoded(
        "snapshots",
        "restore",
        { uuid: params.uuid },
        parseSnapshotRestoreResponse,
      );
    case "subscribeNotifications": {
      const result = await requestDecoded(
        "engine",
        "subscribe_notifications",
        { workspace: "main" },
        parseSubscriptionIDResponse,
      );
      notificationSubscriptionID = result.subscription_id;
      return result;
    }
    case "subscribeLogs": {
      const result = await requestDecoded("log", "subscribe", {}, parseSubscribeLogsResponse);
      logStoreLimit = Math.max(result.cache_size * 2, result.entries.length);
      frontendLogs.splice(0, frontendLogs.length, ...result.entries);
      renderLogs();
      return result;
    }
    case "dnsSubscribeCaches":
      return requestDecoded("dns", "subscribe_caches", {}, parseDNSSubscribeCachesResponse);
    case "dnsSubscribeKeys":
      return requestDecoded(
        "dns",
        "subscribe_keys",
        { cache: params.cache },
        parseDNSSubscribeKeysResponse,
      );
    case "dnsSubscribeValue":
      return requestDecoded(
        "dns",
        "subscribe_value",
        { cache: params.cache, key: params.key },
        parseDNSSubscribeValueResponse,
      );
    case "dnsUnsubscribe":
      return request("dns", "unsubscribe", { subscription_id: params.subscriptionID });
    case "dnsSet":
      return requestDecoded(
        "dns",
        "set",
        { cache: params.cache, key: params.key, record: params.record },
        parseDNSSetResponse,
      );
    case "dnsDelete":
      return request("dns", "delete", { cache: params.cache, key: params.key });
    case "dnsDrop":
      return request("dns", "drop", { cache: params.cache });
    case "tlsSubscribe":
      return requestDecoded("tls", "subscribe", {}, parseTLSSubscribeResponse);
    case "tlsUnsubscribe":
      return request("tls", "unsubscribe", { subscription_id: params.subscriptionID });
    case "tlsBuiltinCA":
      return requestDecoded("tls", "builtin_ca", {}, parseTLSBuiltinCAResponse);
    case "tlsSetBuiltinCA":
      return requestDecoded(
        "tls",
        "set_builtin_ca_options",
        { options: params.options },
        parseTLSBuiltinCAResponse,
      );
    case "tlsReloadOSCerts":
      return requestDecoded("tls", "reload_os_certs", {}, parseTLSSnapshotResponse);
    case "tlsSetCert":
      return requestDecoded(
        "tls",
        "set_cert",
        { name: params.name, pem: params.pem },
        parseTLSCertResponse,
      );
    case "tlsRenameCert":
      return requestDecoded(
        "tls",
        "rename_cert",
        { name: params.name, new_name: params.newName },
        parseTLSCertResponse,
      );
    case "tlsDeleteCert":
      return request("tls", "delete_cert", { name: params.name });
    case "tlsSetPolicy":
      return requestDecoded(
        "tls",
        "set_policy",
        { name: params.name, spec: params.spec },
        parseTLSPolicyResponse,
      );
    case "tlsRenamePolicy":
      return requestDecoded(
        "tls",
        "rename_policy",
        { name: params.name, new_name: params.newName },
        parseTLSPolicyResponse,
      );
    case "tlsDeletePolicy":
      return request("tls", "delete_policy", { name: params.name });
    case "secSubscribe":
      return requestDecoded("secrets", "subscribe", {}, parseSecretsSubscribeResponse);
    case "secUnsubscribe":
      return request("secrets", "unsubscribe", { subscription_id: params.subscriptionID });
    case "secSetValue":
      return requestDecoded(
        "secrets",
        "set_value",
        { key: params.key, value: params.value },
        parseSecretEntryResponse,
      );
    case "secSetNodes":
      return requestDecoded(
        "secrets",
        "set_nodes",
        { key: params.key, nodes: params.nodes },
        parseSecretEntryResponse,
      );
    case "secRename":
      return requestDecoded(
        "secrets",
        "rename",
        { key: params.key, new_key: params.newKey },
        parseSecretEntryResponse,
      );
    case "secDelete":
      return request("secrets", "delete", { key: params.key });
    case "kvSubscribeStores":
      return requestDecoded("kv", "subscribe_stores", {}, parseKVSubscribeStoresResponse);
    case "kvSubscribeEntries":
      return requestDecoded(
        "kv",
        "subscribe_entries",
        { store: params.store, path: params.path },
        parseKVSubscribeEntriesResponse,
      );
    case "kvSubscribeValue":
      return requestDecoded(
        "kv",
        "subscribe_value",
        { store: params.store, path: params.path, key: params.key },
        parseKVSubscribeValueResponse,
      );
    case "kvUnsubscribe":
      return request("kv", "unsubscribe", { subscription_id: params.subscriptionID });
    case "kvEnsure":
      return request("kv", "ensure", { store: params.store, path: params.path });
    case "kvSet":
      return requestDecoded(
        "kv",
        "set",
        { store: params.store, path: params.path, key: params.key, value: params.value },
        parseKVValueSnapshot,
      );
    case "kvDelete":
      return request("kv", "delete", { store: params.store, path: params.path, key: params.key });
    case "kvDrop":
      return request("kv", "drop", { store: params.store, path: params.path });
    case "snapshot": {
      const result = await request("engine", "workspace_snapshot", { workspace: "main" });
      const parsed = parseWorkspaceSnapshotResponse(result);
      if (!parsed) throw new Error("engine workspace_snapshot returned an invalid result");
      return parsed;
    }
    case "classDescription": {
      const result = await requestDecoded("engine", "node_class_long_description", {
        workspace: "main",
        class: params.class,
      }, parseDescriptionResponse);
      return result;
    }
    case "addNode": {
      const result = await requestDecoded(
        "engine",
        "add_node",
        { workspace: "main", class: params.class },
        parseIDResponse,
      );
      if (typeof params.position === "string") {
        await request("engine", "set_node_position", { workspace: "main", id: result, position: params.position });
      }
      return result;
    }
    case "addLink": {
      const result = await requestDecoded("engine", "add_link", {
        workspace: "main",
        port_a: params.from,
        port_b: params.to,
      }, parseIDResponse);
      return result;
    }
    case "removeLink":
      return request("engine", "remove_link", { workspace: "main", id: params.id });
    case "setNodePosition":
      return request("engine", "set_node_position", { workspace: "main", id: params.id, position: params.position });
    case "moveNodes":
      return request("engine", "move_nodes", { workspace: "main", positions: params.positions });
    case "setNodeName":
      await request("engine", "set_node_name", { workspace: "main", id: params.id, name: params.name });
      return true;
    case "removeNodePopup":
      return request("engine", "remove_node_popup", { workspace: "main", node: params.id, popup: params.popupId });
    case "removeNodePopups":
      return request("engine", "remove_node_popups", { workspace: "main", node: params.id });
    case "removeNodes": {
      const ids = (params.ids as number[] | undefined) ?? [];
      if (ids.length === 1) {
        await request("engine", "remove_node", { workspace: "main", id: ids[0] });
      } else if (ids.length > 1) {
        await request("engine", "remove_nodes", { workspace: "main", ids });
      }
      return {};
    }
    case "triggerNode":
      return request("engine", "trigger_node", { workspace: "main", id: params.id });
    case "copy": {
      const result = await requestDecoded(
        "engine",
        "copy",
        { workspace: "main", ids: params.ids },
        parseDataResponse,
      );
      return result;
    }
    case "paste": {
      const result = await requestDecoded(
        "engine",
        "paste",
        { workspace: "main", data: params.data },
        parseIDsResponse,
      );
      return result;
    }
    case "undo":
    case "redo":
      return request("engine", method, { workspace: "main" });
    case "subscribeNodeMenu":
      return request("engine", "subscribe_node_menu", {
        workspace: "main",
        node: params.id,
        subscription_id: notificationSubscriptionID,
      });
    case "unsubscribeNodeMenu":
      return request("engine", "unsubscribe_node_menu", {
        workspace: "main",
        node: params.id,
        subscription_id: notificationSubscriptionID,
      });
    case "formular":
      return request("engine", "send_node_formular_msg", {
        workspace: "main",
        node: params.id,
        message: params.message,
      });
    default:
      throw new Error(`unknown frontend method ${method}`);
  }
}

async function tryBackend<M extends BackendMethod>(
  method: M,
  params: Record<string, unknown> = {},
): Promise<BackendResultMap[M] | null> {
  try {
    return await backend(method, params);
  } catch (error) {
    log("api", "warn", `${method}: ${errorMessage(error)}`);
    return null;
  }
}

function registerLiteGraphNode(): void {
  if (LiteGraph.registered_node_types?.["pasta/view"]) return;
  function PastaViewNode(this: any) {
    this.properties = { pastaId: 0 };
    this.resizable = false;
    this.clonable = false;
    this.removable = false;
  }
  PastaViewNode.title = "Pasta";
  PastaViewNode.prototype.onConnectInput = function (
    inputIndex: number,
    _outputType: string,
    outputSlot: { pastaPortId?: number },
    outputNode: any,
    outputIndex: number,
  ): boolean {
    if (rendering) return true;
    const from = outputSlot?.pastaPortId ?? outputNode.outputs?.[outputIndex]?.pastaPortId;
    const to = this.inputs?.[inputIndex]?.pastaPortId;
    if (from && to) tryBackend("addLink", { from, to });
    return false;
  };
  PastaViewNode.prototype.onConnectionsChange = function (
    _type: number,
    _slot: number,
    isConnected: boolean,
    link: { id?: number; data?: { pastaId?: number } },
  ): void {
    if (rendering || isConnected || !link) return;
    const id = link.data?.pastaId ?? (link.id ? liteLinkToPasta.get(link.id) : undefined);
    if (id) tryBackend("removeLink", { id });
  };
  PastaViewNode.prototype.onDrawForeground = function (ctx: CanvasRenderingContext2D): void {
    drawNodePopup(ctx, this);
    drawNodeLabel(ctx, this);
  };
  PastaViewNode.prototype.onSelected = scheduleSidekickUpdate;
  PastaViewNode.prototype.onDeselected = scheduleSidekickUpdate;
  LiteGraph.registerNodeType("pasta/view", PastaViewNode);
}

function initGraph(): void {
  // Keep the vendored script-node feature disabled even if another caller has
  // modified the mutable LiteGraph global before the panel starts. The CSP also
  // omits unsafe-eval as a second enforcement layer.
  LiteGraph.allow_scripts = false;
  installLiteGraphDrawGuard();
  installLiteGraphWidgetSelectionGuard();
  registerLiteGraphNode();
  graph = new LGraph();
  canvas = new LGraphCanvas("#graph-canvas", graph);
  canvas.node_title_color = DEFAULT_NODE_TEXT_COLOR;
  canvas.onNodeMoved = flushNodePositions;
  canvas.onSelectionChange = scheduleSidekickUpdate;
  installPastaPalette();
  applyGraphViewportState();
  resizeGraphCanvas();
  graphResizeObserver = new ResizeObserver(resizeGraphCanvas);
  graphResizeObserver.observe(el.graphCanvas);
  graph.start();
  el.graphCanvas.addEventListener("pointerdown", (event) => {
    armGraphShortcuts(event);
    el.graphCanvas.focus({ preventScroll: true });
  });
  el.graphCanvas.addEventListener("pointerenter", (event) => {
    graphPointerInside = true;
    rememberGraphPointer(event);
  });
  el.graphCanvas.addEventListener("pointermove", rememberGraphPointer);
  el.graphCanvas.addEventListener("pointerleave", () => {
    graphPointerInside = false;
  });
  el.graphCanvas.addEventListener("mouseup", () => {
    flushNodePositions();
    scheduleGraphViewportSave();
  });
  el.graphCanvas.addEventListener("wheel", scheduleGraphViewportSave);
  el.graphCanvas.addEventListener("mousewheel", scheduleGraphViewportSave);
  el.graphCanvas.addEventListener("dragover", handleGraphCanvasDragOver);
  el.graphCanvas.addEventListener("drop", handleGraphCanvasDrop);
  document.addEventListener("pointerup", () => {
    flushNodePositions();
    scheduleGraphViewportSave();
  }, { capture: true });
  document.addEventListener("pointerdown", (event) => {
    if (event.target !== el.graphCanvas) graphShortcutsArmed = false;
  }, { capture: true });
  document.addEventListener("mouseup", scheduleGraphViewportSave, { capture: true });
  window.addEventListener("blur", () => {
    flushNodePositions();
    saveGraphViewportState();
  });
  window.addEventListener("pagehide", saveGraphViewportState);
  el.graphCanvas.addEventListener("keydown", (event) => {
    graphKeydown(event);
    scheduleGraphViewportSave();
  }, { capture: true });
  el.graphCanvas.tabIndex = 0;
  requestAnimationFrame(resizeGraphCanvas);
}

function installLiteGraphDrawGuard(): void {
  if (liteGraphDrawGuardInstalled || !LGraphCanvas?.prototype?.drawFrontCanvas) return;
  const drawFrontCanvas = LGraphCanvas.prototype.drawFrontCanvas;
  LGraphCanvas.prototype.drawFrontCanvas = function (): void {
    sanitizeLiteGraphConnectionState(this);
    return drawFrontCanvas.call(this);
  };
  liteGraphDrawGuardInstalled = true;
}

function installLiteGraphWidgetSelectionGuard(): void {
  if (liteGraphWidgetSelectionGuardInstalled || !LGraphCanvas?.prototype?.processMouseDown) return;
  const processMouseDown = LGraphCanvas.prototype.processMouseDown;
  LGraphCanvas.prototype.processMouseDown = function (event: MouseEvent): unknown {
    const selectedBefore = selectedNodeIDs(this.selected_nodes);
    const result = processMouseDown.call(this, event);
    const [node, widget] = this.node_widget ?? [];
    if (widget?.options?.preserveSelection && node && event.type === `${LiteGraph.pointerevents_method}down`) {
      restoreGraphSelection(this, selectedBefore);
    }
    return result;
  };
  liteGraphWidgetSelectionGuardInstalled = true;
}

function sanitizeLiteGraphConnectionState(targetCanvas: any): void {
  if (!targetCanvas?.connecting_pos) return;
  if (targetCanvas.connecting_node && (targetCanvas.connecting_output || targetCanvas.connecting_input)) return;
  cancelLiteGraphConnectionState(targetCanvas);
}

function cancelLiteGraphConnectionState(targetCanvas: any): void {
  if (!targetCanvas) return;
  targetCanvas.connecting_output = null;
  targetCanvas.connecting_input = null;
  targetCanvas.connecting_pos = null;
  targetCanvas.connecting_node = null;
  targetCanvas.connecting_slot = -1;
  targetCanvas._highlight_input = null;
  targetCanvas._highlight_input_slot = null;
  targetCanvas._highlight_output = null;
  targetCanvas.dirty_canvas = true;
  targetCanvas.dirty_bgcanvas = true;
}

function installPastaPalette(): void {
  if (!canvas) return;
  canvas.getMenuOptions = () => [
    {
      content: "Add Node",
      has_submenu: true,
      callback: (_value: unknown, _options: unknown, event: MouseEvent, menu: unknown) => {
        showPastaAddNodeMenu(event, menu);
        return false;
      },
    },
  ];
  canvas.onSearchBox = (_helper: HTMLElement, query: string) => {
    const normalized = query.trim().toLowerCase();
    return paletteClasses()
      .filter((cls) => paletteLabel(cls).toLowerCase().includes(normalized) || cls.class.toLowerCase().includes(normalized))
      .map((cls) => escapeHTMLText(paletteLabel(cls)));
  };
  canvas.onSearchBoxSelection = (label: string, event: MouseEvent) => {
    const cls = paletteClasses().find((candidate) => (
      escapeHTMLText(paletteLabel(candidate)) === label || escapeHTMLText(candidate.class) === label
    ));
    if (cls) addPastaNodeAt(cls.class, canvasPositionFromEvent(event));
  };
}

function installLiteGraphTypeColors(): void {
  if (!canvas) return;
  const byType: Record<string, string> = {};
  for (const port of Object.values(snapshot.ports)) {
    const primaryType = liteGraphPortType(port.types);
    const color = portTypeColor(port.types);
    if (primaryType && color) byType[primaryType] = color;
  }
  if (canvas.default_connection_color_byType) {
    Object.assign(canvas.default_connection_color_byType, byType);
  }
  if (canvas.default_connection_color_byTypeOff) {
    Object.assign(canvas.default_connection_color_byTypeOff, byType);
  }
}

function showPastaAddNodeMenu(event: MouseEvent, parentMenu: unknown): void {
  const entries = paletteClasses().map((cls) => ({
    content: escapeHTMLText(paletteLabel(cls)),
    value: cls.class,
    callback: (entry: { value: string }) => {
      addPastaNodeAt(entry.value, canvasPositionFromEvent(event));
      return false;
    },
  }));
  new LiteGraph.ContextMenu(entries, { event, parentMenu }, canvas.getCanvasWindow());
}

function paletteClasses(): NodeClassSnapshot[] {
  return snapshot.classes;
}

function paletteLabel(cls: NodeClassSnapshot): string {
  return shortClass(cls.class);
}

function previewPorts(cls: NodeClassSnapshot, side: "left" | "right"): ClassPreviewPort[] {
  return cls.initial_ports
    .filter((port) => previewPortSide(port.direction) === side)
    .map((port) => ({
      name: port.name,
      color: previewPortColor(port.types),
    }));
}

function previewPortSide(direction: string): "left" | "right" | null {
  switch (direction.toLowerCase()) {
    case "left":
    case "in":
    case "input":
      return "left";
    case "right":
    case "out":
    case "output":
      return "right";
    default:
      return null;
  }
}

function previewPortColor(types: string[]): string {
  for (const type of types) {
    const color = typeColor(type);
    if (color) return color.color;
  }
  return DEFAULT_LINK_COLOR;
}

function resizeGraphCanvas(): void {
  if (!canvas) return;
  const rect = el.graphCanvas.getBoundingClientRect();
  const width = Math.max(1, Math.round(rect.width));
  const height = Math.max(1, Math.round(rect.height));
  canvas.resize(width, height);
}

function renderGraph(): void {
  if (!graph) return;
  rendering = true;
  cancelLiteGraphConnectionState(canvas);
  liteLinkToPasta.clear();
  const selected = new Set(selectedNodeIDs(canvas?.selected_nodes));
  graph.clear();

  for (const [idText, nodeSnapshot] of Object.entries(snapshot.nodes)) {
    const id = Number(idText);
    const node = LiteGraph.createNode("pasta/view");
    node.id = id;
    node.properties.pastaId = id;
    node.properties.pastaLabel = nodeSnapshot.label;
    node.properties.pastaPopups = nodeSnapshot.popups ?? [];
    node.title = nodeTitle(nodeSnapshot);
    const primaryColor = typeColor(nodeSnapshot.primary_type);
    node.color = nodeSnapshot.placeholder ? "#f38ba8" : primaryColor?.color ?? DEFAULT_NODE_TITLE_COLOR;
    node.bgcolor = nodeSnapshot.has_root_path ? "#313244" : "#1e1e2e";
    node.boxcolor = nodeSnapshot.root ? "#a6e3a1" : "#45475a";
    node.pos = parsePosition(nodeSnapshot.position, 80 + graph._nodes.length * 24, 80 + graph._nodes.length * 24);
    for (const portID of nodeSnapshot.left_ports) addPort(node, snapshot.ports[String(portID)], portID);
    for (const portID of nodeSnapshot.right_ports) addPort(node, snapshot.ports[String(portID)], portID);
    addNodeWidgets(node, id, nodeSnapshot);
    node.size = node.computeSize();
    fitLabelInNode(node, nodeSnapshot);
    graph.add(node);
  }

  for (const [idText, link] of Object.entries(snapshot.links)) {
    connectLink(Number(idText), link);
  }

  graph.setDirtyCanvas(true, true);
  if (selected.size) {
    selectGraphNodes([...selected]);
  }
  rendering = false;
  updateSidekick();
}

function selectGraphNodes(ids: number[]): void {
  if (!canvas || !graph) return;
  const nodes = ids.map((id) => graph.getNodeById(id)).filter(Boolean);
  if (nodes.length) canvas.selectNodes(nodes);
}

function restoreGraphSelection(targetCanvas: any, ids: number[]): void {
  const nodes = ids.map((id) => targetCanvas.graph?.getNodeById(id)).filter(Boolean);
  if (nodes.length) {
    targetCanvas.selectNodes(nodes);
  } else {
    targetCanvas.deselectAllNodes();
  }
  updateSidekick();
}

function nodeTitle(node: NodeSnapshot): string {
  return node.name;
}

function fitLabelInNode(node: any, snapshot: NodeSnapshot): void {
  if (!snapshot.label) return;
  const labelWidth = Math.min(280, 32 + snapshot.label.length * 7);
  node.size[0] = Math.max(node.size[0], labelWidth);
  node.size[1] += 24;
}

function addNodeWidgets(node: any, id: number, nodeSnapshot: NodeSnapshot): void {
  if (nodeSnapshot.class !== PASTA_TRIGGER_CLASS || nodeSnapshot.placeholder) return;
  node.addWidget("button", "Trigger", null, () => {
    void tryBackend("triggerNode", { id });
  }, { preserveSelection: true });
}

function drawNodeLabel(ctx: CanvasRenderingContext2D, node: any): void {
  const label = node.properties?.pastaLabel;
  if (!label || node.flags?.collapsed) return;
  ctx.save();
  ctx.font = "12px Arial";
  ctx.fillStyle = "#cdd6f4";
  ctx.textAlign = "center";
  ctx.textBaseline = "bottom";
  ctx.fillText(String(label), node.size[0] * 0.5, node.size[1] - 7, node.size[0] - 18);
  ctx.restore();
}

function drawNodePopup(ctx: CanvasRenderingContext2D, node: any): void {
  const popup = latestPriorityPopup(node.properties?.pastaPopups);
  if (!popup) return;
  const text = trimPopupText(popup.text, 50);
  const palette = popupPalette(popup.type);
  ctx.save();
  ctx.font = "12px Arial";
  const paddingX = 8;
  const width = Math.max(54, ctx.measureText(text).width + paddingX * 2);
  const height = 22;
  const x = (node.size[0] - width) * 0.5;
  const y = -54;
  ctx.fillStyle = palette.bg;
  ctx.strokeStyle = palette.border;
  ctx.lineWidth = 1;
  roundRect(ctx, x, y, width, height, 5);
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = palette.text;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(text, x + width * 0.5, y + height * 0.5, width - paddingX * 2);
  ctx.restore();
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, width: number, height: number, radius: number): void {
  const r = Math.min(radius, width * 0.5, height * 0.5);
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + width, y, x + width, y + height, r);
  ctx.arcTo(x + width, y + height, x, y + height, r);
  ctx.arcTo(x, y + height, x, y, r);
  ctx.arcTo(x, y, x + width, y, r);
  ctx.closePath();
}

function popupPalette(type: string): { bg: string; border: string; text: string } {
  switch (type) {
    case "err":
      return { bg: "#4a1f2a", border: "#f38ba8", text: "#f8d4de" };
    case "ward":
      return { bg: "#473817", border: "#f9e2af", text: "#fff1c7" };
    case "info":
      return { bg: "#18304b", border: "#89b4fa", text: "#d7e5ff" };
    default:
      return { bg: "#313244", border: "#a6adc8", text: "#f8f9ff" };
  }
}

function addPort(node: any, port: PortSnapshot | undefined, portID: number): void {
  if (!port) return;
  const type = liteGraphPortType(port.types);
  const color = portTypeColor(port.types);
  const extra = {
    pastaPortId: portID,
    label: port.name,
    pastaTypes: port.types,
    color_on: color ?? undefined,
    color_off: color ?? undefined,
  };
  if (port.direction === "left") {
    const linkIDs = port.links.length > 1 ? port.links : [undefined];
    for (let i = 0; i < linkIDs.length; i += 1) {
      const name = i === 0 ? port.name : `${port.name} ${i + 1}`;
      node.addInput(name, type, { ...extra, pastaLinkId: linkIDs[i] });
    }
  } else {
    node.addOutput(port.name, type, extra);
  }
}

function liteGraphPortType(types: string[]): string {
  if (!types.length || types.includes("any/any")) return "";
  return types.join(",");
}

function portTypeColor(types: string[]): string | null {
  for (const type of types) {
    const color = typeColor(type);
    if (color) return color.color;
  }
  return null;
}

function connectLink(id: number, link: LinkSnapshot): void {
  const outNode = graph.getNodeById(link.right_port_node);
  const inNode = graph.getNodeById(link.left_port_node);
  if (!outNode || !inNode) return;
  const outIndex = findPortIndex(snapshot.nodes[String(link.right_port_node)]?.right_ports, link.right_port);
  const inIndex = findInputSlotIndex(inNode, link.left_port, id);
  if (outIndex < 0 || inIndex < 0) return;
  const liteLink = outNode.connect(outIndex, inNode, inIndex);
  if (liteLink) {
    liteLink.color = linkColor(link.type);
    liteLink.data = { pastaId: id };
    liteLinkToPasta.set(liteLink.id, id);
  }
}

function findPortIndex(ports: number[] | undefined, id: number): number {
  return ports ? ports.indexOf(id) : -1;
}

function findInputSlotIndex(node: any, portID: number, linkID: number): number {
  if (!node.inputs) return -1;
  const exact = node.inputs.findIndex((input: { pastaPortId?: number; pastaLinkId?: number }) => (
    input.pastaPortId === portID && input.pastaLinkId === linkID
  ));
  if (exact >= 0) return exact;
  return node.inputs.findIndex((input: { pastaPortId?: number }) => input.pastaPortId === portID);
}

function flushNodePositions(): void {
  if (rendering || !graph) return;
  const changes: Array<{ id: number; position: string }> = [];
  for (const node of graph._nodes as any[]) {
    const pastaNode = snapshot.nodes[String(node.id)];
    if (!pastaNode) continue;
    const next = formatPosition(node.pos);
    if (next !== pastaNode.position) {
      changes.push({ id: node.id, position: next });
    }
  }
  if (changes.length === 1) {
    tryBackend("setNodePosition", changes[0]);
  } else if (changes.length > 1) {
    tryBackend("moveNodes", { positions: nodePositionMap(changes) });
  }
}

function currentSelectedNodeIDs(): number[] {
  return selectedNodeIDs(canvas?.selected_nodes).filter((id) => graph?.getNodeById(id)?.is_selected);
}

function updateSidekick(): void {
  const ids = currentSelectedNodeIDs();
  if (ids.length === 1) renderNodePanel(ids[0]!);
  else renderClassPanel(ids.length);
}

function scheduleSidekickUpdate(): void {
  window.clearTimeout(sidekickUpdate);
  sidekickUpdate = window.setTimeout(updateSidekick, 0);
}

function renderClassPanel(selectedCount: number): void {
  destroyMenu();
  el.sidekick.dataset.nodeId = "";
  el.sidekick.dataset.nodeClass = "";
  const classes = snapshot.classes;
  const list = document.createElement("div");
  list.className = "class-list";
  el.sidekick.replaceChildren(
    textElement("div", selectedCount > 1 ? `${selectedCount} nodes selected` : "Create node", "panel-title"),
    list,
  );
  for (const cls of classes) {
    const primaryColor = typeColor(cls.primary_type);
    const leftPorts = previewPorts(cls, "left");
    const rightPorts = previewPorts(cls, "right");
    const button = document.createElement("button");
    button.className = "class-button";
    button.classList.toggle("class-button-primary", Boolean(primaryColor));
    button.type = "button";
    button.draggable = true;
    button.setAttribute("aria-label", `Create ${shortClass(cls.class)} node`);
    if (primaryColor) {
      button.style.setProperty("--class-primary-color", safeCSSColor(primaryColor.color, DEFAULT_NODE_TITLE_COLOR));
      button.style.setProperty("--class-primary-text-color", safeCSSColor(primaryColor.textColor, DEFAULT_NODE_TEXT_COLOR));
    }
    button.append(...createClassButtonContent(cls, leftPorts, rightPorts));
    button.addEventListener("dragstart", (event) => {
      if (!event.dataTransfer) return;
      event.dataTransfer.effectAllowed = "copy";
      event.dataTransfer.setData(CLASS_DRAG_MIME, cls.class);
      event.dataTransfer.setData("text/plain", cls.class);
    });
    button.addEventListener("click", () => {
      addPastaNodeAt(cls.class, visibleCanvasSpawnPosition());
    });
    list.append(button);
  }
}

function addPastaNodeAt(className: string, pos: [number, number]): void {
  const position = formatPosition(pos);
  const placement: PendingNodePlacement = { className, position };
  pendingNodePlacements.push(placement);
  void tryBackend("addNode", { class: className, position }).then((id) => {
    if (id === null) {
      forgetPendingNodePlacement(placement);
      return;
    }
    placement.id = id;
  });
}

function visibleCanvasSpawnPosition(): [number, number] {
  if (!canvas?.convertCanvasToOffset) return [160, 120];
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(rect.width * 0.5, 80, Math.max(80, rect.width - 160));
  const y = clamp(rect.height * 0.35, 80, Math.max(80, rect.height - 120));
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function canvasPositionFromEvent(event: MouseEvent): [number, number] {
  if (!canvas?.convertCanvasToOffset) return visibleCanvasSpawnPosition();
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(event.clientX - rect.left, 0, rect.width);
  const y = clamp(event.clientY - rect.top, 0, rect.height);
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function canvasPositionFromDragEvent(event: DragEvent): [number, number] {
  if (!canvas?.convertCanvasToOffset) return visibleCanvasSpawnPosition();
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(event.clientX - rect.left, 0, rect.width);
  const y = clamp(event.clientY - rect.top, 0, rect.height);
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function dragEventClassName(event: DragEvent): string {
  const className = event.dataTransfer?.getData(CLASS_DRAG_MIME) || event.dataTransfer?.getData("text/plain") || "";
  return nodeClassByName(snapshot, className) ? className : "";
}

function handleGraphCanvasDragOver(event: DragEvent): void {
  if (!dragEventClassName(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
}

function handleGraphCanvasDrop(event: DragEvent): void {
  const className = dragEventClassName(event);
  if (!className) return;
  event.preventDefault();
  addPastaNodeAt(className, canvasPositionFromDragEvent(event));
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function renderNodePanel(id: number): void {
  const node = snapshot.nodes[String(id)];
  if (!node) return;
  if (node.placeholder) {
    renderPlaceholderNodePanel(id, node);
    return;
  }
  if (
    el.sidekick.dataset.nodeId === String(id) &&
    el.sidekick.dataset.nodeClass === node.class &&
    selectedMenu?.node === id &&
    document.getElementById("node-menu")
  ) {
    syncNodeNameEditor(node);
    renderNodePopups(id, node);
    return;
  }
  const description = "";
  destroyMenu();
  el.sidekick.dataset.nodeId = String(id);
  el.sidekick.dataset.nodeClass = node.class;
  const tabs = document.createElement("div");
  tabs.className = "node-panel-tabs";
  const menuTab = textElement("button", "Node menu");
  menuTab.className = "active";
  menuTab.dataset.nodeTab = "menu";
  menuTab.type = "button";
  const docsTab = textElement("button", "Description");
  docsTab.dataset.nodeTab = "docs";
  docsTab.type = "button";
  tabs.append(menuTab, docsTab);

  const menuPanel = document.createElement("div");
  menuPanel.className = "node-panel-section";
  menuPanel.dataset.nodePanel = "menu";
  const menu = document.createElement("div");
  menu.id = "node-menu";
  const popups = document.createElement("div");
  popups.id = "node-popups";
  menuPanel.append(menu, popups);

  const docsPanel = document.createElement("div");
  docsPanel.className = "node-panel-section hidden";
  docsPanel.dataset.nodePanel = "docs";
  const descriptionPanel = document.createElement("div");
  descriptionPanel.className = "node-description";
  descriptionPanel.dataset.nodeDescription = "";
  renderNodeDescription(descriptionPanel, description);
  docsPanel.append(textElement("h2", shortClass(node.class)), descriptionPanel);
  el.sidekick.replaceChildren(createNodeNameForm(node.name), tabs, menuPanel, docsPanel);
  void tryBackend("classDescription", { class: node.class }).then((text) => {
    const descriptionPanel = el.sidekick.querySelector<HTMLElement>("[data-node-description]");
    if (
      descriptionPanel &&
      el.sidekick.dataset.nodeId === String(id) &&
      el.sidekick.dataset.nodeClass === node.class
    ) {
      renderNodeDescription(descriptionPanel, text ?? "");
    }
  });
  for (const tab of el.sidekick.querySelectorAll<HTMLButtonElement>("[data-node-tab]")) {
    tab.addEventListener("click", () => setNodePanelTab(tab.dataset.nodeTab ?? "menu"));
  }
  const form = el.sidekick.querySelector<HTMLFormElement>("[data-node-name-form]");
  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    submitNodeName(id);
  });
  form?.querySelector<HTMLInputElement>("input")?.addEventListener("blur", () => submitNodeName(id));
  mountMenu(id);
  renderNodePopups(id, node);
}

function renderPlaceholderNodePanel(id: number, node: NodeSnapshot): void {
  destroyMenu();
  el.sidekick.dataset.nodeId = String(id);
  el.sidekick.dataset.nodeClass = node.class;
  const section = document.createElement("div");
  section.className = "node-panel-section";
  section.append(
    textElement("h2", shortClass(node.class)),
    textElement("p", "This node is a placeholder. Its implementation is unavailable now, so almagest preserves its ports and links as placeholders in case implementation will become available later."),
  );
  el.sidekick.replaceChildren(createNodeNameForm(node.name), section);
  const form = el.sidekick.querySelector<HTMLFormElement>("[data-node-name-form]");
  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    submitNodeName(id);
  });
  form?.querySelector<HTMLInputElement>("input")?.addEventListener("blur", () => submitNodeName(id));
}

function syncNodeNameEditor(node: NodeSnapshot): void {
  const input = el.sidekick.querySelector<HTMLInputElement>("#node-name-input");
  if (!input || document.activeElement === input) return;
  input.value = node.name;
}

async function submitNodeName(id: number): Promise<void> {
  const input = el.sidekick.querySelector<HTMLInputElement>("#node-name-input");
  if (!input) return;
  const node = snapshot.nodes[String(id)];
  const name = input.value.trim();
  if (!node || name === node.name) {
    if (node) input.value = node.name;
    return;
  }
  const renamed = await tryBackend("setNodeName", { id, name });
  if (!renamed) {
    input.value = node.name;
  }
}

function renderNodePopups(nodeID: number, node: NodeSnapshot): void {
  const root = el.sidekick.querySelector<HTMLElement>("#node-popups");
  if (!root) return;
  const popups = [...(node.popups ?? [])].reverse();
  root.replaceChildren();
  const header = document.createElement("div");
  header.className = "popup-panel-header";
  header.append(textElement("h2", "Popups"));
  if (popups.length) {
    const clear = document.createElement("button");
    clear.type = "button";
    clear.textContent = "Clear all";
    clear.addEventListener("click", () => tryBackend("removeNodePopups", { id: nodeID }));
    header.append(clear);
  }
  root.append(header);
  if (!popups.length) {
    const empty = document.createElement("p");
    empty.className = "popup-empty";
    empty.textContent = "No popups";
    root.append(empty);
    return;
  }
  const list = document.createElement("ol");
  list.className = "popup-list";
  for (const popup of popups) {
    list.append(renderPopupItem(nodeID, popup));
  }
  root.append(list);
}

function renderPopupItem(nodeID: number, popup: NodePopup): HTMLElement {
  const item = document.createElement("li");
  item.className = `popup-item popup-${popup.type}`;
  const type = document.createElement("strong");
  type.textContent = popup.type;
  const text = document.createElement("span");
  text.textContent = popup.text;
  const remove = document.createElement("button");
  remove.type = "button";
  remove.textContent = "Delete";
  remove.addEventListener("click", () => tryBackend("removeNodePopup", { id: nodeID, popupId: popup.id }));
  item.append(type, text, remove);
  return item;
}

function setNodePanelTab(name: string): void {
  for (const tab of el.sidekick.querySelectorAll<HTMLButtonElement>("[data-node-tab]")) {
    tab.classList.toggle("active", tab.dataset.nodeTab === name);
  }
  for (const panel of el.sidekick.querySelectorAll<HTMLElement>("[data-node-panel]")) {
    panel.classList.toggle("hidden", panel.dataset.nodePanel !== name);
  }
}

function mountMenu(nodeID: number): void {
  if (selectedMenu?.node === nodeID && document.getElementById("node-menu")) return;
  destroyMenu();
  selectedMenu = {
    node: nodeID,
    menu: new FormularMenu("node-menu", nodeMenuID(nodeID), (message: unknown) => {
      tryBackend("formular", { id: nodeID, message });
    }, {
      selectors: (selector: string) => formularSelectorValues(nodeID, selector),
    }),
  };
  tryBackend("subscribeNodeMenu", { id: nodeID });
}

function formularSelectorValues(nodeID: number, selector: string): readonly string[] | null {
  if (selector !== FORMULAR_SECRET_SELECTOR) return null;
  const nodeName = snapshot.nodes[String(nodeID)]?.name ?? "";
  // An empty value removes an optional reference or requests a new generated
  // WireGuard private-key secret.
  return ["", ...secretNamesForNode(secrets, nodeName)];
}

function destroyMenu(): void {
  if (!selectedMenu) return;
  tryBackend("unsubscribeNodeMenu", { id: selectedMenu.node });
  selectedMenu.menu.destroy();
  selectedMenu = null;
}

function feedMenu(notification: WorkspaceNotification): void {
  if (!selectedMenu || notification.id !== selectedMenu.node || !notification.formular) return;
  const message = notification.formular as { type?: unknown; dialog?: { kind?: unknown } };
  try {
    const accepted = selectedMenu.menu.feed(message);
    if (message.type === "dialog.create" && !accepted) {
      const text = "The node menu rejected a dialog request. Reload the panel to use the current Formular renderer.";
      log("frontend", "error", text);
      notifyError(text);
    } else if (message.type === "dialog.create" && message.dialog?.kind === "captcha") {
      log("frontend", "debug", `Captcha dialog opened for node ${notification.id}.`);
    }
  } catch (error) {
    if (message.type !== "dialog.create") throw error;
    const text = `The node menu could not open a dialog: ${errorMessage(error)}`;
    log("frontend", "error", text);
    notifyError(text);
  }
}

async function requestSnapshot(): Promise<void> {
  const next = await tryBackend("snapshot");
  if (next) {
    snapshot = applyWorkspaceSnapshot(snapshot, next);
    renderGraph();
    renderWorkers();
    updateWorkerAgeTimer();
  }
}

function handleSecEvent(method: string, payload: unknown): void {
  if (method !== "update") return;
  const event = parseSecretsEvent(payload);
  if (!event) return;
  if (event.subscription_id !== secSubscriptionID) return;
  applySecretChangeToState(event.change);
  renderSec();
}

// The secrets API already sends node allowlists with each entry. Keep this
// subscription for the full panel session so node-menu selectors stay current.
// Values are shown in this tab only after explicit selection.
function activateSecPanel(): void {
  renderSec();
  restoreSecScrollState();
  void ensureSecSubscription();
}

function deactivateSecPanel(): void {
  rememberSecScrollState();
  closeSecValuePanel();
}

async function ensureSecSubscription(): Promise<void> {
  if (secSubscriptionID || secSubscribePending) return;
  secSubscribePending = true;
  const response = await tryBackend("secSubscribe");
  secSubscribePending = false;
  if (!response) {
    secNotice = { level: "error", text: "Secrets storage is unavailable." };
    renderSec();
    return;
  }
  secSubscriptionID = response.subscription_id;
  applySecretsSnapshot(response.snapshot);
}

function applySecretsSnapshot(snapshot: SecretsSnapshot): void {
  secrets.splice(0, secrets.length, ...sortSecrets(snapshot.secrets ?? []));
  if (secValuePanel && !secretEntryByKey(secValuePanel.key)) closeSecValuePanel();
  renderSec();
}

function applySecretChangeToState(change: SecretChange): void {
  secrets.splice(0, secrets.length, ...applySecretChange(secrets, change));
  if (change.deleted && secValuePanel?.key === change.key) {
    closeSecValuePanel();
  }
}

function renderSec(): void {
  const previousTop = el.secPanel.scrollTop;
  const tree = document.createElement("div");
  tree.className = "kv-tree-inner";
  tree.append(renderSecHeader());
  if (secNotice) tree.append(renderSecNotice(secNotice));
  if (secOpenForm) tree.append(renderSecretCreateForm());

  const list = document.createElement("ol");
  list.className = "kv-store-list";
  if (!secrets.length) {
    list.append(renderKVEmptyState(secSubscribePending ? "Loading secrets." : "No secrets."));
  } else {
    for (const entry of secrets) {
      list.append(renderSecretRow(entry));
    }
  }
  tree.append(list);
  el.secTree.replaceChildren(tree);
  el.secPanel.scrollTop = activeTab === "sec" ? previousTop : secScrollTop;
  renderSecValuePanel();
}

function renderSecHeader(): HTMLElement {
  const header = document.createElement("header");
  header.className = "kv-header sec-header";
  header.append(textElement("h2", "Secrets"));
  const add = textElement("button", "+ Secret");
  add.type = "button";
  add.title = "Create secret";
  add.addEventListener("click", () => {
    secOpenForm = true;
    renderSec();
  });
  header.append(add);
  return header;
}

function renderSecNotice(notice: SecNotice): HTMLElement {
  const row = document.createElement("div");
  row.className = `kv-notice kv-notice-${notice.level}`;
  row.append(textElement("span", notice.text));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Dismiss";
  close.setAttribute("aria-label", "Dismiss Sec message");
  close.addEventListener("click", () => {
    secNotice = null;
    renderSec();
  });
  row.append(close);
  return row;
}

function renderSecretRow(entry: SecretEntry): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "kv-entry kv-entry-value sec-secret";
  item.classList.toggle("sec-secret-readonly", Boolean(entry.read_only));
  const row = document.createElement("div");
  row.className = "kv-row";
  row.append(createKVIndent());
  row.append(createSecretTitle(entry));
  row.append(renderSecretRowActions(entry));
  if (secValuePanel?.key === entry.key) {
    row.classList.add("kv-row-selected");
  }
  item.append(row);
  return item;
}

function renderSecretRowActions(entry: SecretEntry): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const show = textElement("button", "Open");
  show.type = "button";
  show.title = "Open secret";
  show.addEventListener("click", () => openSecValuePanel(entry.key));
  actions.append(show);
  if (!entry.read_only) {
    const remove = textElement("button", "Delete");
    remove.type = "button";
    remove.title = "Delete secret";
    remove.className = "danger";
    remove.addEventListener("click", () => deleteSecret(entry.key));
    actions.append(remove);
  }
  return actions;
}

function renderSecretCreateForm(): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form sec-secret-form";
  const key = document.createElement("input");
  key.name = "key";
  key.placeholder = "Key";
  key.autocomplete = "off";
  key.required = true;
  const nodeEditor = createSecretNodeEditor(2);
  const nodes = nodeEditor.textarea;
  nodes.placeholder = "Allowed nodes";
  const value = document.createElement("textarea");
  value.name = "value";
  value.placeholder = "Value";
  value.rows = 5;
  const actions = document.createElement("div");
  actions.className = "sec-form-actions";
  const save = textElement("button", "Create");
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", () => {
    secOpenForm = false;
    renderSec();
  });
  actions.append(save, cancel);
  form.append(
    secFormField("Key", key),
    secFormField("Nodes", nodeEditor.root),
    secFormField("Value", value, "sec-form-field-wide"),
    actions,
  );
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void createSecret(key.value, nodes.value, value.value);
  });
  window.setTimeout(() => key.focus(), 0);
  return form;
}

function secFormField(label: string, control: HTMLElement, className = ""): HTMLLabelElement {
  const field = document.createElement("label");
  field.className = `sec-form-field ${className}`.trim();
  field.append(textElement("span", label), control);
  return field;
}

function createSecretNodeEditor(rows: number, value = ""): { root: HTMLElement; textarea: HTMLTextAreaElement } {
  const root = document.createElement("div");
  root.className = "sec-node-editor";
  const textarea = document.createElement("textarea");
  textarea.name = "nodes";
  textarea.rows = rows;
  textarea.value = value;
  textarea.setAttribute("autocomplete", "off");
  const suggestions = document.createElement("div");
  suggestions.className = "sec-node-suggestions hidden";
  suggestions.id = `sec-node-suggestions-${secNodeAutocompleteID}`;
  suggestions.setAttribute("role", "listbox");
  secNodeAutocompleteID += 1;
  textarea.setAttribute("aria-autocomplete", "list");
  textarea.setAttribute("aria-controls", suggestions.id);
  textarea.setAttribute("aria-expanded", "false");
  root.append(textarea, suggestions);
  wireSecretNodeAutocomplete(textarea, suggestions);
  return { root, textarea };
}

function wireSecretNodeAutocomplete(textarea: HTMLTextAreaElement, suggestions: HTMLElement): void {
  let activeIndex = 0;
  let result = secretNodeTextSuggestions(
    textarea.value,
    textarea.selectionStart,
    workspaceSecretNodeNames(),
    SECRET_NODE_AUTOCOMPLETE_LIMIT,
  );

  const hide = () => {
    suggestions.classList.add("hidden");
    suggestions.replaceChildren();
    textarea.setAttribute("aria-expanded", "false");
    textarea.removeAttribute("aria-activedescendant");
  };

  const render = () => {
    suggestions.replaceChildren();
    if (!result.suggestions.length || document.activeElement !== textarea) {
      hide();
      return;
    }
    activeIndex = Math.max(0, Math.min(activeIndex, result.suggestions.length - 1));
    for (const [index, nodeName] of result.suggestions.entries()) {
      const option = textElement("div", nodeName, "sec-node-suggestion");
      option.id = `${suggestions.id}-${index}`;
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", String(index === activeIndex));
      option.classList.toggle("active", index === activeIndex);
      option.addEventListener("mousedown", (event) => {
        event.preventDefault();
        accept(index);
      });
      suggestions.append(option);
    }
    suggestions.classList.remove("hidden");
    textarea.setAttribute("aria-expanded", "true");
    textarea.setAttribute("aria-activedescendant", `${suggestions.id}-${activeIndex}`);
  };

  const update = () => {
    result = secretNodeTextSuggestions(
      textarea.value,
      textarea.selectionStart,
      workspaceSecretNodeNames(),
      SECRET_NODE_AUTOCOMPLETE_LIMIT,
    );
    activeIndex = 0;
    render();
  };

  const accept = (index: number) => {
    const nodeName = result.suggestions[index];
    if (!nodeName) return;
    const replacement = replaceSecretNodeTextToken(textarea.value, result.token, nodeName);
    textarea.value = replacement.text;
    textarea.setSelectionRange(replacement.cursor, replacement.cursor);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    hide();
    textarea.focus();
  };

  textarea.addEventListener("input", update);
  textarea.addEventListener("focus", update);
  textarea.addEventListener("click", update);
  textarea.addEventListener("keyup", (event) => {
    if (["ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown"].includes(event.key)) update();
  });
  textarea.addEventListener("keydown", (event) => {
    if (suggestions.classList.contains("hidden")) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      activeIndex = (activeIndex + 1) % result.suggestions.length;
      render();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      activeIndex = (activeIndex + result.suggestions.length - 1) % result.suggestions.length;
      render();
    } else if (event.key === "Enter" || event.key === "Tab") {
      event.preventDefault();
      accept(activeIndex);
    } else if (event.key === "Escape") {
      event.preventDefault();
      hide();
    }
  });
  textarea.addEventListener("blur", () => window.setTimeout(hide, 80));
}

async function createSecret(key: string, nodesText: string, value: string): Promise<void> {
  const name = key.trim();
  if (!name) {
    secNotice = { level: "error", text: "Secret key is required." };
    renderSec();
    return;
  }
  if (secretEntryByKey(name)) {
    secNotice = { level: "error", text: "Secret already exists." };
    renderSec();
    return;
  }
  const nodes = parseSecretNodeText(nodesText);
  if (!validateSecretNodes(nodes)) return;
  try {
    const valueEntry = await backend("secSetValue", { key: name, value });
    const entry = nodes.length
      ? await backend("secSetNodes", { key: name, nodes })
      : valueEntry;
    upsertSecretEntry(entry);
    secNotice = { level: "info", text: "Secret created." };
    secOpenForm = false;
    openSecValuePanel(entry.key);
  } catch (error) {
    secNotice = { level: "error", text: `secCreate: ${errorMessage(error)}` };
    renderSec();
  }
}

function openSecValuePanel(key: string): void {
  secValuePanel = {
    key,
    reveal: false,
    editing: null,
  };
  renderSec();
}

function closeSecValuePanel(): void {
  secValuePanel = null;
  renderSecValuePanel();
}

function renderSecValuePanel(): void {
  el.secValuePanel.replaceChildren();
  el.secPanel.classList.toggle("kv-value-open", Boolean(secValuePanel));
  if (!secValuePanel) {
    el.secValuePanel.classList.add("hidden");
    return;
  }
  el.secValuePanel.classList.remove("hidden");
  const entry = secretEntryByKey(secValuePanel.key);
  if (!entry) {
    el.secValuePanel.append(textElement("p", "Secret not found.", "kv-value-missing"));
    return;
  }
  if (entry.read_only && secValuePanel.editing) {
    secValuePanel.editing = null;
  }

  const header = document.createElement("header");
  header.className = "kv-value-header";
  const title = document.createElement("div");
  title.append(textElement("h2", entry.key), textElement("p", formatSecretEntryMeta(entry)));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Close";
  close.setAttribute("aria-label", "Close secret panel");
  close.addEventListener("click", closeSecValuePanel);
  header.append(title, close);
  el.secValuePanel.append(header, renderSecretDetail(entry));
}

function renderSecretDetail(entry: SecretEntry): HTMLElement {
  const detail = document.createElement("div");
  detail.className = "kv-value-detail";
  const meta = document.createElement("div");
  meta.className = "kv-value-meta";
  meta.append(renderSecretValueActions(entry));
  detail.append(meta);
  switch (secValuePanel?.editing) {
    case "value":
      detail.append(renderSecretValueForm(entry));
      break;
    case "nodes":
      detail.append(renderSecretNodesForm(entry));
      break;
    case "rename":
      detail.append(renderSecretRenameForm(entry));
      break;
    default:
      detail.append(createSecretValuePreview(entry, {
        reveal: secValuePanel?.reveal ?? false,
        limit: SECRET_TEXT_RENDER_LIMIT,
      }));
      detail.append(renderSecretNodes(entry));
      break;
  }
  return detail;
}

function renderSecretValueActions(entry: SecretEntry): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-value-actions";
  const copy = textElement("button", "Copy");
  copy.type = "button";
  copy.title = "Copy secret value";
  copy.addEventListener("click", () => copySecretValue(entry));
  const download = textElement("button", "Download");
  download.type = "button";
  download.title = "Download secret value";
  download.addEventListener("click", () => downloadSecretValue(entry));
  const reveal = textElement("button", secValuePanel?.reveal ? "Hide" : "Reveal");
  reveal.type = "button";
  reveal.title = secValuePanel?.reveal ? "Hide secret value" : "Reveal secret value";
  reveal.addEventListener("click", () => {
    if (!secValuePanel) return;
    secValuePanel.reveal = !secValuePanel.reveal;
    renderSecValuePanel();
  });
  actions.append(copy, download, reveal);
  if (!entry.read_only) {
    const editValue = textElement("button", "Edit");
    editValue.type = "button";
    editValue.title = "Edit secret value";
    editValue.addEventListener("click", () => setSecEditor("value"));
    const editNodes = textElement("button", "Nodes");
    editNodes.type = "button";
    editNodes.title = "Edit allowed nodes";
    editNodes.addEventListener("click", () => setSecEditor("nodes"));
    const rename = textElement("button", "Rename");
    rename.type = "button";
    rename.title = "Rename secret";
    rename.addEventListener("click", () => setSecEditor("rename"));
    const remove = textElement("button", "Delete");
    remove.type = "button";
    remove.title = "Delete secret";
    remove.className = "danger";
    remove.addEventListener("click", () => deleteSecret(entry.key));
    actions.append(editValue, editNodes, rename, remove);
  }
  return actions;
}

function setSecEditor(editor: SecValuePanelState["editing"]): void {
  if (!secValuePanel) return;
  secValuePanel.editing = editor;
  renderSecValuePanel();
}

function renderSecretValueForm(entry: SecretEntry): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form sec-value-edit-form";
  const value = document.createElement("textarea");
  value.name = "value";
  value.rows = 8;
  value.value = entry.value ?? "";
  const actions = secEditActions();
  form.append(secFormField("Value", value, "sec-form-field-wide"), actions);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void updateSecretValue(entry.key, value.value);
  });
  window.setTimeout(() => value.focus(), 0);
  return form;
}

function renderSecretNodesForm(entry: SecretEntry): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form sec-nodes-form";
  const nodeEditor = createSecretNodeEditor(5, secretNodesText(entry.nodes));
  const nodes = nodeEditor.textarea;
  const actions = secEditActions();
  form.append(secFormField("Nodes", nodeEditor.root, "sec-form-field-wide"), actions);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void updateSecretNodes(entry.key, nodes.value);
  });
  window.setTimeout(() => nodes.focus(), 0);
  return form;
}

function renderSecretRenameForm(entry: SecretEntry): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form sec-rename-form";
  const key = document.createElement("input");
  key.name = "key";
  key.autocomplete = "off";
  key.required = true;
  key.value = entry.key;
  const actions = secEditActions();
  form.append(secFormField("Key", key), actions);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void renameSecret(entry.key, key.value);
  });
  window.setTimeout(() => key.focus(), 0);
  return form;
}

function secEditActions(): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "sec-form-actions";
  const save = textElement("button", "Save");
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", () => setSecEditor(null));
  actions.append(save, cancel);
  return actions;
}

async function updateSecretValue(key: string, value: string): Promise<void> {
  const entry = await runSecMutation("secSetValue", { key, value }, "Secret value saved.");
  if (!entry) return;
  upsertSecretEntry(entry);
  if (secValuePanel) {
    secValuePanel.editing = null;
    secValuePanel.reveal = false;
  }
  renderSec();
}

async function updateSecretNodes(key: string, nodesText: string): Promise<void> {
  const nodes = parseSecretNodeText(nodesText);
  if (!validateSecretNodes(nodes)) return;
  const entry = await runSecMutation("secSetNodes", { key, nodes }, "Secret nodes saved.");
  if (!entry) return;
  upsertSecretEntry(entry);
  if (secValuePanel) secValuePanel.editing = null;
  renderSec();
}

async function renameSecret(key: string, nextKey: string): Promise<void> {
  const newKey = nextKey.trim();
  if (!newKey) {
    secNotice = { level: "error", text: "Secret key is required." };
    renderSec();
    return;
  }
  if (newKey !== key && secretEntryByKey(newKey)) {
    secNotice = { level: "error", text: "Secret already exists." };
    renderSec();
    return;
  }
  const entry = await runSecMutation("secRename", { key, newKey }, "Secret renamed.");
  if (!entry) return;
  if (key !== entry.key) removeSecretEntry(key);
  upsertSecretEntry(entry);
  if (secValuePanel) {
    secValuePanel.key = entry.key;
    secValuePanel.editing = null;
  }
  renderSec();
}

async function deleteSecret(key: string): Promise<void> {
  if (!window.confirm(`Delete secret "${key}"?`)) return;
  if (await runSecMutation("secDelete", { key }, "Secret deleted.")) {
    removeSecretEntry(key);
    if (secValuePanel?.key === key) closeSecValuePanel();
    renderSec();
  }
}

async function runSecMutation<M extends SecMutationMethod>(
  method: M,
  params: Record<string, unknown>,
  success: string,
): Promise<BackendResultMap[M] | null> {
  try {
    const result = await backend(method, params);
    secNotice = { level: "info", text: success };
    renderSec();
    return result;
  } catch (error) {
    secNotice = { level: "error", text: `${method}: ${errorMessage(error)}` };
    renderSec();
    return null;
  }
}

function renderSecretNodes(entry: SecretEntry): HTMLElement {
  const block = document.createElement("section");
  block.className = "sec-node-block";
  block.append(textElement("h3", "Allowed nodes"));
  const nodes = normalizeSecretNodes(entry.nodes ?? []);
  if (!nodes.length) {
    block.append(textElement("p", "No nodes allowed.", "kv-subtree-empty"));
    return block;
  }
  const list = document.createElement("ol");
  list.className = "sec-node-list";
  for (const node of nodes) {
    list.append(textElement("li", node, "sec-node-chip"));
  }
  block.append(list);
  return block;
}

function validateSecretNodes(nodes: string[]): boolean {
  const known = new Set(workspaceSecretNodeNames());
  const unknown = nodes.filter((node) => !known.has(node));
  if (!unknown.length) return true;
  secNotice = { level: "error", text: `Unknown node${unknown.length === 1 ? "" : "s"}: ${unknown.join(", ")}` };
  renderSec();
  return false;
}

function workspaceSecretNodeNames(): string[] {
  return normalizeSecretNodes(Object.values(snapshot.nodes).map((node) => node.name));
}

function secretEntryByKey(key: string): SecretEntry | null {
  return secrets.find((entry) => entry.key === key) ?? null;
}

function upsertSecretEntry(entry: SecretEntry): void {
  secrets.splice(0, secrets.length, ...sortSecrets([...secrets.filter((secret) => secret.key !== entry.key), entry]));
}

function removeSecretEntry(key: string): void {
  const index = secrets.findIndex((entry) => entry.key === key);
  if (index >= 0) secrets.splice(index, 1);
}

async function copySecretValue(entry: SecretEntry): Promise<void> {
  try {
    await navigator.clipboard.writeText(entry.value ?? "");
    secNotice = { level: "info", text: "Secret value copied." };
  } catch (error) {
    secNotice = { level: "error", text: `Copy failed: ${errorMessage(error)}` };
  }
  renderSec();
}

function downloadSecretValue(entry: SecretEntry): void {
  try {
    const blob = new Blob([entry.value ?? ""], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${safeFilenamePart(entry.key) || "secret"}.txt`;
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    secNotice = { level: "info", text: "Download started." };
  } catch (error) {
    secNotice = { level: "error", text: `Download failed: ${errorMessage(error)}` };
  }
  renderSec();
}

function rememberSecScrollState(): void {
  secScrollTop = el.secPanel.scrollTop;
}

function restoreSecScrollState(): void {
  window.setTimeout(() => {
    el.secPanel.scrollTop = secScrollTop;
  }, 0);
}

function handleDNSEvent(method: string, payload: unknown): void {
  if (method === "caches_update") {
    const event = parseDNSCacheEvent(payload);
    if (!event) return;
    if (event.subscription_id !== dnsCacheSubscriptionID) return;
    applyDNSCacheChangeToState(event.change);
    renderDNS();
    return;
  }
  if (method === "keys_update") {
    const event = parseDNSKeyEvent(payload);
    if (!event) return;
    const nodeKey = dnsNodeKeyForKeySubscription(event.subscription_id);
    if (!nodeKey) return;
    const snapshot = dnsKeysByCache.get(nodeKey) ?? { exists: true, keys: [] };
    dnsKeysByCache.set(nodeKey, applyDNSKeyChange(snapshot, event.change));
    renderDNS();
    return;
  }
  if (method === "value_update") {
    const event = parseDNSValueEvent(payload);
    if (!event) return;
    if (!dnsValuePanel || event.subscription_id !== dnsValuePanel.subscriptionID) return;
    dnsValuePanel.snapshot = event.value === undefined
      ? { found: event.found }
      : { found: event.found, value: event.value };
    renderDNSValuePanel();
  }
}

// DNS subscriptions follow the KV panel's scope: keep expanded/cache snapshots
// locally for quick return, but only hold backend subscriptions while the DNS
// tab is active and while a cache row is visibly expanded.
function activateDNSPanel(): void {
  renderDNS();
  restoreDNSScrollState();
  void ensureDNSCacheSubscription();
  syncDNSKeySubscriptions();
  if (dnsValuePanel && !dnsValuePanel.subscriptionID) {
    void subscribeDNSValuePanel();
  }
}

function deactivateDNSPanel(): void {
  rememberDNSScrollState();
  closeDNSValuePanel();
  if (dnsCacheSubscriptionID) {
    void tryBackend("dnsUnsubscribe", { subscriptionID: dnsCacheSubscriptionID });
    dnsCacheSubscriptionID = 0;
  }
  for (const id of dnsKeySubscriptions.values()) {
    void tryBackend("dnsUnsubscribe", { subscriptionID: id });
  }
  dnsKeySubscriptions.clear();
}

async function ensureDNSCacheSubscription(): Promise<void> {
  if (activeTab !== "dns" || dnsCacheSubscriptionID || dnsCacheSubscribePending) return;
  dnsCacheSubscribePending = true;
  const response = await tryBackend("dnsSubscribeCaches");
  dnsCacheSubscribePending = false;
  if (!response) {
    dnsNotice = { level: "error", text: "DNS cache storage is unavailable." };
    renderDNS();
    return;
  }
  if (activeTab !== "dns") {
    if (response.subscription_id) void tryBackend("dnsUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  dnsCacheSubscriptionID = response.subscription_id;
  applyDNSCachesSnapshot(response.snapshot);
}

function applyDNSCachesSnapshot(snapshot: DNSCachesSnapshot): void {
  dnsCaches.splice(0, dnsCaches.length, ...sortDNSCaches(snapshot.caches ?? []));
  renderDNS();
}

function applyDNSCacheChangeToState(change: DNSCacheChange): void {
  dnsCaches.splice(0, dnsCaches.length, ...applyDNSCacheChange(dnsCaches, change));
  if (!change.deleted) return;
  const nodeKey = dnsCacheNodeKey(change.name);
  dnsExpandedCaches.delete(nodeKey);
  dnsKeysByCache.delete(nodeKey);
  const subscriptionID = dnsKeySubscriptions.get(nodeKey);
  if (subscriptionID) void tryBackend("dnsUnsubscribe", { subscriptionID });
  dnsKeySubscriptions.delete(nodeKey);
  if (dnsValuePanel?.cache === change.name) closeDNSValuePanel();
}

function renderDNS(): void {
  const previousTop = el.dnsPanel.scrollTop;
  const tree = document.createElement("div");
  tree.className = "kv-tree-inner";
  tree.append(renderDNSHeader());
  if (dnsNotice) tree.append(renderDNSNotice(dnsNotice));

  const list = document.createElement("ol");
  list.className = "kv-store-list";
  if (!dnsCaches.length) {
    list.append(renderKVEmptyState(dnsCacheSubscribePending ? "Loading DNS caches." : "No DNS caches."));
  } else {
    for (const cache of dnsCaches) {
      list.append(renderDNSCache(cache));
    }
  }
  tree.append(list);
  el.dnsTree.replaceChildren(tree);
  el.dnsPanel.scrollTop = activeTab === "dns" ? previousTop : dnsScrollTop;
  syncDNSKeySubscriptions();
}

function renderDNSHeader(): HTMLElement {
  const header = document.createElement("header");
  header.className = "kv-header";
  header.append(textElement("h2", "DNS caches"));
  return header;
}

function renderDNSNotice(notice: DNSNotice): HTMLElement {
  const row = document.createElement("div");
  row.className = `kv-notice kv-notice-${notice.level}`;
  row.append(textElement("span", notice.text));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Dismiss";
  close.setAttribute("aria-label", "Dismiss DNS message");
  close.addEventListener("click", () => {
    dnsNotice = null;
    renderDNS();
  });
  row.append(close);
  return row;
}

function renderDNSCache(cache: DNSCacheInfo): HTMLLIElement {
  const nodeKey = dnsCacheNodeKey(cache);
  const expanded = dnsExpandedCaches.has(nodeKey);
  const item = document.createElement("li");
  item.className = "kv-store";
  const row = document.createElement("div");
  row.className = "kv-row kv-store-row";
  bindKVRowAction(row, `Toggle DNS cache ${cache.name}`, () => toggleDNSCache(nodeKey));
  row.append(createKVDisclosure(expanded, `Toggle DNS cache ${cache.name}`, () => toggleDNSCache(nodeKey)));
  row.append(createDNSCacheTitle(cache));
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const remove = textElement("button", "Delete", "danger");
  remove.type = "button";
  remove.title = "Delete DNS cache";
  remove.addEventListener("click", () => dropDNSCache(cache.name));
  actions.append(remove);
  row.append(actions);
  item.append(row);
  if (expanded) {
    item.append(renderDNSCacheBody(cache.name, nodeKey));
  }
  return item;
}

function renderDNSCacheBody(cache: string, nodeKey: string): HTMLElement {
  const body = document.createElement("div");
  body.className = "kv-node-body";
  body.append(renderDNSCacheToolbar(cache));
  const snapshot = dnsKeysByCache.get(nodeKey);
  if (!snapshot) {
    body.append(renderKVLoading());
    return body;
  }
  if (!snapshot.exists) {
    body.append(textElement("p", "Cache does not exist.", "kv-subtree-missing"));
    return body;
  }
  const keys = snapshot.keys ?? [];
  if (!keys.length) {
    body.append(textElement("p", "Empty cache.", "kv-subtree-empty"));
    return body;
  }
  const list = document.createElement("ol");
  list.className = "kv-entry-list";
  for (const entry of keys) {
    list.append(renderDNSKey(cache, entry));
  }
  body.append(list);
  return body;
}

function renderDNSCacheToolbar(cache: string): HTMLElement {
  const toolbar = document.createElement("div");
  toolbar.className = "kv-toolbar";
  const pathLabel = textElement("span", cache, "kv-path-label");
  pathLabel.title = cache;
  const addRecord = textElement("button", "+ Record");
  addRecord.type = "button";
  addRecord.title = "Create DNS record";
  addRecord.addEventListener("click", () => openDNSForm(cache));
  toolbar.append(pathLabel, addRecord);
  if (dnsOpenForm?.cache === cache) {
    const wrapper = document.createElement("div");
    wrapper.className = "kv-form-slot";
    wrapper.append(renderDNSRecordForm({
      initial: defaultDNSRecordInput(),
      submitLabel: "Create",
      onSubmit: (record) => setDNSRecord(cache, "", record),
      onCancel: closeDNSForm,
    }));
    toolbar.append(wrapper);
  }
  return toolbar;
}

// Key rows carry only metadata. Cached DNS message JSON is fetched through a
// value subscription after explicit selection, matching the KV disclosure rule.
function renderDNSKey(cache: string, entry: DNSKeyInfo): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "kv-entry kv-entry-value";
  const row = document.createElement("div");
  row.className = "kv-row";
  bindKVRowAction(row, `Open DNS value ${formatDNSCacheKey(entry.key)}`, () => openDNSValuePanel(cache, entry.key));
  row.append(createKVIndent());
  row.append(createDNSKeyTitle(entry));
  row.append(renderDNSKeyActions(cache, entry.key));
  if (dnsValuePanel?.cache === cache && dnsValuePanel.key === entry.key) {
    row.classList.add("kv-row-selected");
  }
  item.append(row);
  return item;
}

function renderDNSKeyActions(cache: string, key: string): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const remove = textElement("button", "Delete", "danger");
  remove.type = "button";
  remove.title = "Delete DNS record";
  remove.addEventListener("click", () => deleteDNSValue(cache, key));
  actions.append(remove);
  return actions;
}

function openDNSForm(cache: string): void {
  dnsOpenForm = { cache };
  renderDNS();
}

function closeDNSForm(): void {
  dnsOpenForm = null;
  renderDNS();
}

type DNSRecordFormOptions = {
  initial: DNSRecordInput;
  submitLabel: string;
  onSubmit: (record: DNSRecordInput) => void;
  onCancel: () => void;
};

function renderDNSRecordForm(options: DNSRecordFormOptions): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form dns-record-form";

  const name = document.createElement("input");
  name.name = "name";
  name.placeholder = "example.test.";
  name.autocomplete = "off";
  name.required = true;
  name.value = options.initial.name;

  const type = document.createElement("select");
  type.name = "type";
  appendSelectOptions(type, [...DNS_RECORD_TYPES], options.initial.type);

  const cls = document.createElement("select");
  cls.name = "class";
  appendSelectOptions(cls, [...DNS_RECORD_CLASSES], options.initial.class);

  const ttl = document.createElement("input");
  ttl.name = "ttl";
  ttl.type = "number";
  ttl.min = "1";
  ttl.max = "4294967295";
  ttl.step = "1";
  ttl.required = true;
  ttl.value = String(options.initial.ttl || 60);

  const value = document.createElement("textarea");
  value.name = "value";
  value.rows = 2;
  value.required = true;
  value.value = options.initial.value;

  const updateValuePlaceholder = () => {
    value.placeholder = dnsRecordValuePlaceholder(type.value);
  };
  type.addEventListener("change", updateValuePlaceholder);
  updateValuePlaceholder();

  form.append(
    dnsRecordField("Name", name, "dns-record-field-name"),
    dnsRecordField("Type", type),
    dnsRecordField("Class", cls),
    dnsRecordField("TTL", ttl),
    dnsRecordField("Value", value, "dns-record-field-value"),
  );

  const actions = document.createElement("div");
  actions.className = "dns-record-form-actions";
  const save = textElement("button", options.submitLabel);
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", options.onCancel);
  actions.append(save, cancel);
  form.append(actions);

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    options.onSubmit({
      name: name.value.trim(),
      type: type.value,
      class: cls.value,
      ttl: Math.trunc(Number(ttl.value)),
      value: value.value.trim(),
    });
  });
  window.setTimeout(() => name.focus(), 0);
  return form;
}

function dnsRecordField(label: string, control: HTMLElement, className = ""): HTMLLabelElement {
  const field = document.createElement("label");
  field.className = `dns-record-field ${className}`.trim();
  field.append(textElement("span", label), control);
  return field;
}

function appendSelectOptions(select: HTMLSelectElement, values: string[], selected: string): void {
  for (const value of values) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = value;
    select.append(option);
  }
  select.value = values.includes(selected) ? selected : values[0] ?? "";
}

function dnsRecordValuePlaceholder(type: string): string {
  switch (type) {
    case "A":
      return "192.0.2.10";
    case "AAAA":
      return "2001:db8::10";
    case "CNAME":
      return "target.example.test.";
    case "NS":
      return "ns1.example.test.";
    case "PTR":
      return "host.example.test.";
    case "MX":
      return "10 mail.example.test.";
    case "SRV":
      return "1 2 443 srv.example.test.";
    case "TXT":
      return "hello world";
    default:
      return "Record value";
  }
}

async function setDNSRecord(cache: string, previousKey: string, record: DNSRecordInput): Promise<void> {
  if (!record.name) {
    dnsNotice = { level: "error", text: "DNS record name is required." };
    renderDNS();
    return;
  }
  if (!Number.isInteger(record.ttl) || record.ttl <= 0) {
    dnsNotice = { level: "error", text: "DNS TTL must be greater than zero." };
    renderDNS();
    return;
  }
  const response = await runDNSMutation("dnsSet", {
    cache,
    key: previousKey,
    record,
  }, "DNS record saved.");
  if (!response) return;
  closeDNSForm();
  if (!previousKey || (dnsValuePanel?.cache === cache && (dnsValuePanel.key === previousKey || dnsValuePanel.key === response.key))) {
    closeDNSValuePanel();
    openDNSValuePanel(cache, response.key);
  }
}

async function deleteDNSValue(cache: string, key: string): Promise<void> {
  if (!window.confirm(`Delete DNS record "${formatDNSCacheKey(key)}"?`)) return;
  if (dnsValuePanel?.cache === cache && dnsValuePanel.key === key) {
    closeDNSValuePanel();
  }
  await runDNSMutation("dnsDelete", { cache, key }, "DNS record deleted.");
}

async function dropDNSCache(cache: string): Promise<void> {
  if (!window.confirm(`Delete DNS cache "${cache}"?`)) return;
  if (dnsValuePanel?.cache === cache) {
    closeDNSValuePanel();
  }
  dnsOpenForm = dnsOpenForm?.cache === cache ? null : dnsOpenForm;
  if (await runDNSMutation("dnsDrop", { cache }, "DNS cache deleted.")) {
    const nodeKey = dnsCacheNodeKey(cache);
    dnsExpandedCaches.delete(nodeKey);
    dnsKeysByCache.delete(nodeKey);
    renderDNS();
  }
}

async function runDNSMutation<M extends DNSMutationMethod>(
  method: M,
  params: Record<string, unknown>,
  success: string,
): Promise<BackendResultMap[M] | null> {
  try {
    const result = await backend(method, params);
    dnsNotice = { level: "info", text: success };
    renderDNS();
    return result;
  } catch (error) {
    dnsNotice = { level: "error", text: `${method}: ${errorMessage(error)}` };
    renderDNS();
    return null;
  }
}

function toggleDNSCache(nodeKey: string): void {
  if (dnsExpandedCaches.has(nodeKey)) {
    dnsExpandedCaches.delete(nodeKey);
  } else {
    dnsExpandedCaches.add(nodeKey);
  }
  renderDNS();
}

function syncDNSKeySubscriptions(): void {
  if (activeTab !== "dns") return;
  const desired = desiredDNSKeySubscriptionKeys(dnsCaches, dnsExpandedCaches);
  for (const [nodeKey, id] of [...dnsKeySubscriptions]) {
    if (desired.has(nodeKey)) continue;
    dnsKeySubscriptions.delete(nodeKey);
    void tryBackend("dnsUnsubscribe", { subscriptionID: id });
  }
  for (const nodeKey of desired) {
    if (!dnsKeySubscriptions.has(nodeKey) && !dnsKeySubscribePending.has(nodeKey)) {
      void subscribeDNSKeys(nodeKey);
    }
  }
}

async function subscribeDNSKeys(nodeKey: string): Promise<void> {
  const ref = parseDNSCacheNodeKey(nodeKey);
  if (!ref || dnsKeySubscribePending.has(nodeKey) || dnsKeySubscriptions.has(nodeKey)) return;
  dnsKeySubscribePending.add(nodeKey);
  const response = await tryBackend("dnsSubscribeKeys", { cache: ref.cache });
  dnsKeySubscribePending.delete(nodeKey);
  if (!response) {
    dnsNotice = { level: "error", text: `Failed to subscribe DNS cache ${ref.cache}.` };
    renderDNS();
    return;
  }
  const desired = activeTab === "dns" && desiredDNSKeySubscriptionKeys(dnsCaches, dnsExpandedCaches).has(nodeKey);
  if (!desired) {
    if (response.subscription_id) void tryBackend("dnsUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  dnsKeySubscriptions.set(nodeKey, response.subscription_id);
  dnsKeysByCache.set(nodeKey, response.snapshot);
  renderDNS();
}

function dnsNodeKeyForKeySubscription(subscriptionID: number): string | null {
  for (const [nodeKey, id] of dnsKeySubscriptions) {
    if (id === subscriptionID) return nodeKey;
  }
  return null;
}

function openDNSValuePanel(cache: string, key: string): void {
  closeDNSValuePanel();
  dnsValuePanel = {
    cache,
    key,
    subscriptionID: 0,
    snapshot: null,
    editing: false,
  };
  renderDNSValuePanel();
  void subscribeDNSValuePanel();
}

async function subscribeDNSValuePanel(): Promise<void> {
  if (!dnsValuePanel || activeTab !== "dns" || dnsValueSubscribePending || dnsValuePanel.subscriptionID) return;
  dnsValueSubscribePending = true;
  const target = {
    cache: dnsValuePanel.cache,
    key: dnsValuePanel.key,
  };
  const response = await tryBackend("dnsSubscribeValue", target);
  dnsValueSubscribePending = false;
  if (!response) {
    dnsNotice = { level: "error", text: "Failed to subscribe DNS value." };
    renderDNSValuePanel();
    return;
  }
  if (
    activeTab !== "dns" ||
    !dnsValuePanel ||
    dnsValuePanel.cache !== target.cache ||
    dnsValuePanel.key !== target.key
  ) {
    if (response.subscription_id) void tryBackend("dnsUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  dnsValuePanel.subscriptionID = response.subscription_id;
  dnsValuePanel.snapshot = response.snapshot;
  renderDNSValuePanel();
}

function renderDNSValuePanel(): void {
  el.dnsValuePanel.replaceChildren();
  el.dnsPanel.classList.toggle("kv-value-open", Boolean(dnsValuePanel));
  if (!dnsValuePanel) {
    el.dnsValuePanel.classList.add("hidden");
    return;
  }
  el.dnsValuePanel.classList.remove("hidden");
  const header = document.createElement("header");
  header.className = "kv-value-header";
  const title = document.createElement("div");
  const heading = textElement("h2", formatDNSCacheKey(dnsValuePanel.key));
  heading.title = dnsValuePanel.key;
  title.append(heading, textElement("p", dnsValuePanel.cache));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Close";
  close.setAttribute("aria-label", "Close DNS value panel");
  close.addEventListener("click", closeDNSValuePanel);
  header.append(title, close);
  el.dnsValuePanel.append(header);

  const snapshot = dnsValuePanel.snapshot;
  if (!snapshot) {
    el.dnsValuePanel.append(renderKVLoading());
    return;
  }
  if (!snapshot.found || !snapshot.value) {
    el.dnsValuePanel.append(textElement("p", "DNS value not found.", "kv-value-missing"));
    return;
  }
  el.dnsValuePanel.append(renderDNSValueDetail(snapshot.value));
}

function renderDNSValueDetail(value: DNSValue): HTMLElement {
  const detail = document.createElement("div");
  detail.className = "kv-value-detail";
  const meta = document.createElement("div");
  meta.className = "kv-value-meta";
  const actions = document.createElement("div");
  actions.className = "kv-value-actions";
  if (dnsValuePanel && !dnsValuePanel.editing) {
    const edit = textElement("button", "Edit");
    edit.type = "button";
    edit.title = "Edit DNS record";
    edit.addEventListener("click", () => {
      if (!dnsValuePanel) return;
      dnsValuePanel.editing = true;
      renderDNSValuePanel();
    });
    actions.append(edit);
  }
  const copy = textElement("button", "Copy");
  copy.type = "button";
  copy.title = "Copy DNS value JSON";
  copy.addEventListener("click", () => copyDNSValue(value));
  const download = textElement("button", "Download");
  download.type = "button";
  download.title = "Download DNS value JSON";
  download.addEventListener("click", () => downloadDNSValue(value));
  actions.append(copy, download);
  if (dnsValuePanel) {
    const remove = textElement("button", "Delete");
    remove.type = "button";
    remove.title = "Delete DNS record";
    remove.className = "danger";
    remove.addEventListener("click", () => {
      if (!dnsValuePanel) return;
      void deleteDNSValue(dnsValuePanel.cache, dnsValuePanel.key);
    });
    actions.append(remove);
  }
  meta.append(actions);
  detail.append(meta);
  if (dnsValuePanel?.editing) {
    detail.append(renderDNSRecordForm({
      initial: editableDNSRecordFromValue(dnsValuePanel.key, value),
      submitLabel: "Save",
      onSubmit: (record) => {
        if (!dnsValuePanel) return;
        void setDNSRecord(dnsValuePanel.cache, dnsValuePanel.key, record);
      },
      onCancel: () => {
        if (!dnsValuePanel) return;
        dnsValuePanel.editing = false;
        renderDNSValuePanel();
      },
    }));
  } else {
    detail.append(createDNSValuePreview(value, { limit: DNS_JSON_RENDER_LIMIT }));
  }
  return detail;
}

function closeDNSValuePanel(): void {
  if (dnsValuePanel?.subscriptionID) {
    void tryBackend("dnsUnsubscribe", { subscriptionID: dnsValuePanel.subscriptionID });
  }
  dnsValuePanel = null;
  dnsValueSubscribePending = false;
  renderDNSValuePanel();
}

async function copyDNSValue(value: DNSValue): Promise<void> {
  try {
    await navigator.clipboard.writeText(value.json ?? "");
    dnsNotice = { level: "info", text: "DNS value copied." };
  } catch (error) {
    dnsNotice = { level: "error", text: `Copy failed: ${errorMessage(error)}` };
  }
  renderDNS();
}

function downloadDNSValue(value: DNSValue): void {
  try {
    const blob = new Blob([value.json ?? ""], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${safeFilenamePart(dnsValuePanel?.key ?? "dns-value") || "dns-value"}.json`;
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    dnsNotice = { level: "info", text: "Download started." };
  } catch (error) {
    dnsNotice = { level: "error", text: `Download failed: ${errorMessage(error)}` };
  }
  renderDNS();
}

function rememberDNSScrollState(): void {
  dnsScrollTop = el.dnsPanel.scrollTop;
}

function restoreDNSScrollState(): void {
  window.setTimeout(() => {
    el.dnsPanel.scrollTop = dnsScrollTop;
  }, 0);
}

function handleTLSEvent(method: string, payload: unknown): void {
  if (method !== "update") return;
  const event = parseTLSEvent(payload);
  if (!event) return;
  if (event.subscription_id !== tlsSubscriptionID) return;
  applyTLSChangeToState(event.change);
  renderTLS();
}

// TLS config entries contain their editable payloads in the tab snapshot. Keep
// one tab-scoped subscription active and release it when the user leaves TLS.
function activateTLSPanel(): void {
  renderTLS();
  restoreTLSScrollState();
  void ensureTLSSubscription();
}

function deactivateTLSPanel(): void {
  rememberTLSScrollState();
  closeTLSValuePanel();
  if (tlsSubscriptionID) {
    void tryBackend("tlsUnsubscribe", { subscriptionID: tlsSubscriptionID });
    tlsSubscriptionID = 0;
  }
}

async function ensureTLSSubscription(): Promise<void> {
  if (activeTab !== "tls" || tlsSubscriptionID || tlsSubscribePending) return;
  tlsSubscribePending = true;
  const response = await tryBackend("tlsSubscribe");
  tlsSubscribePending = false;
  if (!response) {
    tlsNotice = { level: "error", text: "TLS config storage is unavailable." };
    renderTLS();
    return;
  }
  if (activeTab !== "tls") {
    if (response.subscription_id) void tryBackend("tlsUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  tlsSubscriptionID = response.subscription_id;
  applyTLSSnapshotToState(response.snapshot);
}

function applyTLSSnapshotToState(snapshot: TLSSnapshot): void {
  tlsSnapshot = {
    ...(snapshot.builtin_ca === undefined ? {} : { builtin_ca: snapshot.builtin_ca }),
    certs: sortTLSCerts(snapshot.certs ?? []),
    policies: sortTLSPolicies(snapshot.policies ?? []),
  };
  if (tlsValuePanel && !tlsSelectedEntry()) closeTLSValuePanel();
  renderTLS();
}

function applyTLSChangeToState(change: TLSChange): void {
  tlsSnapshot = applyTLSChange(tlsSnapshot, change);
  if (change.deleted && tlsValuePanel?.kind === change.kind && tlsValuePanel.name === change.name) {
    closeTLSValuePanel();
  }
}

function renderTLS(): void {
  const previousTop = el.tlsPanel.scrollTop;
  const tree = document.createElement("div");
  tree.className = "kv-tree-inner";
  tree.append(renderTLSHeader());
  if (tlsNotice) tree.append(renderTLSNotice(tlsNotice));
  tree.append(renderTLSBuiltinCABlock());
  if (tlsOpenForm) tree.append(tlsOpenForm.kind === "cert" ? renderTLSCertCreateForm() : renderTLSPolicyCreateForm());

  const certs = tlsSnapshot.certs ?? [];
  const policies = tlsSnapshot.policies ?? [];
  const list = document.createElement("ol");
  list.className = "kv-store-list tls-list";
  list.append(renderTLSCertSection(certs));
  list.append(renderTLSSection("Policies", policies, renderTLSPolicyRow, "No TLS policies."));
  tree.append(list);
  el.tlsTree.replaceChildren(tree);
  el.tlsPanel.scrollTop = activeTab === "tls" ? previousTop : tlsScrollTop;
  renderTLSValuePanel();
}

function renderTLSHeader(): HTMLElement {
  const header = document.createElement("header");
  header.className = "kv-header sec-header tls-header";
  header.append(textElement("h2", "TLS configs"));
  const actions = document.createElement("div");
  actions.className = "kv-row-actions tls-header-actions";
  const reloadOS = textElement("button", tlsReloadOSCertsPending ? "Reloading..." : "Reload OS certs");
  reloadOS.type = "button";
  reloadOS.title = "Reload OS certificate authorities";
  reloadOS.disabled = tlsReloadOSCertsPending;
  reloadOS.addEventListener("click", () => void reloadTLSOSCerts());
  const addCert = textElement("button", "+ Certificate");
  addCert.type = "button";
  addCert.title = "Create TLS certificate";
  addCert.addEventListener("click", () => {
    tlsOpenForm = { kind: "cert" };
    renderTLS();
  });
  const addPolicy = textElement("button", "+ Policy");
  addPolicy.type = "button";
  addPolicy.title = "Create TLS policy";
  addPolicy.addEventListener("click", () => {
    tlsOpenForm = { kind: "policy" };
    renderTLS();
  });
  actions.append(reloadOS, addCert, addPolicy);
  header.append(actions);
  return header;
}

function renderTLSBuiltinCABlock(): HTMLElement {
  const section = document.createElement("section");
  section.className = "tls-builtin-ca";
  const header = document.createElement("header");
  header.append(textElement("h3", "Built-in CA"));
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const info = tlsSnapshot.builtin_ca;
  if (info?.cert.pem) {
    const download = textElement("button", "Download PEM");
    download.type = "button";
    download.title = "Download built-in CA public certificate";
    download.addEventListener("click", () => downloadTLSBuiltinCAPublic(info));
    actions.append(download);
  }
  header.append(actions);
  section.append(header);

  if (!info) {
    section.append(renderKVLoading());
    return section;
  }

  section.append(renderTLSBuiltinCAFacts(info));
  if (info.immutable || info.regeneration_disabled) {
    section.append(textElement("p", "Protected config", "sec-form-hint"));
  } else {
    section.append(renderTLSBuiltinCAForm(info));
  }
  return section;
}

function renderTLSBuiltinCAFacts(info: TLSBuiltinCAInfo): HTMLElement {
  const options = info.options ?? {};
  const facts = document.createElement("dl");
  facts.className = "tls-fact-list tls-builtin-ca-facts";
  const validity = options.validity_days && options.validity_days > 0
    ? `${options.validity_days} day${options.validity_days === 1 ? "" : "s"}`
    : "none";
  const crypto = [
    options.key_algorithm || "ecdsa",
    options.curve,
    options.key_bits ? `${options.key_bits} bits` : "",
  ].filter(Boolean).join(" / ");
  for (const [label, value] of [
    ["Issuer", options.issuer || "Almagest"],
    ["Expiration", validity],
    ["Crypto", crypto],
    ["Fingerprint", info.cert.fingerprint],
    ["SPKI", info.cert.spki_fingerprint],
  ] as Array<[string, string | undefined]>) {
    if (!value) continue;
    facts.append(textElement("dt", label), textElement("dd", value));
  }
  return facts;
}

function renderTLSBuiltinCAForm(info: TLSBuiltinCAInfo): HTMLFormElement {
  const options = info.options ?? {};
  const form = document.createElement("form");
  form.className = "kv-inline-form tls-builtin-ca-form";
  const issuer = document.createElement("input");
  issuer.name = "issuer";
  issuer.autocomplete = "off";
  issuer.value = options.issuer || "Almagest";

  const validity = document.createElement("input");
  validity.name = "validity_days";
  validity.type = "number";
  validity.min = "0";
  validity.step = "1";
  validity.value = String(options.validity_days ?? 0);

  const algorithm = document.createElement("select");
  appendSelectOptions(algorithm, [...TLS_BUILTIN_CA_ALGORITHMS], options.key_algorithm || "ecdsa");

  const curve = document.createElement("select");
  appendSelectOptions(curve, [...TLS_BUILTIN_CA_CURVES], options.curve || "P256");

  const keyBits = document.createElement("input");
  keyBits.name = "key_bits";
  keyBits.type = "number";
  keyBits.min = "2048";
  keyBits.max = "8192";
  keyBits.step = "256";
  keyBits.value = String(options.key_bits || 3072);

  const setCryptoFieldState = () => {
    curve.disabled = algorithm.value !== "ecdsa";
    keyBits.disabled = algorithm.value !== "rsa";
  };
  algorithm.addEventListener("change", setCryptoFieldState);
  setCryptoFieldState();

  const actions = document.createElement("div");
  actions.className = "sec-form-actions";
  const save = textElement("button", "Regenerate");
  save.type = "submit";
  actions.append(save);

  form.append(
    secFormField("Issuer", issuer),
    secFormField("Validity days", validity),
    secFormField("Key algorithm", algorithm),
    secFormField("Curve", curve),
    secFormField("RSA bits", keyBits),
    actions,
  );
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const next: TLSBuiltinCAOptions = {
      issuer: issuer.value.trim(),
      validity_days: Number.parseInt(validity.value, 10) || 0,
      key_algorithm: algorithm.value,
    };
    if (algorithm.value === "ecdsa") next.curve = curve.value;
    if (algorithm.value === "rsa") next.key_bits = Number.parseInt(keyBits.value, 10) || 0;
    void setTLSBuiltinCAOptions(next);
  });
  return form;
}

function downloadTLSBuiltinCAPublic(info: TLSBuiltinCAInfo): void {
  try {
    const blob = new Blob([info.cert.pem ?? ""], { type: "application/x-pem-file;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${safeFilenamePart(info.cert.name) || "almagest-builtin-ca"}.pem`;
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    tlsNotice = { level: "info", text: "Download started." };
  } catch (error) {
    tlsNotice = { level: "error", text: `Download failed: ${errorMessage(error)}` };
  }
  renderTLS();
}

function renderTLSNotice(notice: TLSNotice): HTMLElement {
  const row = document.createElement("div");
  row.className = `kv-notice kv-notice-${notice.level}`;
  row.append(textElement("span", notice.text));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Dismiss";
  close.setAttribute("aria-label", "Dismiss TLS message");
  close.addEventListener("click", () => {
    tlsNotice = null;
    renderTLS();
  });
  row.append(close);
  return row;
}

function renderTLSSection<T>(
  titleText: string,
  entries: T[],
  renderEntry: (entry: T) => HTMLLIElement,
  emptyText: string,
): HTMLLIElement {
  const section = document.createElement("li");
  section.className = "kv-store tls-section";
  section.append(textElement("h3", titleText, "tls-section-title"));
  const list = document.createElement("ol");
  list.className = "kv-entry-list";
  if (!entries.length) {
    list.append(renderKVEmptyState(tlsSubscribePending ? "Loading TLS configs." : emptyText));
  } else {
    for (const entry of entries) list.append(renderEntry(entry));
  }
  section.append(list);
  return section;
}

function renderTLSCertSection(certs: TLSCertEntry[]): HTMLLIElement {
  const section = document.createElement("li");
  section.className = "kv-store tls-section";
  section.append(textElement("h3", "Certificates", "tls-section-title"));
  const list = document.createElement("ol");
  list.className = "kv-entry-list";

  if (!certs.length) {
    list.append(renderKVEmptyState(tlsSubscribePending ? "Loading TLS configs." : "No TLS certificates."));
  } else {
    const immutableCerts = certs.filter((cert) => isImmutableTLSCertName(cert.name));
    const userCerts = certs.filter((cert) => !isImmutableTLSCertName(cert.name));
    list.append(
      renderTLSCertGroup(
        TLS_IMMUTABLE_CERT_GROUP_KEY,
        "Built-in and OS certificates",
        immutableCerts,
        "No built-in or OS certificates.",
      ),
      renderTLSCertGroup(
        TLS_USER_CERT_GROUP_KEY,
        "User certificates",
        userCerts,
        "No user certificates.",
      ),
    );
  }

  section.append(list);
  return section;
}

function renderTLSCertGroup(
  groupKey: string,
  titleText: string,
  certs: TLSCertEntry[],
  emptyText: string,
): HTMLLIElement {
  const expanded = tlsExpandedCertGroups.has(groupKey);
  const item = document.createElement("li");
  item.className = "kv-entry tls-cert-group";
  const row = document.createElement("div");
  row.className = "kv-row kv-store-row tls-cert-group-row";
  bindKVRowAction(row, `Toggle ${titleText}`, () => toggleTLSCertGroup(groupKey));
  row.append(createKVDisclosure(expanded, `Toggle ${titleText}`, () => toggleTLSCertGroup(groupKey)));
  row.append(createTLSCertGroupTitle(titleText, certs.length));
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  row.append(actions);
  item.append(row);

  if (expanded) {
    const list = document.createElement("ol");
    list.className = "kv-entry-list";
    if (!certs.length) {
      list.append(renderKVEmptyState(emptyText));
    } else {
      for (const cert of certs) list.append(renderTLSCertRow(cert));
    }
    item.append(list);
  }

  return item;
}

function createTLSCertGroupTitle(titleText: string, count: number): HTMLElement {
  const title = document.createElement("div");
  title.className = "kv-store-title tls-cert-group-title";
  title.append(
    textElement("strong", titleText),
    textElement("em", `${count} cert${count === 1 ? "" : "s"}`),
  );
  return title;
}

function renderTLSCertRow(entry: TLSCertEntry): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "kv-entry kv-entry-value tls-cert";
  item.classList.toggle("tls-readonly", Boolean(entry.read_only));
  const row = document.createElement("div");
  row.className = "kv-row";
  bindKVRowAction(row, `Open TLS certificate ${entry.name}`, () => openTLSValuePanel("cert", entry.name));
  row.append(createKVIndent(), createTLSCertTitle(entry), renderTLSRowActions("cert", entry));
  if (tlsValuePanel?.kind === "cert" && tlsValuePanel.name === entry.name) row.classList.add("kv-row-selected");
  item.append(row);
  return item;
}

function renderTLSPolicyRow(entry: TLSPolicyEntry): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "kv-entry kv-entry-value tls-policy";
  item.classList.toggle("tls-readonly", Boolean(entry.read_only));
  item.classList.toggle("tls-policy-invalid-row", !entry.valid);
  const row = document.createElement("div");
  row.className = "kv-row";
  bindKVRowAction(row, `Open TLS policy ${entry.name}`, () => openTLSValuePanel("policy", entry.name));
  row.append(createKVIndent(), createTLSPolicyTitle(entry), renderTLSRowActions("policy", entry));
  if (tlsValuePanel?.kind === "policy" && tlsValuePanel.name === entry.name) row.classList.add("kv-row-selected");
  item.append(row);
  return item;
}

function renderTLSRowActions(kind: TLSItemKind, entry: TLSCertEntry | TLSPolicyEntry): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  if (!entry.read_only) {
    const remove = textElement("button", "Delete");
    remove.type = "button";
    remove.title = kind === "cert" ? "Delete TLS certificate" : "Delete TLS policy";
    remove.className = "danger";
    remove.addEventListener("click", () => deleteTLSEntry(kind, entry.name));
    actions.append(remove);
  }
  return actions;
}

function renderTLSCertCreateForm(): HTMLFormElement {
  return renderTLSCertForm({
    initialName: "",
    initialPEM: "",
    includeName: true,
    submitLabel: "Create",
    onSubmit: (name, pem) => createTLSCert(name, pem),
    onCancel: closeTLSForm,
  });
}

function renderTLSPolicyCreateForm(): HTMLFormElement {
  return renderTLSPolicyForm({
    initialName: "",
    initialSpec: emptyTLSPolicySpec(),
    includeName: true,
    submitLabel: "Create",
    onSubmit: (name, spec) => createTLSPolicy(name, spec),
    onCancel: closeTLSForm,
  });
}

type TLSCertFormOptions = {
  initialName: string;
  initialPEM: string;
  includeName: boolean;
  submitLabel: string;
  onSubmit(name: string, pem: string): void;
  onCancel(): void;
};

function renderTLSCertForm(options: TLSCertFormOptions): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form tls-cert-form";
  const name = document.createElement("input");
  name.name = "name";
  name.placeholder = "Certificate name";
  name.autocomplete = "off";
  name.required = true;
  name.value = options.initialName;
  const pem = document.createElement("textarea");
  pem.name = "pem";
  pem.placeholder = "-----BEGIN CERTIFICATE-----";
  pem.rows = 9;
  pem.required = true;
  pem.value = options.initialPEM;
  const pemEditor = document.createElement("div");
  pemEditor.className = "tls-cert-pem-editor";
  const upload = document.createElement("input");
  upload.type = "file";
  upload.accept = ".pem,.crt,.cer,application/x-pem-file,application/pem-certificate,text/plain";
  const hint = document.createElement("p");
  hint.className = "sec-form-hint";
  hint.append(
    document.createTextNode("Generate a self-signed certificate: "),
    textElement("code", TLS_CERT_OPENSSL_COMMAND),
    document.createTextNode(". Upload or paste tls.crt here."),
  );
  upload.addEventListener("change", () => {
    const file = upload.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.addEventListener("load", () => {
      pem.value = typeof reader.result === "string" ? reader.result : "";
      pem.focus();
    });
    reader.addEventListener("error", () => {
      tlsNotice = { level: "error", text: `Certificate file read failed: ${reader.error?.message ?? "unknown error"}` };
      renderTLS();
    });
    reader.readAsText(file);
  });
  pemEditor.append(upload, pem, hint);
  const actions = tlsEditActions(options.submitLabel, options.onCancel);
  if (options.includeName) form.append(secFormField("Name", name));
  form.append(secFormField("PEM", pemEditor, "sec-form-field-wide"), actions);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    options.onSubmit(name.value, pem.value);
  });
  window.setTimeout(() => (options.includeName ? name : pem).focus(), 0);
  return form;
}

type TLSPolicyFormOptions = {
  initialName: string;
  initialSpec: TLSPolicySpec;
  includeName: boolean;
  submitLabel: string;
  onSubmit(name: string, spec: TLSPolicySpec): void;
  onCancel(): void;
};

function renderTLSPolicyForm(options: TLSPolicyFormOptions): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form tls-policy-form";
  let mode: "form" | "json" = "form";
  let gui = createTLSPolicyGUIEditor(options.initialSpec);
  const name = document.createElement("input");
  name.name = "name";
  name.placeholder = "Policy name";
  name.autocomplete = "off";
  name.required = true;
  name.value = options.initialName;
  const modeToggle = document.createElement("div");
  modeToggle.className = "tls-policy-mode-toggle";
  const formMode = textElement("button", "Form");
  formMode.type = "button";
  formMode.setAttribute("aria-pressed", "true");
  const jsonMode = textElement("button", "JSON");
  jsonMode.type = "button";
  jsonMode.setAttribute("aria-pressed", "false");
  modeToggle.append(formMode, jsonMode);
  const guiField = secFormField("Policy rules", gui.root, "sec-form-field-wide tls-policy-gui-field");
  const spec = document.createElement("textarea");
  spec.name = "spec";
  spec.placeholder = '{"trust_anchors":[{"cert":"ca","scope":{"any":true}}]}';
  spec.rows = TLS_POLICY_JSON_ROWS;
  spec.required = true;
  spec.value = formatTLSPolicySpec(options.initialSpec);
  const jsonField = secFormField("Policy JSON", spec, "sec-form-field-wide tls-policy-json-field hidden");
  const actions = tlsEditActions(options.submitLabel, options.onCancel);
  if (options.includeName) form.append(secFormField("Name", name));
  form.append(modeToggle, guiField, jsonField, actions);
  const setMode = (nextMode: "form" | "json") => {
    if (nextMode === mode) return;
    if (nextMode === "json") {
      try {
        spec.value = formatTLSPolicySpec(gui.read());
      } catch {
        // Keep the last JSON text available when the form has incomplete rows.
      }
    } else {
      let parsed: TLSPolicySpec;
      try {
        parsed = parseTLSPolicySpec(spec.value);
      } catch (error) {
        tlsNotice = { level: "error", text: errorMessage(error) };
        renderTLS();
        return;
      }
      gui = createTLSPolicyGUIEditor(parsed);
      guiField.replaceChildren(textElement("span", "Policy rules"), gui.root);
    }
    mode = nextMode;
    formMode.classList.toggle("active", mode === "form");
    jsonMode.classList.toggle("active", mode === "json");
    formMode.setAttribute("aria-pressed", String(mode === "form"));
    jsonMode.setAttribute("aria-pressed", String(mode === "json"));
    guiField.classList.toggle("hidden", mode !== "form");
    jsonField.classList.toggle("hidden", mode !== "json");
    window.setTimeout(() => (mode === "form" ? gui.focus() : spec.focus()), 0);
  };
  formMode.classList.add("active");
  formMode.addEventListener("click", () => setMode("form"));
  jsonMode.addEventListener("click", () => setMode("json"));
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    let parsed: TLSPolicySpec;
    try {
      parsed = mode === "json" ? parseTLSPolicySpec(spec.value) : gui.read();
    } catch (error) {
      tlsNotice = { level: "error", text: errorMessage(error) };
      renderTLS();
      return;
    }
    options.onSubmit(name.value, parsed);
  });
  window.setTimeout(() => (options.includeName ? name : gui.focus()), 0);
  return form;
}

type TLSPolicyGUIEditor = {
  root: HTMLElement;
  read(): TLSPolicySpec;
  focus(): void;
};

type TLSPolicyRuleEditor<T> = {
  root: HTMLElement;
  read(): T | null;
  focus(): boolean;
};

function createTLSPolicyGUIEditor(spec: TLSPolicySpec): TLSPolicyGUIEditor {
  const root = document.createElement("div");
  root.className = "tls-policy-gui-editor";
  const certListID = `tls-policy-certs-${tlsPolicyEditorID}`;
  tlsPolicyEditorID += 1;
  const builtinCAName = tlsSnapshot.builtin_ca?.cert.name ?? "";
  const trustAnchors = (spec.trust_anchors ?? []).filter((rule) => (
    rule.cert !== builtinCAName && !isBuiltinTLSCertName(rule.cert)
  ));
  const certList = document.createElement("datalist");
  certList.id = certListID;
  for (const cert of tlsSnapshot.certs ?? []) {
    const option = document.createElement("option");
    option.value = cert.name;
    certList.append(option);
  }

  const trustEditors: Array<TLSPolicyRuleEditor<TLSTrustAnchor>> = [];
  const constraintEditors: Array<TLSPolicyRuleEditor<TLSAuthorityConstraint>> = [];
  const pinEditors: Array<TLSPolicyRuleEditor<TLSPin>> = [];
  root.append(certList);
  if (builtinCAName) root.append(createTLSBuiltinCAPolicyAnchor(builtinCAName));
  root.append(
    createTLSPolicyRuleSection("Trust anchors", "Add anchor", trustEditors, trustAnchors, (rule) => (
      createTLSTrustAnchorEditor(rule, certListID)
    )),
    createTLSPolicyRuleSection("Constraints", "Add constraint", constraintEditors, spec.constraints ?? [], (rule) => (
      createTLSConstraintEditor(rule, certListID)
    )),
    createTLSPolicyRuleSection("Pins", "Add pin", pinEditors, spec.pins ?? [], createTLSPinEditor),
  );

  return {
    root,
    read: () => {
      const trust_anchors = readTLSPolicyRuleEditors(trustEditors);
      const constraints = readTLSPolicyRuleEditors(constraintEditors);
      const pins = readTLSPolicyRuleEditors(pinEditors);
      const out: TLSPolicySpec = {};
      if (trust_anchors.length) out.trust_anchors = trust_anchors;
      if (constraints.length) out.constraints = constraints;
      if (pins.length) out.pins = pins;
      return out;
    },
    focus: () => {
      for (const editors of [trustEditors, constraintEditors, pinEditors]) {
        for (const editor of editors) {
          if (editor.focus()) return;
        }
      }
    },
  };
}

function createTLSBuiltinCAPolicyAnchor(certName: string): HTMLElement {
  const root = document.createElement("article");
  root.className = "tls-policy-rule-card tls-policy-builtin-anchor";
  root.append(textElement("h4", "Built-in trust anchor"));
  const fields = document.createElement("div");
  fields.className = "tls-policy-rule-fields";
  const cert = document.createElement("input");
  cert.value = certName;
  cert.disabled = true;
  const scope = document.createElement("input");
  scope.value = "any server";
  scope.disabled = true;
  fields.append(secFormField("Certificate", cert), secFormField("Scope", scope));
  root.append(fields);
  return root;
}

function createTLSPolicyRuleSection<T>(
  titleText: string,
  addText: string,
  editors: Array<TLSPolicyRuleEditor<T>>,
  rules: T[],
  createEditor: (rule?: T) => TLSPolicyRuleEditor<T>,
): HTMLElement {
  const section = document.createElement("section");
  section.className = "tls-policy-edit-section";
  const header = document.createElement("header");
  header.append(textElement("h3", titleText));
  const add = textElement("button", addText);
  add.type = "button";
  const list = document.createElement("div");
  list.className = "tls-policy-edit-list";
  const appendEditor = (rule?: T) => {
    const editor = createEditor(rule);
    editors.push(editor);
    list.append(editor.root);
    editor.focus();
  };
  add.addEventListener("click", () => appendEditor());
  header.append(add);
  section.append(header, tlsPolicyHelp(tlsPolicySectionHelp(titleText)), list);
  for (const rule of rules) appendEditor(rule);
  if (!rules.length && titleText === "Trust anchors") appendEditor();
  return section;
}

function createTLSTrustAnchorEditor(rule: TLSTrustAnchor | undefined, certListID: string): TLSPolicyRuleEditor<TLSTrustAnchor> {
  const card = createTLSPolicyRuleCard("Trust anchor");
  const cert = tlsPolicyTextInput("Certificate", "Cert name", rule?.cert ?? "", certListID);
  const scope = createTLSScopeEditor(rule?.scope);
  card.fields.append(cert.field, scope.root);
  return {
    root: card.root,
    read: () => {
      const certName = cert.input.value.trim();
      if (!certName && scope.isBlank()) return null;
      if (!certName) throw new Error("Trust anchor certificate is required.");
      return { cert: certName, scope: scope.read() };
    },
    focus: () => {
      cert.input.focus();
      return true;
    },
  };
}

function createTLSConstraintEditor(
  rule: TLSAuthorityConstraint | undefined,
  certListID: string,
): TLSPolicyRuleEditor<TLSAuthorityConstraint> {
  const card = createTLSPolicyRuleCard("Constraint");
  const cert = tlsPolicyTextInput("Certificate", "Cert name", rule?.cert ?? "", certListID);
  const match = document.createElement("select");
  appendTLSSelectOptions(match, [...TLS_AUTHORITY_MATCHES], rule?.match || "certificate");
  const scope = createTLSScopeEditor(rule?.scope);
  card.fields.append(cert.field, secFormField("Match", match), scope.root);
  return {
    root: card.root,
    read: () => {
      const certName = cert.input.value.trim();
      if (!certName && scope.isBlank()) return null;
      if (!certName) throw new Error("Constraint certificate is required.");
      return { cert: certName, match: match.value, scope: scope.read() };
    },
    focus: () => {
      cert.input.focus();
      return true;
    },
  };
}

function createTLSPinEditor(rule?: TLSPin): TLSPolicyRuleEditor<TLSPin> {
  const card = createTLSPolicyRuleCard("Pin");
  const kind = document.createElement("select");
  appendTLSSelectOptions(kind, [...TLS_PIN_KINDS], rule?.kind || "spki");
  const fingerprint = tlsPolicyTextInput("Fingerprint", "sha256 fingerprint", rule?.fingerprint ?? "");
  const scope = createTLSScopeEditor(rule?.scope);
  card.fields.append(secFormField("Kind", kind), fingerprint.field, scope.root);
  return {
    root: card.root,
    read: () => {
      const value = fingerprint.input.value.trim();
      if (!value && scope.isBlank()) return null;
      if (!value) throw new Error("Pin fingerprint is required.");
      return { kind: kind.value, fingerprint: value, scope: scope.read() };
    },
    focus: () => {
      fingerprint.input.focus();
      return true;
    },
  };
}

function createTLSPolicyRuleCard(titleText: string): { root: HTMLElement; fields: HTMLElement } {
  const root = document.createElement("article");
  root.className = "tls-policy-rule-card";
  const header = document.createElement("header");
  header.append(textElement("h4", titleText));
  const remove = textElement("button", "Remove");
  remove.type = "button";
  remove.addEventListener("click", () => root.remove());
  header.append(remove);
  const fields = document.createElement("div");
  fields.className = "tls-policy-rule-fields";
  root.append(header, fields);
  return { root, fields };
}

function createTLSScopeEditor(initial: TLSScope | undefined): {
  root: HTMLElement;
  read(): TLSScope;
  isBlank(): boolean;
} {
  const root = document.createElement("div");
  root.className = "tls-policy-scope-editor";
  const anyLabel = document.createElement("label");
  anyLabel.className = "tls-policy-checkbox";
  const any = document.createElement("input");
  any.type = "checkbox";
  any.checked = Boolean(initial?.any);
  anyLabel.append(any, textElement("span", "Any server"));
  const dns = document.createElement("textarea");
  dns.rows = 2;
  dns.placeholder = "example.test";
  dns.value = (initial?.dns ?? []).map(formatTLSDNSRuleInput).join("\n");
  const ip = document.createElement("textarea");
  ip.rows = 2;
  ip.placeholder = "192.0.2.0/24";
  ip.value = (initial?.ip_prefixes ?? []).join("\n");
  const setScopeFieldState = () => {
    dns.disabled = any.checked;
    ip.disabled = any.checked;
  };
  any.addEventListener("change", setScopeFieldState);
  setScopeFieldState();
  root.append(
    anyLabel,
    secFormField("DNS names", dns),
    tlsPolicyHelp("Limit this rule to these server names. Use *.example.test to include subdomains."),
    secFormField("IP prefixes", ip),
    tlsPolicyHelp("Limit this rule to servers in these IP ranges. A single IP address is also accepted."),
  );
  const scopeIsBlank = () => !any.checked && !splitTLSPolicyLines(dns.value).length && !splitTLSPolicyLines(ip.value).length;
  return {
    root,
    read: () => {
      if (any.checked) return { any: true };
      const dnsRules = splitTLSPolicyLines(dns.value).map(parseTLSDNSRuleInput);
      const ipPrefixes = splitTLSPolicyLines(ip.value);
      if (!dnsRules.length && !ipPrefixes.length) throw new Error("Scope needs any server, DNS names, or IP prefixes.");
      const scope: TLSScope = {};
      if (dnsRules.length) scope.dns = dnsRules;
      if (ipPrefixes.length) scope.ip_prefixes = ipPrefixes;
      return scope;
    },
    isBlank: scopeIsBlank,
  };
}

function tlsPolicyTextInput(label: string, placeholder: string, value: string, listID = ""): {
  field: HTMLLabelElement;
  input: HTMLInputElement;
} {
  const input = document.createElement("input");
  input.placeholder = placeholder;
  input.autocomplete = "off";
  input.value = value;
  if (listID) input.setAttribute("list", listID);
  return { field: secFormField(label, input), input };
}

function readTLSPolicyRuleEditors<T>(editors: Array<TLSPolicyRuleEditor<T>>): T[] {
  const out: T[] = [];
  for (const editor of editors) {
    if (!editor.root.isConnected) continue;
    const rule = editor.read();
    if (rule) out.push(rule);
  }
  return out;
}

function appendTLSSelectOptions(select: HTMLSelectElement, values: string[], selected: string): void {
  appendSelectOptions(select, values, values.includes(selected) ? selected : values[0] ?? "");
  if (selected && !values.includes(selected)) {
    const option = document.createElement("option");
    option.value = selected;
    option.textContent = selected;
    select.append(option);
    select.value = selected;
  }
}

function formatTLSDNSRuleInput(rule: TLSDNSRule): string {
  return rule.include_subdomains ? `*.${rule.domain}` : rule.domain;
}

function parseTLSDNSRuleInput(value: string): TLSDNSRule {
  const trimmed = value.trim();
  if (trimmed.startsWith("*.")) return { domain: trimmed.slice(2), include_subdomains: true };
  return { domain: trimmed };
}

function splitTLSPolicyLines(value: string): string[] {
  return value.split(/[\n,]+/).map((part) => part.trim()).filter(Boolean);
}

function tlsPolicySectionHelp(titleText: string): string {
  switch (titleText) {
    case "Trust anchors":
      return "Accept server certificates that chain to the selected certificate in the selected scope.";
    case "Constraints":
      return "Require certificates in the selected scope to also match this certificate authority.";
    case "Pins":
      return "Require certificates in the selected scope to match this certificate or public key fingerprint.";
    default:
      return "";
  }
}

function tlsPolicyHelp(text: string): HTMLElement {
  return textElement("p", text, "tls-policy-help");
}

function renderTLSRenameForm(kind: TLSItemKind, name: string): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form sec-rename-form tls-rename-form";
  const input = document.createElement("input");
  input.name = "name";
  input.autocomplete = "off";
  input.required = true;
  input.value = name;
  const actions = tlsEditActions("Save", () => setTLSEditor(null));
  form.append(secFormField("Name", input), actions);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void renameTLSEntry(kind, name, input.value);
  });
  window.setTimeout(() => input.focus(), 0);
  return form;
}

function tlsEditActions(submitLabel: string, onCancel: () => void): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "sec-form-actions";
  const save = textElement("button", submitLabel);
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", onCancel);
  actions.append(save, cancel);
  return actions;
}

function closeTLSForm(): void {
  tlsOpenForm = null;
  renderTLS();
}

function toggleTLSCertGroup(groupKey: string): void {
  if (tlsExpandedCertGroups.has(groupKey)) {
    tlsExpandedCertGroups.delete(groupKey);
  } else {
    tlsExpandedCertGroups.add(groupKey);
  }
  renderTLS();
}

async function createTLSCert(rawName: string, pem: string): Promise<void> {
  const name = rawName.trim();
  if (!isMutableTLSCertName(name)) {
    tlsNotice = { level: "error", text: "Certificate name is required and must not start with os-ca:." };
    renderTLS();
    return;
  }
  const entry = await runTLSMutation("tlsSetCert", { name, pem }, "TLS certificate saved.");
  if (!entry) return;
  upsertTLSCert(entry);
  tlsOpenForm = null;
  openTLSValuePanel("cert", entry.name);
}

async function createTLSPolicy(rawName: string, spec: TLSPolicySpec): Promise<void> {
  const name = rawName.trim();
  if (!isMutableTLSPolicyName(name)) {
    tlsNotice = { level: "error", text: "Policy name is required and must not be default." };
    renderTLS();
    return;
  }
  const entry = await runTLSMutation("tlsSetPolicy", { name, spec }, "TLS policy saved.");
  if (!entry) return;
  upsertTLSPolicy(entry);
  tlsOpenForm = null;
  openTLSValuePanel("policy", entry.name);
}

async function setTLSBuiltinCAOptions(options: TLSBuiltinCAOptions): Promise<void> {
  const info = await runTLSMutation(
    "tlsSetBuiltinCA",
    { options },
    "Built-in TLS CA updated.",
  );
  if (!info) return;
  upsertTLSBuiltinCA(info);
  renderTLS();
}

async function reloadTLSOSCerts(): Promise<void> {
  if (tlsReloadOSCertsPending) return;
  tlsReloadOSCertsPending = true;
  tlsNotice = null;
  renderTLS();
  try {
    const snapshot = await backend("tlsReloadOSCerts", {});
    tlsNotice = { level: "info", text: "OS TLS certificates reloaded." };
    applyTLSSnapshotToState(snapshot);
  } catch (error) {
    tlsNotice = { level: "error", text: `tlsReloadOSCerts: ${errorMessage(error)}` };
    renderTLS();
  } finally {
    tlsReloadOSCertsPending = false;
    renderTLS();
  }
}

function openTLSValuePanel(kind: TLSItemKind, name: string): void {
  tlsValuePanel = {
    kind,
    name,
    editing: null,
  };
  renderTLS();
}

function closeTLSValuePanel(): void {
  tlsValuePanel = null;
  renderTLSValuePanel();
}

function renderTLSValuePanel(): void {
  el.tlsValuePanel.replaceChildren();
  el.tlsPanel.classList.toggle("kv-value-open", Boolean(tlsValuePanel));
  if (!tlsValuePanel) {
    el.tlsValuePanel.classList.add("hidden");
    return;
  }
  el.tlsValuePanel.classList.remove("hidden");
  const entry = tlsSelectedEntry();
  if (!entry) {
    el.tlsValuePanel.append(textElement("p", "TLS config not found.", "kv-value-missing"));
    return;
  }
  if (entry.read_only && tlsValuePanel.editing) tlsValuePanel.editing = null;

  const header = document.createElement("header");
  header.className = "kv-value-header";
  const title = document.createElement("div");
  const meta = tlsValuePanel.kind === "cert"
    ? formatTLSCertMeta(entry as TLSCertEntry)
    : formatTLSPolicyMeta(entry as TLSPolicyEntry);
  title.append(textElement("h2", entry.name), textElement("p", meta));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Close";
  close.setAttribute("aria-label", "Close TLS config panel");
  close.addEventListener("click", closeTLSValuePanel);
  header.append(title, close);
  el.tlsValuePanel.append(header, renderTLSDetail(entry));
}

function renderTLSDetail(entry: TLSCertEntry | TLSPolicyEntry): HTMLElement {
  const detail = document.createElement("div");
  detail.className = "kv-value-detail";
  const meta = document.createElement("div");
  meta.className = "kv-value-meta";
  meta.append(renderTLSValueActions(entry));
  detail.append(meta);
  if (!tlsValuePanel) return detail;
  if (tlsValuePanel.editing === "rename") {
    detail.append(renderTLSRenameForm(tlsValuePanel.kind, entry.name));
  } else if (tlsValuePanel.editing === "value" && tlsValuePanel.kind === "cert") {
    detail.append(renderTLSCertForm({
      initialName: entry.name,
      initialPEM: (entry as TLSCertEntry).pem ?? "",
      includeName: false,
      submitLabel: "Save",
      onSubmit: (_name, pem) => updateTLSCert(entry.name, pem),
      onCancel: () => setTLSEditor(null),
    }));
  } else if (tlsValuePanel.editing === "value" && tlsValuePanel.kind === "policy") {
    detail.append(renderTLSPolicyForm({
      initialName: entry.name,
      initialSpec: (entry as TLSPolicyEntry).spec,
      includeName: false,
      submitLabel: "Save",
      onSubmit: (_name, spec) => updateTLSPolicy(entry.name, spec),
      onCancel: () => setTLSEditor(null),
    }));
  } else if (tlsValuePanel.kind === "cert") {
    detail.append(createTLSCertPreview(entry as TLSCertEntry));
  } else {
    detail.append(createTLSPolicyPreview(entry as TLSPolicyEntry));
  }
  return detail;
}

function renderTLSValueActions(entry: TLSCertEntry | TLSPolicyEntry): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-value-actions";
  const copy = textElement("button", tlsValuePanel?.kind === "cert" ? "Copy PEM" : "Copy JSON");
  copy.type = "button";
  copy.title = tlsValuePanel?.kind === "cert" ? "Copy certificate PEM" : "Copy policy JSON";
  copy.addEventListener("click", () => copyTLSEntry(entry));
  const download = textElement("button", "Download");
  download.type = "button";
  download.title = tlsValuePanel?.kind === "cert" ? "Download certificate PEM" : "Download policy JSON";
  download.addEventListener("click", () => downloadTLSEntry(entry));
  actions.append(copy, download);
  if (!entry.read_only) {
    const edit = textElement("button", "Edit");
    edit.type = "button";
    edit.title = tlsValuePanel?.kind === "cert" ? "Edit certificate PEM" : "Edit policy JSON";
    edit.addEventListener("click", () => setTLSEditor("value"));
    const rename = textElement("button", "Rename");
    rename.type = "button";
    rename.title = "Rename TLS config";
    rename.addEventListener("click", () => setTLSEditor("rename"));
    const remove = textElement("button", "Delete");
    remove.type = "button";
    remove.title = "Delete TLS config";
    remove.className = "danger";
    remove.addEventListener("click", () => {
      if (tlsValuePanel) void deleteTLSEntry(tlsValuePanel.kind, entry.name);
    });
    actions.append(edit, rename, remove);
  }
  return actions;
}

function setTLSEditor(editor: TLSValuePanelState["editing"]): void {
  if (!tlsValuePanel) return;
  tlsValuePanel.editing = editor;
  renderTLSValuePanel();
}

async function updateTLSCert(name: string, pem: string): Promise<void> {
  const entry = await runTLSMutation("tlsSetCert", { name, pem }, "TLS certificate saved.");
  if (!entry) return;
  upsertTLSCert(entry);
  if (tlsValuePanel) tlsValuePanel.editing = null;
  renderTLS();
}

async function updateTLSPolicy(name: string, spec: TLSPolicySpec): Promise<void> {
  const entry = await runTLSMutation("tlsSetPolicy", { name, spec }, "TLS policy saved.");
  if (!entry) return;
  upsertTLSPolicy(entry);
  if (tlsValuePanel) tlsValuePanel.editing = null;
  renderTLS();
}

async function renameTLSEntry(kind: TLSItemKind, name: string, rawNewName: string): Promise<void> {
  const newName = rawNewName.trim();
  const valid = kind === "cert" ? isMutableTLSCertName(newName) : isMutableTLSPolicyName(newName);
  if (!valid) {
    tlsNotice = {
      level: "error",
      text: kind === "cert"
        ? "Certificate name is required and must not start with os-ca:."
        : "Policy name is required and must not be default.",
    };
    renderTLS();
    return;
  }
  const method = kind === "cert" ? "tlsRenameCert" : "tlsRenamePolicy";
  const entry = await runTLSMutation(method, { name, newName }, "TLS config renamed.");
  if (!entry) return;
  if (kind === "cert") {
    removeTLSCert(name);
    upsertTLSCert(entry as TLSCertEntry);
  } else {
    removeTLSPolicy(name);
    upsertTLSPolicy(entry as TLSPolicyEntry);
  }
  if (tlsValuePanel) {
    tlsValuePanel.name = entry.name;
    tlsValuePanel.editing = null;
  }
  renderTLS();
}

async function deleteTLSEntry(kind: TLSItemKind, name: string): Promise<void> {
  const label = kind === "cert" ? "certificate" : "policy";
  if (!window.confirm(`Delete TLS ${label} "${name}"?`)) return;
  const method = kind === "cert" ? "tlsDeleteCert" : "tlsDeletePolicy";
  if (await runTLSMutation(method, { name }, "TLS config deleted.")) {
    if (kind === "cert") removeTLSCert(name);
    else removeTLSPolicy(name);
    if (tlsValuePanel?.kind === kind && tlsValuePanel.name === name) closeTLSValuePanel();
    renderTLS();
  }
}

async function runTLSMutation<M extends TLSMutationMethod>(
  method: M,
  params: Record<string, unknown>,
  success: string,
): Promise<BackendResultMap[M] | null> {
  try {
    const result = await backend(method, params);
    tlsNotice = { level: "info", text: success };
    renderTLS();
    return result;
  } catch (error) {
    tlsNotice = { level: "error", text: `${method}: ${errorMessage(error)}` };
    renderTLS();
    return null;
  }
}

function tlsSelectedEntry(): TLSCertEntry | TLSPolicyEntry | null {
  if (!tlsValuePanel) return null;
  return tlsValuePanel.kind === "cert"
    ? tlsCertByName(tlsValuePanel.name)
    : tlsPolicyByName(tlsValuePanel.name);
}

function tlsCertByName(name: string): TLSCertEntry | null {
  return (tlsSnapshot.certs ?? []).find((entry) => entry.name === name) ?? null;
}

function tlsPolicyByName(name: string): TLSPolicyEntry | null {
  return (tlsSnapshot.policies ?? []).find((entry) => entry.name === name) ?? null;
}

function upsertTLSCert(entry: TLSCertEntry): void {
  tlsSnapshot = {
    ...tlsSnapshot,
    certs: sortTLSCerts([...(tlsSnapshot.certs ?? []).filter((cert) => cert.name !== entry.name), entry]),
  };
}

function upsertTLSPolicy(entry: TLSPolicyEntry): void {
  tlsSnapshot = {
    ...tlsSnapshot,
    policies: sortTLSPolicies([...(tlsSnapshot.policies ?? []).filter((policy) => policy.name !== entry.name), entry]),
  };
}

function upsertTLSBuiltinCA(info: TLSBuiltinCAInfo): void {
  tlsSnapshot = {
    ...tlsSnapshot,
    builtin_ca: info,
    certs: sortTLSCerts([...(tlsSnapshot.certs ?? []).filter((cert) => cert.name !== info.cert.name), info.cert]),
  };
}

function removeTLSCert(name: string): void {
  tlsSnapshot = {
    ...tlsSnapshot,
    certs: (tlsSnapshot.certs ?? []).filter((entry) => entry.name !== name),
  };
}

function removeTLSPolicy(name: string): void {
  tlsSnapshot = {
    ...tlsSnapshot,
    policies: (tlsSnapshot.policies ?? []).filter((entry) => entry.name !== name),
  };
}

async function copyTLSEntry(entry: TLSCertEntry | TLSPolicyEntry): Promise<void> {
  try {
    await navigator.clipboard.writeText(tlsEntryText(entry));
    tlsNotice = { level: "info", text: "TLS config copied." };
  } catch (error) {
    tlsNotice = { level: "error", text: `Copy failed: ${errorMessage(error)}` };
  }
  renderTLS();
}

function downloadTLSEntry(entry: TLSCertEntry | TLSPolicyEntry): void {
  try {
    const isCert = tlsValuePanel?.kind === "cert";
    const text = tlsEntryText(entry);
    const blob = new Blob([text], { type: isCert ? "application/x-pem-file;charset=utf-8" : "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${safeFilenamePart(entry.name) || "tls-config"}.${isCert ? "pem" : "json"}`;
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    tlsNotice = { level: "info", text: "Download started." };
  } catch (error) {
    tlsNotice = { level: "error", text: `Download failed: ${errorMessage(error)}` };
  }
  renderTLS();
}

function tlsEntryText(entry: TLSCertEntry | TLSPolicyEntry): string {
  if (tlsValuePanel?.kind === "cert") return (entry as TLSCertEntry).pem ?? "";
  return formatTLSPolicySpec((entry as TLSPolicyEntry).spec);
}

function rememberTLSScrollState(): void {
  tlsScrollTop = el.tlsPanel.scrollTop;
}

function restoreTLSScrollState(): void {
  window.setTimeout(() => {
    el.tlsPanel.scrollTop = tlsScrollTop;
  }, 0);
}

function handleKVEvent(method: string, payload: unknown): void {
  if (method === "stores_update") {
    const event = parseKVStoresEvent(payload);
    if (!event) return;
    if (event.subscription_id !== kvStoreSubscriptionID) {
      if (kvStoreSubscribePending) kvPendingStoreSnapshots.set(event.subscription_id, event.snapshot);
      return;
    }
    applyKVStoresSnapshot(event.snapshot);
    return;
  }
  if (method === "entries_update") {
    const event = parseKVEntriesEvent(payload);
    if (!event) return;
    const nodeKey = kvNodeKeyForEntrySubscription(event.subscription_id);
    if (!nodeKey) {
      if (kvEntrySubscribePending.size) kvPendingEntrySnapshots.set(event.subscription_id, event.snapshot);
      return;
    }
    kvEntriesByNode.set(nodeKey, event.snapshot);
    renderKV();
    return;
  }
  if (method === "value_update") {
    const event = parseKVValueEvent(payload);
    if (!event) return;
    if (!kvValuePanel || event.subscription_id !== kvValuePanel.subscriptionID) {
      if (kvValueSubscribePending) kvPendingValueSnapshots.set(event.subscription_id, event.snapshot);
      return;
    }
    kvValuePanel.snapshot = event.snapshot;
    renderKVValuePanel();
  }
}

// KV subscriptions are deliberately tab-scoped. The expanded tree and cached
// snapshots stay in memory for quick return, but every live backend
// subscription is released when the user leaves the KV tab.
function activateKVPanel(): void {
  renderKV();
  restoreKVScrollState();
  void ensureKVStoreSubscription();
  syncKVEntrySubscriptions();
  if (kvValuePanel && !kvValuePanel.subscriptionID) {
    void subscribeKVValuePanel();
  }
}

function deactivateKVPanel(): void {
  rememberKVScrollState();
  closeKVValuePanel();
  if (kvStoreSubscriptionID) {
    void tryBackend("kvUnsubscribe", { subscriptionID: kvStoreSubscriptionID });
    kvStoreSubscriptionID = 0;
  }
  for (const id of kvEntrySubscriptions.values()) {
    void tryBackend("kvUnsubscribe", { subscriptionID: id });
  }
  kvEntrySubscriptions.clear();
  kvPendingStoreSnapshots.clear();
  kvPendingEntrySnapshots.clear();
  kvPendingValueSnapshots.clear();
}

async function ensureKVStoreSubscription(): Promise<void> {
  if (activeTab !== "kv" || kvStoreSubscriptionID || kvStoreSubscribePending) return;
  kvStoreSubscribePending = true;
  const response = await tryBackend("kvSubscribeStores");
  kvStoreSubscribePending = false;
  if (!response) {
    kvPendingStoreSnapshots.clear();
    kvNotice = { level: "error", text: "KV storage is unavailable." };
    renderKV();
    return;
  }
  if (activeTab !== "kv") {
    kvPendingStoreSnapshots.delete(response.subscription_id);
    if (response.subscription_id) void tryBackend("kvUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  kvStoreSubscriptionID = response.subscription_id;
  applyKVStoresSnapshot(takePendingKVSnapshot(kvPendingStoreSnapshots, response.subscription_id, response.snapshot));
  kvPendingStoreSnapshots.clear();
}

function applyKVStoresSnapshot(snapshot: KVStoresSnapshot): void {
  kvStores.splice(0, kvStores.length, ...sortKVStores(snapshot.stores.map(normalizeKVStore)));
  renderKV();
}

function sortKVStores(stores: KVStoreInfo[]): KVStoreInfo[] {
  return [...stores].sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === "default" ? -1 : 1;
    return (left.name ?? "").localeCompare(right.name ?? "");
  });
}

function renderKV(): void {
  const previousTop = el.kvPanel.scrollTop;
  const tree = document.createElement("div");
  tree.className = "kv-tree-inner";
  tree.append(renderKVHeader());
  if (kvNotice) tree.append(renderKVNotice(kvNotice));

  const list = document.createElement("ol");
  list.className = "kv-store-list";
  if (!kvStores.length) {
    list.append(renderKVEmptyState(kvStoreSubscribePending ? "Loading KV stores." : "No KV stores."));
  } else {
    for (const store of kvStores) {
      list.append(renderKVStore(store));
    }
  }
  tree.append(list);
  el.kvTree.replaceChildren(tree);
  el.kvPanel.scrollTop = activeTab === "kv" ? previousTop : kvScrollTop;
  syncKVEntrySubscriptions();
}

function renderKVHeader(): HTMLElement {
  const header = document.createElement("header");
  header.className = "kv-header";
  header.append(textElement("h2", "KV stores"));
  return header;
}

function renderKVNotice(notice: KVNotice): HTMLElement {
  const row = document.createElement("div");
  row.className = `kv-notice kv-notice-${notice.level}`;
  row.append(textElement("span", notice.text));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Dismiss";
  close.setAttribute("aria-label", "Dismiss KV message");
  close.addEventListener("click", () => {
    kvNotice = null;
    renderKV();
  });
  row.append(close);
  return row;
}

function renderKVEmptyState(text: string): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "kv-empty";
  item.append(textElement("span", text));
  return item;
}

function renderKVStore(store: KVStoreInfo): HTMLLIElement {
  const nodeKey = kvNodeKey(store);
  const expanded = kvExpandedNodes.has(nodeKey);
  const item = document.createElement("li");
  item.className = "kv-store";
  const row = document.createElement("div");
  row.className = "kv-row kv-store-row";
  bindKVRowAction(row, `Toggle ${store.kind} KV store`, () => toggleKVNode(nodeKey));
  row.append(createKVDisclosure(expanded, `Toggle ${store.kind} KV store`, () => toggleKVNode(nodeKey)));
  row.append(createKVStoreTitle(store));
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  row.append(actions);
  item.append(row);
  if (expanded) {
    item.append(renderKVNodeBody(store, [], nodeKey));
  }
  return item;
}

function renderKVNodeBody(store: KVStoreInfo, path: string[], nodeKey: string): HTMLElement {
  const body = document.createElement("div");
  body.className = "kv-node-body";
  body.append(renderKVPathToolbar(store, path, nodeKey));
  const snapshot = kvEntriesByNode.get(nodeKey);
  if (!snapshot) {
    body.append(renderKVLoading());
    return body;
  }
  if (!snapshot.exists) {
    body.append(textElement("p", "Subtree does not exist.", "kv-subtree-missing"));
    return body;
  }
  const entries = snapshot.entries ?? [];
  if (!entries.length) {
    body.append(textElement("p", "Empty subtree.", "kv-subtree-empty"));
    return body;
  }
  const list = document.createElement("ol");
  list.className = "kv-entry-list";
  for (const entry of entries) {
    list.append(renderKVEntry(store, path, nodeKey, entry));
  }
  body.append(list);
  return body;
}

function renderKVLoading(): HTMLElement {
  const loading = document.createElement("p");
  loading.className = "kv-loading";
  loading.textContent = "Loading subtree.";
  return loading;
}

// Entry snapshots intentionally contain metadata only. Values are fetched with
// a separate value subscription after the user explicitly opens the value
// panel, so tree rendering cannot accidentally disclose value contents.
function renderKVEntry(store: KVStoreInfo, path: string[], parentKey: string, entry: KVEntryInfo): HTMLLIElement {
  const item = document.createElement("li");
  item.className = `kv-entry kv-entry-${entry.kind}`;
  const row = document.createElement("div");
  row.className = "kv-row";
  if (entry.kind === "bucket") {
    const childKey = childKVNodeKey(parentKey, entry.name) ?? kvNodeKey(store, [...path, entry.name]);
    const expanded = kvExpandedNodes.has(childKey);
    bindKVRowAction(row, `Toggle subtree ${entry.name}`, () => toggleKVNode(childKey));
    row.append(createKVDisclosure(expanded, `Toggle subtree ${entry.name}`, () => toggleKVNode(childKey)));
    row.append(createKVEntryTitle(entry));
    row.append(renderKVBucketActions(store, [...path, entry.name]));
    item.append(row);
    if (expanded) item.append(renderKVNodeBody(store, [...path, entry.name], childKey));
  } else {
    bindKVRowAction(row, `Open value ${entry.name}`, () => openKVValuePanel(store, path, entry.name));
    row.append(createKVIndent());
    row.append(createKVEntryTitle(entry));
    row.append(renderKVValueActions(store, path, entry));
    if (kvValuePanel && sameKVStore(kvValuePanel.store, store) && pathsEqual(kvValuePanel.path, path) && kvValuePanel.key === entry.name) {
      row.classList.add("kv-row-selected");
    }
    item.append(row);
  }
  return item;
}

function createKVDisclosure(expanded: boolean, label: string, onClick: () => void): HTMLButtonElement {
  const button = textElement("button", expanded ? "-" : "+", "kv-disclosure");
  button.type = "button";
  button.setAttribute("aria-label", label);
  button.setAttribute("aria-expanded", String(expanded));
  button.addEventListener("click", onClick);
  return button;
}

function bindKVRowAction(row: HTMLElement, label: string, onAction: () => void): void {
  row.classList.add("kv-row-clickable");
  row.tabIndex = 0;
  row.setAttribute("role", "button");
  row.setAttribute("aria-label", label);
  row.addEventListener("click", (event) => {
    if (isKVRowInteractiveEvent(row, event)) return;
    onAction();
  });
  row.addEventListener("keydown", (event) => {
    if (isKVRowInteractiveEvent(row, event)) return;
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    onAction();
  });
}

function isKVRowInteractiveEvent(row: HTMLElement, event: Event): boolean {
  if (event.defaultPrevented || !(event.target instanceof Element)) return true;
  const interactive = event.target.closest("button, a, input, select, textarea, label, [role='button']");
  return Boolean(interactive && interactive !== row);
}

function createKVIndent(): HTMLElement {
  const indent = document.createElement("span");
  indent.className = "kv-indent";
  indent.setAttribute("aria-hidden", "true");
  return indent;
}

function renderKVBucketActions(store: KVStoreInfo, path: string[]): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const drop = textElement("button", "Drop");
  drop.type = "button";
  drop.title = "Drop subtree";
  drop.className = "danger";
  drop.addEventListener("click", () => dropKVSubtree(store, path));
  actions.append(drop);
  return actions;
}

function renderKVValueActions(store: KVStoreInfo, path: string[], entry: KVEntryInfo): HTMLElement {
  const actions = document.createElement("div");
  actions.className = "kv-row-actions";
  const remove = textElement("button", "Delete");
  remove.type = "button";
  remove.title = "Delete value";
  remove.className = "danger";
  remove.addEventListener("click", () => deleteKVValue(store, path, entry.name));
  actions.append(remove);
  return actions;
}

function renderKVPathToolbar(store: KVStoreInfo, path: string[], nodeKey: string): HTMLElement {
  const toolbar = document.createElement("div");
  toolbar.className = "kv-toolbar";
  const pathLabel = textElement("span", kvPathLabel(store, path), "kv-path-label");
  pathLabel.title = kvPathLabel(store, path);
  const addValue = textElement("button", "+ Value");
  addValue.type = "button";
  addValue.title = "Create value in this subtree";
  addValue.addEventListener("click", () => openKVForm(nodeKey, "value"));
  const addSubtree = textElement("button", "+ Subtree");
  addSubtree.type = "button";
  addSubtree.title = "Create child subtree";
  addSubtree.addEventListener("click", () => openKVForm(nodeKey, "subtree"));
  toolbar.append(pathLabel, addValue, addSubtree);
  if (kvOpenForm?.nodeKey === nodeKey) {
    const form = kvOpenForm.mode === "value" ? renderKVValueForm(store, path) : renderKVSubtreeForm(store, path);
    const wrapper = document.createElement("div");
    wrapper.className = "kv-form-slot";
    wrapper.append(form);
    toolbar.append(wrapper);
  }
  return toolbar;
}

function kvPathLabel(store: KVStoreInfo, path: string[]): string {
  const root = store.kind === "plugin" ? `plugin:${store.name ?? ""}` : "default";
  return path.length ? `${root} / ${path.join(" / ")}` : root;
}

function renderKVSubtreeForm(store: KVStoreInfo, path: string[]): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form kv-subtree-form";
  const name = document.createElement("input");
  name.name = "name";
  name.placeholder = "Subtree name";
  name.autocomplete = "off";
  name.required = true;
  const save = textElement("button", "Create");
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", closeKVForm);
  form.append(name, save, cancel);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void createKVSubtree(store, path, name.value);
  });
  return form;
}

function renderKVValueForm(store: KVStoreInfo, path: string[]): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "kv-inline-form kv-value-form";
  const key = document.createElement("input");
  key.name = "key";
  key.placeholder = "Key";
  key.autocomplete = "off";
  key.required = true;
  const mode = document.createElement("select");
  mode.name = "mode";
  const textOption = document.createElement("option");
  textOption.value = "text";
  textOption.textContent = "Text";
  const bytesOption = document.createElement("option");
  bytesOption.value = "base64";
  bytesOption.textContent = "Bytes base64";
  mode.append(textOption, bytesOption);
  const hiddenLabel = document.createElement("label");
  hiddenLabel.className = "kv-checkbox";
  const hidden = document.createElement("input");
  hidden.type = "checkbox";
  hidden.name = "hidden";
  hiddenLabel.append(hidden, textElement("span", "Hidden"));
  const data = document.createElement("textarea");
  data.name = "data";
  data.placeholder = "Value";
  data.rows = 4;
  const save = textElement("button", "Set");
  save.type = "submit";
  const cancel = textElement("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", closeKVForm);
  form.append(key, mode, hiddenLabel, data, save, cancel);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void setKVValue(store, path, key.value, data.value, mode.value, hidden.checked);
  });
  return form;
}

function openKVForm(nodeKey: string, mode: "value" | "subtree"): void {
  kvOpenForm = { nodeKey, mode };
  renderKV();
}

function closeKVForm(): void {
  kvOpenForm = null;
  renderKV();
}

function toggleKVNode(nodeKey: string): void {
  if (kvExpandedNodes.has(nodeKey)) {
    kvExpandedNodes.delete(nodeKey);
  } else {
    kvExpandedNodes.add(nodeKey);
  }
  renderKV();
}

async function createKVSubtree(store: KVStoreInfo, path: string[], name: string): Promise<void> {
  const childName = name;
  if (!childName) {
    kvNotice = { level: "error", text: "Subtree name is required." };
    renderKV();
    return;
  }
  const childPath = [...path, childName];
  if (await runKVMutation("kvEnsure", { store, path: childPath }, "Subtree created.")) {
    closeKVForm();
  }
}

async function setKVValue(
  store: KVStoreInfo,
  path: string[],
  key: string,
  data: string,
  mode: string,
  hidden: boolean,
): Promise<void> {
  const name = key;
  if (!name) {
    kvNotice = { level: "error", text: "Key is required." };
    renderKV();
    return;
  }
  const textMode = mode !== "base64";
  const value: KVValue = {
    data,
    encoding: textMode ? "string" : "base64",
    string: textMode,
    hidden,
  };
  if (await runKVMutation("kvSet", { store, path, key: name, value }, "Value saved.")) {
    closeKVForm();
  }
}

async function deleteKVValue(store: KVStoreInfo, path: string[], key: string): Promise<void> {
  if (!window.confirm(`Delete value "${key}"?`)) return;
  if (kvValuePanel && sameKVStore(kvValuePanel.store, store) && pathsEqual(kvValuePanel.path, path) && kvValuePanel.key === key) {
    closeKVValuePanel();
  }
  await runKVMutation("kvDelete", { store, path, key }, "Value deleted.");
}

async function dropKVSubtree(store: KVStoreInfo, path: string[]): Promise<void> {
  if (!path.length) return;
  if (!window.confirm(`Drop subtree "${path[path.length - 1]}"?`)) return;
  const targetKey = kvNodeKey(store, path);
  if (kvValuePanel && sameKVStore(kvValuePanel.store, store) && pathHasPrefix(kvValuePanel.path, path)) {
    closeKVValuePanel();
  }
  if (await runKVMutation("kvDrop", { store, path }, "Subtree dropped.")) {
    for (const nodeKey of [...kvExpandedNodes]) {
      if (kvNodeDescendsFrom(nodeKey, targetKey)) kvExpandedNodes.delete(nodeKey);
    }
    renderKV();
  }
}

async function runKVMutation(
  method: KVMutationMethod,
  params: Record<string, unknown>,
  success: string,
): Promise<boolean> {
  try {
    await backend(method, params);
    kvNotice = { level: "info", text: success };
    renderKV();
    return true;
  } catch (error) {
    kvNotice = { level: "error", text: `${method}: ${errorMessage(error)}` };
    renderKV();
    return false;
  }
}

function syncKVEntrySubscriptions(): void {
  if (activeTab !== "kv") return;
  const desired = desiredKVEntrySubscriptionKeys(kvStores, kvExpandedNodes, kvEntriesByNode);
  for (const [nodeKey, id] of [...kvEntrySubscriptions]) {
    if (desired.has(nodeKey)) continue;
    kvEntrySubscriptions.delete(nodeKey);
    void tryBackend("kvUnsubscribe", { subscriptionID: id });
  }
  for (const nodeKey of desired) {
    if (!kvEntrySubscriptions.has(nodeKey) && !kvEntrySubscribePending.has(nodeKey)) {
      void subscribeKVEntries(nodeKey);
    }
  }
}

async function subscribeKVEntries(nodeKey: string): Promise<void> {
  const ref = parseKVNodeKey(nodeKey);
  if (!ref || kvEntrySubscribePending.has(nodeKey) || kvEntrySubscriptions.has(nodeKey)) return;
  kvEntrySubscribePending.add(nodeKey);
  const response = await tryBackend("kvSubscribeEntries", {
    store: ref.store,
    path: ref.path,
  });
  kvEntrySubscribePending.delete(nodeKey);
  if (!response) {
    if (!kvEntrySubscribePending.size) kvPendingEntrySnapshots.clear();
    kvNotice = { level: "error", text: `Failed to subscribe ${kvPathLabel(ref.store, ref.path)}.` };
    renderKV();
    return;
  }
  const desired = activeTab === "kv" && desiredKVEntrySubscriptionKeys(kvStores, kvExpandedNodes, kvEntriesByNode).has(nodeKey);
  if (!desired) {
    kvPendingEntrySnapshots.delete(response.subscription_id);
    if (response.subscription_id) void tryBackend("kvUnsubscribe", { subscriptionID: response.subscription_id });
    if (!kvEntrySubscribePending.size) kvPendingEntrySnapshots.clear();
    return;
  }
  kvEntrySubscriptions.set(nodeKey, response.subscription_id);
  kvEntriesByNode.set(
    nodeKey,
    takePendingKVSnapshot(kvPendingEntrySnapshots, response.subscription_id, response.snapshot),
  );
  if (!kvEntrySubscribePending.size) kvPendingEntrySnapshots.clear();
  renderKV();
}

function kvNodeKeyForEntrySubscription(subscriptionID: number): string | null {
  for (const [nodeKey, id] of kvEntrySubscriptions) {
    if (id === subscriptionID) return nodeKey;
  }
  return null;
}

function openKVValuePanel(store: KVStoreInfo, path: string[], key: string): void {
  closeKVValuePanel();
  kvValuePanel = {
    store: normalizeKVStore(store),
    path: [...path],
    key,
    subscriptionID: 0,
    snapshot: null,
    revealHidden: false,
  };
  renderKVValuePanel();
  void subscribeKVValuePanel();
}

async function subscribeKVValuePanel(): Promise<void> {
  if (!kvValuePanel || activeTab !== "kv" || kvValueSubscribePending || kvValuePanel.subscriptionID) return;
  kvValueSubscribePending = true;
  const target = {
    store: kvValuePanel.store,
    path: [...kvValuePanel.path],
    key: kvValuePanel.key,
  };
  const response = await tryBackend("kvSubscribeValue", target);
  kvValueSubscribePending = false;
  if (!response) {
    kvPendingValueSnapshots.clear();
    kvNotice = { level: "error", text: "Failed to subscribe value." };
    renderKVValuePanel();
    return;
  }
  if (
    activeTab !== "kv" ||
    !kvValuePanel ||
    !sameKVStore(kvValuePanel.store, target.store) ||
    !pathsEqual(kvValuePanel.path, target.path) ||
    kvValuePanel.key !== target.key
  ) {
    kvPendingValueSnapshots.delete(response.subscription_id);
    if (response.subscription_id) void tryBackend("kvUnsubscribe", { subscriptionID: response.subscription_id });
    return;
  }
  kvValuePanel.subscriptionID = response.subscription_id;
  kvValuePanel.snapshot = takePendingKVSnapshot(kvPendingValueSnapshots, response.subscription_id, response.snapshot);
  kvPendingValueSnapshots.clear();
  renderKVValuePanel();
}

function renderKVValuePanel(): void {
  el.kvValuePanel.replaceChildren();
  el.kvPanel.classList.toggle("kv-value-open", Boolean(kvValuePanel));
  if (!kvValuePanel) {
    el.kvValuePanel.classList.add("hidden");
    return;
  }
  el.kvValuePanel.classList.remove("hidden");
  const header = document.createElement("header");
  header.className = "kv-value-header";
  const title = document.createElement("div");
  title.append(textElement("h2", kvValuePanel.key), textElement("p", kvPathLabel(kvValuePanel.store, kvValuePanel.path)));
  const close = textElement("button", "x");
  close.type = "button";
  close.title = "Close";
  close.setAttribute("aria-label", "Close value panel");
  close.addEventListener("click", closeKVValuePanel);
  header.append(title, close);
  el.kvValuePanel.append(header);

  const snapshot = kvValuePanel.snapshot;
  if (!snapshot) {
    el.kvValuePanel.append(renderKVLoading());
    return;
  }
  if (!snapshot.found || !snapshot.value) {
    el.kvValuePanel.append(textElement("p", "Value not found.", "kv-value-missing"));
    return;
  }
  el.kvValuePanel.append(renderKVValueDetail(snapshot.value));
}

function renderKVValueDetail(value: KVValue): HTMLElement {
  const detail = document.createElement("div");
  detail.className = "kv-value-detail";
  const meta = document.createElement("div");
  meta.className = "kv-value-meta";
  const actions = document.createElement("div");
  actions.className = "kv-value-actions";
  const copy = textElement("button", "Copy");
  copy.type = "button";
  copy.title = "Copy value";
  copy.addEventListener("click", () => copyKVValue(value));
  const download = textElement("button", "Download");
  download.type = "button";
  download.title = "Download value";
  download.addEventListener("click", () => downloadKVValue(value));
  actions.append(copy, download);
  if (value.hidden && !kvValuePanel?.revealHidden && value.string && isRenderableKVText(value)) {
    const reveal = textElement("button", "Reveal hidden");
    reveal.type = "button";
    reveal.title = "Reveal hidden value";
    reveal.addEventListener("click", () => {
      if (!kvValuePanel) return;
      kvValuePanel.revealHidden = true;
      renderKVValuePanel();
    });
    actions.append(reveal);
  }
  meta.append(textElement("span", formatKVValueMeta(value)), actions);
  detail.append(meta, createKVValuePreview(value, {
    revealHidden: kvValuePanel?.revealHidden ?? false,
    limit: KV_TEXT_RENDER_LIMIT,
  }));
  return detail;
}

function closeKVValuePanel(): void {
  if (kvValuePanel?.subscriptionID) {
    void tryBackend("kvUnsubscribe", { subscriptionID: kvValuePanel.subscriptionID });
  }
  kvValuePanel = null;
  kvValueSubscribePending = false;
  renderKVValuePanel();
}

function isRenderableKVText(value: KVValue): boolean {
  const data = value.data ?? "";
  return Boolean(value.string) && (value.size ?? data.length) <= KV_TEXT_RENDER_LIMIT && data.length <= KV_TEXT_RENDER_LIMIT;
}

async function copyKVValue(value: KVValue): Promise<void> {
  try {
    if (value.string) {
      await navigator.clipboard.writeText(value.data ?? "");
    } else {
      await copyBinaryKVValue(value);
    }
    kvNotice = { level: "info", text: "Value copied." };
  } catch (error) {
    kvNotice = { level: "error", text: `Copy failed: ${errorMessage(error)}` };
  }
  renderKV();
}

// Browser binary clipboard support is uneven. Prefer real bytes when the
// ClipboardItem API accepts them; otherwise fall back to copying the base64
// representation returned by the KV API.
async function copyBinaryKVValue(value: KVValue): Promise<void> {
  const blob = new Blob([bytesFromBase64(value.data ?? "")], { type: "application/octet-stream" });
  if (navigator.clipboard.write && typeof ClipboardItem !== "undefined") {
    await navigator.clipboard.write([new ClipboardItem({ "application/octet-stream": blob })]);
    return;
  }
  await navigator.clipboard.writeText(value.data ?? "");
}

function downloadKVValue(value: KVValue): void {
  try {
    const blob = value.string
      ? new Blob([value.data ?? ""], { type: "text/plain;charset=utf-8" })
      : new Blob([bytesFromBase64(value.data ?? "")], { type: "application/octet-stream" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = kvDownloadName(value);
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    kvNotice = { level: "info", text: "Download started." };
  } catch (error) {
    kvNotice = { level: "error", text: `Download failed: ${errorMessage(error)}` };
  }
  renderKV();
}

function kvDownloadName(value: KVValue): string {
  const key = kvValuePanel?.key ?? "value";
  const base = safeFilenamePart(key) || "value";
  return `${base}.${value.string ? "txt" : "bin"}`;
}

function safeFilenamePart(value: string): string {
  return value.replace(/[\x00-\x1f\x7f/\\?%*:|"<>]+/g, "_").slice(0, 80);
}

function bytesFromBase64(data: string): Uint8Array<ArrayBuffer> {
  const binary = window.atob(data);
  const bytes = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function rememberKVScrollState(): void {
  kvScrollTop = el.kvPanel.scrollTop;
}

function restoreKVScrollState(): void {
  window.setTimeout(() => {
    el.kvPanel.scrollTop = kvScrollTop;
  }, 0);
}

function pathsEqual(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((segment, index) => segment === right[index]);
}

function pathHasPrefix(path: string[], prefix: string[]): boolean {
  return path.length >= prefix.length && prefix.every((segment, index) => path[index] === segment);
}

function renderLogs(): void {
  el.logs.replaceChildren();
  if (!frontendLogs.length) {
    el.logs.append(createEmptyLogItem());
    return;
  }
  const rows = [...frontendLogs].reverse();
  for (const row of rows) {
    el.logs.append(createLogItem(row));
  }
}

type PsScrollAnchor = {
  nodeID: string;
  offsetTop: number;
  previousHeight: number;
  previousTop: number;
  pinToTop: boolean;
};

function renderWorkers(): void {
  const anchor = capturePsScrollAnchor();
  el.ps.replaceChildren();
  const blocks = workerNodeBlocks();
  if (!blocks.length) {
    el.ps.append(createEmptyWorkerItem());
  } else {
    const now = new Date();
    for (const block of blocks) {
      el.ps.append(createWorkerNodeBlock(block, now));
    }
  }
  restorePsScrollAnchor(anchor);
}

function updateWorkerAges(): void {
  updateWorkerAgeElements(el.ps, new Date());
}

function workerNodeBlocks(): WorkerNodeBlock[] {
  const byNode = new Map<number, WorkerSnapshot[]>();
  for (const worker of Object.values(snapshot.workers ?? {})) {
    const workers = byNode.get(worker.node) ?? [];
    workers.push(worker);
    byNode.set(worker.node, workers);
  }

  return [...byNode.entries()]
    .sort(([left], [right]) => left - right)
    .map(([nodeID, workers]) => {
      workers.sort((left, right) => left.id - right.id);
      const node = snapshot.nodes[String(nodeID)];
      const fallback = workers[0]!;
      const primaryType = node?.primary_type || nodeClassByName(snapshot, fallback.class)?.primary_type || "";
      const color = primaryType ? typeColor(primaryType) : null;
      return {
        nodeID,
        ...(node === undefined ? {} : { node }),
        fallbackClass: fallback.class,
        fallbackName: fallback.name,
        workers,
        ...(color ? { color: color.color, textColor: color.textColor } : {}),
      };
    });
}

// Preserve the first visible node block across replacement renders. This keeps
// the Ps list from jumping when worker notifications insert or remove blocks
// above the current viewport.
function capturePsScrollAnchor(): PsScrollAnchor {
  const panel = el.psPanel;
  const panelTop = panel.getBoundingClientRect().top;
  for (const block of el.ps.querySelectorAll<HTMLElement>(".ps-node-block")) {
    const rect = block.getBoundingClientRect();
    if (rect.bottom >= panelTop) {
      return {
        nodeID: block.dataset.nodeId ?? "",
        offsetTop: rect.top - panelTop,
        previousHeight: panel.scrollHeight,
        previousTop: panel.scrollTop,
        pinToTop: panel.scrollTop <= 2,
      };
    }
  }
  return {
    nodeID: "",
    offsetTop: 0,
    previousHeight: panel.scrollHeight,
    previousTop: panel.scrollTop,
    pinToTop: panel.scrollTop <= 2,
  };
}

function restorePsScrollAnchor(anchor: PsScrollAnchor): void {
  const panel = el.psPanel;
  if (anchor.pinToTop) {
    panel.scrollTop = 0;
    return;
  }
  const block = anchor.nodeID ? el.ps.querySelector<HTMLElement>(`.ps-node-block[data-node-id="${anchor.nodeID}"]`) : null;
  if (block) {
    const nextOffset = block.getBoundingClientRect().top - panel.getBoundingClientRect().top;
    panel.scrollTop += nextOffset - anchor.offsetTop;
    return;
  }
  panel.scrollTop = anchor.previousTop + (panel.scrollHeight - anchor.previousHeight);
}

function updateWorkerAgeTimer(): void {
  const shouldRun = activeTab === "ps" && Object.keys(snapshot.workers ?? {}).length > 0;
  if (shouldRun && !workerAgeTimer) {
    workerAgeTimer = window.setInterval(updateWorkerAges, 1000);
  } else if (!shouldRun && workerAgeTimer) {
    window.clearInterval(workerAgeTimer);
    workerAgeTimer = 0;
  }
}

function appendLog(entry: LogEntry): void {
  if (isConfigSaveFailure(entry)) {
    notifyError(entry.message);
  }
  const panel = el.logsPanel;
  const previousHeight = panel.scrollHeight;
  const previousTop = panel.scrollTop;
  const pinToTop = previousTop <= 2;
  const hadEmptyState = !frontendLogs.length;
  frontendLogs.push(entry);
  trimLogStore();
  if (hadEmptyState) {
    renderLogs();
  } else {
    el.logs.prepend(createLogItem(entry));
    const overflow = el.logs.children.length - frontendLogs.length;
    for (let i = 0; i < overflow; i++) {
      el.logs.lastElementChild?.remove();
    }
  }
  if (!pinToTop) {
    panel.scrollTop = previousTop + (panel.scrollHeight - previousHeight);
  }
}

function isConfigSaveFailure(entry: LogEntry): boolean {
  return entry.source === "daemon" && entry.level === "error" && entry.message.startsWith("config save failed:");
}

async function activateConfigPanel(): Promise<void> {
  if (configSnapshot || configLoading) {
    updateConfigControls();
    return;
  }
  await loadConfig();
}

async function loadConfig(): Promise<void> {
  if (configLoading || configApplying) return;
  configLoading = true;
  el.configStatus.textContent = "Loading editable config.";
  updateConfigControls();
  try {
    const next = await backend("configSnapshot");
    configSnapshot = next;
    configDraft = next.text;
    el.configEditor.value = next.text;
    el.configStatus.textContent = next.read_only
      ? "This daemon started with a read-only config."
      : `Loaded revision ${shortConfigRevision(next.revision)}.`;
  } catch (error) {
    el.configStatus.textContent = `Config load failed: ${errorMessage(error)}`;
  } finally {
    configLoading = false;
    updateConfigControls();
  }
}

async function applyConfig(): Promise<void> {
  if (!configSnapshot || configSnapshot.read_only || configApplying || configDraft === configSnapshot.text) return;
  configApplying = true;
  el.configStatus.textContent = "Applying config.";
  updateConfigControls();
  try {
    const response = await backend("configUpdate", {
      text: configDraft,
      revision: configSnapshot.revision,
    });
    configSnapshot = response.snapshot;
    configDraft = response.snapshot.text;
    el.configEditor.value = response.snapshot.text;
    el.configStatus.textContent = configChangeSummary(response.changes, response.snapshot.revision);
  } catch (error) {
    el.configStatus.textContent = `Config apply failed: ${errorMessage(error)}`;
  } finally {
    configApplying = false;
    updateConfigControls();
  }
}

function updateConfigControls(): void {
  const readOnly = configSnapshot?.read_only ?? false;
  const dirty = configSnapshot !== null && configDraft !== configSnapshot.text;
  el.configEditor.disabled = configLoading || configApplying || readOnly;
  el.configReload.disabled = configLoading || configApplying;
  el.configApply.disabled = configLoading || configApplying || readOnly || !dirty;
}

function shortConfigRevision(revision: string): string {
  return revision.slice(0, 12);
}

function configChangeSummary(changes: ConfigChanges, revision: string): string {
  const parts: string[] = [];
  const addCount = (label: string, values: string[] | undefined): void => {
    if (values?.length) parts.push(`${values.length} ${label}`);
  };
  addCount("node(s) added", changes.added);
  addCount("node(s) removed", changes.removed);
  addCount("node(s) reinitialized", changes.reinitialized);
  addCount("position(s) updated", changes.positions_updated);
  addCount("primary type(s) updated", changes.primary_types_updated);
  addCount("link(s) added", changes.links_added);
  addCount("link(s) removed", changes.links_removed);
  const detail = parts.length ? parts.join(", ") : "no live graph changes";
  return `Applied revision ${shortConfigRevision(revision)}: ${detail}.`;
}

async function activateSnapshotsPanel(): Promise<void> {
  if (snapshotEntries.length || snapshotsLoading) {
    updateSnapshotControls();
    return;
  }
  await loadSnapshots();
}

async function loadSnapshots(): Promise<void> {
  if (snapshotsLoading || snapshotMutationPending) return;
  snapshotsLoading = true;
  el.snapshotsStatus.textContent = "Loading snapshots.";
  updateSnapshotControls();
  try {
    const response = await backend("snapshotsList");
    snapshotEntries = response.snapshots ?? [];
    renderSnapshots();
    el.snapshotsStatus.textContent = `${snapshotEntries.length} snapshot(s).`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot load failed: ${errorMessage(error)}`;
  } finally {
    snapshotsLoading = false;
    updateSnapshotControls();
  }
}

async function createSnapshot(): Promise<void> {
  if (snapshotsLoading || snapshotMutationPending) return;
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = "Creating snapshot.";
  updateSnapshotControls();
  try {
    const created = await backend("snapshotCreate", { name: el.snapshotName.value });
    el.snapshotName.value = "";
    snapshotEntries = upsertSnapshot(snapshotEntries, created);
    renderSnapshots();
    el.snapshotsStatus.textContent = `Created snapshot ${created.uuid}.`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot creation failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

// exportSnapshot downloads the exact stored ZIP. Snapshot UUIDs are stable
// archive identities, so the UI always uses the UUID as the file name.
async function exportSnapshot(entry: SnapshotInfo): Promise<void> {
  if (snapshotsLoading || snapshotMutationPending) return;
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = `Exporting snapshot ${entry.uuid}.`;
  updateSnapshotControls();
  try {
    const response = await backend("snapshotExport", { uuid: entry.uuid });
    const blob = new Blob([bytesFromBase64(response.archive)], { type: "application/zip" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${entry.uuid}.zip`;
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    el.snapshotsStatus.textContent = `Exported snapshot ${response.snapshot.uuid}.`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot export failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

// importSnapshot reads the selected ZIP as base64 because the shared daemon
// API uses JSON on both WebSocket and Unix-socket transports.
async function importSnapshot(): Promise<void> {
  if (snapshotsLoading || snapshotMutationPending) return;
  const file = el.snapshotImportFile.files?.[0];
  if (!file) {
    el.snapshotsStatus.textContent = "Select a snapshot archive to import.";
    return;
  }
  if (file.size > SNAPSHOT_ARCHIVE_MAX_BYTES) {
    el.snapshotsStatus.textContent = "Snapshot archive exceeds the 32 MiB limit.";
    return;
  }
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = `Importing snapshot archive ${file.name}.`;
  updateSnapshotControls();
  try {
    const archive = await fileAsBase64(file);
    const params: Record<string, unknown> = { archive };
    if (el.snapshotImportName.value !== "") params.name = el.snapshotImportName.value;
    const imported = await backend("snapshotImport", params);
    el.snapshotImportFile.value = "";
    el.snapshotImportName.value = "";
    snapshotEntries = upsertSnapshot(snapshotEntries, imported);
    renderSnapshots();
    el.snapshotsStatus.textContent = `Imported snapshot ${imported.uuid}.`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot import failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

function fileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("error", () => reject(reader.error ?? new Error("Snapshot archive read failed.")));
    reader.addEventListener("load", () => {
      if (typeof reader.result !== "string") {
        reject(new Error("Snapshot archive read returned an invalid result."));
        return;
      }
      const separator = reader.result.indexOf(",");
      if (separator < 0) {
        reject(new Error("Snapshot archive encoding failed."));
        return;
      }
      resolve(reader.result.slice(separator + 1));
    });
    reader.readAsDataURL(file);
  });
}

async function restoreSnapshot(entry: SnapshotInfo): Promise<void> {
  const label = entry.name ? `“${entry.name}” (${entry.uuid})` : entry.uuid;
  if (!window.confirm(`Restore snapshot ${label}? Current daemon state will be replaced.`)) return;
  if (snapshotsLoading || snapshotMutationPending) return;
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = `Restoring snapshot ${entry.uuid}.`;
  updateSnapshotControls();
  try {
    const response = await backend("snapshotRestore", { uuid: entry.uuid });
    configSnapshot = null;
    configDraft = "";
    const persistence = response.config_written
      ? " Config was written to disk."
      : response.config_write_error
        ? ` Config is active in memory; the config file write failed: ${response.config_write_error}`
        : " Config is active in memory only.";
    el.snapshotsStatus.textContent = `Restored snapshot ${response.snapshot.uuid}.${persistence}`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot restore failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

async function renameSnapshot(entry: SnapshotInfo): Promise<void> {
  const name = window.prompt("Snapshot name (leave empty for no name):", entry.name ?? "");
  if (name === null || snapshotsLoading || snapshotMutationPending) return;
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = `Renaming snapshot ${entry.uuid}.`;
  updateSnapshotControls();
  try {
    const renamed = await backend("snapshotRename", { uuid: entry.uuid, name });
    snapshotEntries = upsertSnapshot(snapshotEntries, renamed);
    renderSnapshots();
    el.snapshotsStatus.textContent = `Renamed snapshot ${entry.uuid}.`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot rename failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

async function deleteSnapshot(entry: SnapshotInfo): Promise<void> {
  const label = entry.name ? `“${entry.name}” (${entry.uuid})` : entry.uuid;
  if (!window.confirm(`Delete snapshot ${label}? This action cannot be undone.`)) return;
  if (snapshotsLoading || snapshotMutationPending) return;
  snapshotMutationPending = true;
  el.snapshotsStatus.textContent = `Deleting snapshot ${entry.uuid}.`;
  updateSnapshotControls();
  try {
    await backend("snapshotDelete", { uuid: entry.uuid });
    snapshotEntries = removeSnapshotEntry(snapshotEntries, entry.uuid);
    renderSnapshots();
    el.snapshotsStatus.textContent = `Deleted snapshot ${entry.uuid}.`;
  } catch (error) {
    el.snapshotsStatus.textContent = `Snapshot deletion failed: ${errorMessage(error)}`;
  } finally {
    snapshotMutationPending = false;
    updateSnapshotControls();
  }
}

function renderSnapshots(): void {
  renderSnapshotList(el.snapshotsList, snapshotEntries, snapshotMutationPending, {
    export: (entry) => void exportSnapshot(entry),
    rename: (entry) => void renameSnapshot(entry),
    restore: (entry) => void restoreSnapshot(entry),
    delete: (entry) => void deleteSnapshot(entry),
  });
}

function updateSnapshotControls(): void {
  const disabled = snapshotsLoading || snapshotMutationPending;
  el.snapshotName.disabled = disabled;
  el.snapshotCreate.disabled = disabled;
  el.snapshotImportFile.disabled = disabled;
  el.snapshotImportName.disabled = disabled;
  el.snapshotImport.disabled = disabled;
  el.snapshotsReload.disabled = disabled;
  for (const button of el.snapshotsList.querySelectorAll<HTMLButtonElement>("button")) {
    button.disabled = disabled;
  }
}

function notifyError(message: string): void {
  const toast = createNotificationToast(message);
  const id = nextToastID++;
  toast.dataset.toastId = String(id);
  const close = () => {
    toast.remove();
  };
  toast.querySelector("button")?.addEventListener("click", close);
  el.notifications?.append(toast);
  window.setTimeout(() => {
    if (toast.dataset.toastId === String(id)) close();
  }, 12000);
}

function trimLogStore(): void {
  if (logStoreLimit <= 0 || frontendLogs.length <= logStoreLimit) return;
  frontendLogs.splice(0, frontendLogs.length - logStoreLimit);
}

function setTab(name: string): void {
  const previousTab = activeTab;
  if (previousTab === "sec" && name !== "sec") deactivateSecPanel();
  if (previousTab === "dns" && name !== "dns") deactivateDNSPanel();
  if (previousTab === "tls" && name !== "tls") deactivateTLSPanel();
  if (previousTab === "kv" && name !== "kv") deactivateKVPanel();
  activeTab = name;
  if (name !== "graph") graphShortcutsArmed = false;
  for (const tab of el.tabs) tab.classList.toggle("active", tab.dataset.tab === name);
  for (const panel of el.panels) panel.classList.toggle("hidden", panel.dataset.panel !== name);
  if (name === "sec") activateSecPanel();
  if (name === "dns") activateDNSPanel();
  if (name === "tls") activateTLSPanel();
  if (name === "kv") activateKVPanel();
  if (name === "logs") renderLogs();
  if (name === "ps") renderWorkers();
  if (name === "config") void activateConfigPanel();
  if (name === "snapshots") void activateSnapshotsPanel();
  if (name === "graph") setTimeout(resizeGraphCanvas, 0);
  updateWorkerAgeTimer();
}

function graphKeydown(event: KeyboardEvent): void {
  if (!shouldHandleGraphKeydown(event)) return;
  debugLog("graph keydown", {
    key: event.key,
    ctrl: event.ctrlKey,
    meta: event.metaKey,
    shift: event.shiftKey,
    target: event.target instanceof HTMLElement ? event.target.tagName : String(event.target),
    selected: currentSelectedNodeIDs(),
  });
  if (isGraphShortcut(event, "KeyZ", "z")) {
    consumeGraphShortcut(event);
    runHistory(event.shiftKey ? "redo" : "undo");
  } else if (isGraphShortcut(event, "KeyY", "y")) {
    consumeGraphShortcut(event);
    runHistory("redo");
  } else if (isGraphShortcut(event, "KeyC", "c")) {
    consumeGraphShortcut(event);
    copySelection();
  } else if (isGraphShortcut(event, "KeyV", "v")) {
    if (event.repeat) {
      consumeGraphShortcut(event);
      return;
    }
    pasteShortcutActive = true;
    pasteShortcutConsumed = false;
    scheduleLocalPasteFallback();
  } else if (event.key === "Backspace" || event.key === "Delete") {
    const ids = currentSelectedNodeIDs();
    if (!ids.length) return;
    consumeGraphShortcut(event);
    tryBackend("removeNodes", { ids });
  }
}

function graphKeyup(event: KeyboardEvent): void {
  if (event.code === "KeyV" || event.key.toLowerCase() === "v") {
    pasteShortcutActive = false;
    pasteShortcutConsumed = false;
  }
}

function isGraphShortcut(event: KeyboardEvent, code: string, key: string): boolean {
  return isKeyboardShortcut(event, code, key);
}

function consumeGraphShortcut(event: Event): void {
  event.preventDefault();
  event.stopImmediatePropagation();
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select" || target.isContentEditable;
}

function shouldHandleGraphKeydown(event: KeyboardEvent): boolean {
  if (activeTab !== "graph" || isEditableTarget(event.target) || hasUserTextSelection()) return false;
  return eventTargetsGraph(event.target) || document.activeElement === el.graphCanvas || graphShortcutsArmed;
}

function handleGraphPaste(event: ClipboardEvent): void {
  if (!shouldHandleGraphClipboardEvent(event)) return;
  nativePasteEvents += 1;
  if (pasteShortcutActive && pasteShortcutConsumed) {
    consumeGraphShortcut(event);
    return;
  }
  const data = event.clipboardData?.getData("text/plain") ?? "";
  if (!isPastaClipboard(data)) return;
  consumeGraphShortcut(event);
  if (pasteShortcutActive) pasteShortcutConsumed = true;
  localClipboard = data;
  void pasteSelection(data);
}

function shouldHandleGraphClipboardEvent(event: ClipboardEvent): boolean {
  if (activeTab !== "graph" || isEditableTarget(event.target) || hasUserTextSelection()) return false;
  return eventTargetsGraph(event.target) || document.activeElement === el.graphCanvas || graphShortcutsArmed;
}

function eventTargetsGraph(target: EventTarget | null): boolean {
  return target === el.graphCanvas;
}

function hasUserTextSelection(): boolean {
  const selection = window.getSelection();
  return Boolean(selection && !selection.isCollapsed && selection.toString().length > 0);
}

function armGraphShortcuts(event: PointerEvent | MouseEvent): void {
  graphShortcutsArmed = true;
  rememberGraphPointer(event);
}

function rememberGraphPointer(event: PointerEvent | MouseEvent): void {
  lastGraphPointer = { clientX: event.clientX, clientY: event.clientY };
}

async function runHistory(method: "undo" | "redo"): Promise<void> {
  await tryBackend(method);
  await requestSnapshot();
}

async function copySelection(): Promise<void> {
  const ids = currentSelectedNodeIDs();
  debugLog("copy selection requested", { ids });
  if (!ids.length) {
    debugLog("copy selection skipped: no selected backend nodes");
    return;
  }
  const data = await tryBackend("copy", { ids });
  debugLog("backend copy returned", {
    hasData: Boolean(data),
    length: data?.length ?? 0,
    validPastaClipboard: data ? isPastaClipboard(data) : false,
    preview: data ? data.slice(0, 160) : "",
  });
  if (!data) return;
  localClipboard = data;
  void navigator.clipboard?.writeText(data).catch((error) => {
    debugLog("system copy failed", { error: errorMessage(error) });
    log("clipboard", "warn", `system copy failed, using local clipboard: ${errorMessage(error)}`);
  });
}

function scheduleLocalPasteFallback(): void {
  const eventCount = nativePasteEvents;
  window.setTimeout(() => {
    if (nativePasteEvents !== eventCount || !isPastaClipboard(localClipboard)) return;
    if (pasteShortcutActive && pasteShortcutConsumed) return;
    if (pasteShortcutActive) pasteShortcutConsumed = true;
    void pasteSelection(localClipboard);
  }, 0);
}

async function pasteSelection(data: string): Promise<void> {
  debugLog("paste selection requested", {
    length: data.length,
    valid: isPastaClipboard(data),
    preview: data.slice(0, 160),
  });
  if (!isPastaClipboard(data)) {
    debugLog("paste skipped: no valid Pasta clipboard data");
    return;
  }
  const target = nextPasteTarget(data);
  const positionedData = positionClipboardAt(data, target);
  debugLog("calling backend paste", {
    dataLength: positionedData.length,
    target,
    preview: positionedData.slice(0, 160),
  });
  const pasted = await tryBackend("paste", { data: positionedData }) ?? [];
  debugLog("backend paste returned", {
    pasted,
    snapshotNodesBeforeRefresh: Object.keys(snapshot.nodes).length,
    snapshotLinksBeforeRefresh: Object.keys(snapshot.links).length,
  });
  await requestSnapshot();
  if (pasted.length) {
    selectGraphNodes(pasted);
    updateSidekick();
  }
}

function nextPasteTarget(data: string): [number, number] {
  const base = currentPasteBasePosition();
  const key = `${data.length}:${data.slice(0, 256)}:${Math.round(base[0])}:${Math.round(base[1])}`;
  if (lastPastePlacement?.key === key) {
    lastPastePlacement.count += 1;
  } else {
    lastPastePlacement = { key, count: 0 };
  }
  const nudge = lastPastePlacement.count * 32;
  return [base[0] + nudge, base[1] + nudge];
}

function currentPasteBasePosition(): [number, number] {
  if (graphPointerInside && lastGraphPointer) {
    return canvasPositionFromClientPoint(lastGraphPointer.clientX, lastGraphPointer.clientY);
  }
  return visibleCanvasCenterPosition();
}

function canvasPositionFromClientPoint(clientX: number, clientY: number): [number, number] {
  if (!canvas?.convertCanvasToOffset) return visibleCanvasCenterPosition();
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(clientX - rect.left, 0, rect.width);
  const y = clamp(clientY - rect.top, 0, rect.height);
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function visibleCanvasCenterPosition(): [number, number] {
  if (!canvas?.convertCanvasToOffset) return [160, 120];
  const rect = el.graphCanvas.getBoundingClientRect();
  return canvas.convertCanvasToOffset([rect.width * 0.5, rect.height * 0.5]) as [number, number];
}

function wireUI(): void {
  for (const tab of el.tabs) tab.addEventListener("click", () => setTab(tab.dataset.tab ?? "graph"));
  el.secPanel.addEventListener("scroll", rememberSecScrollState);
  el.dnsPanel.addEventListener("scroll", rememberDNSScrollState);
  el.tlsPanel.addEventListener("scroll", rememberTLSScrollState);
  el.kvPanel.addEventListener("scroll", rememberKVScrollState);
  el.configEditor.addEventListener("input", () => {
    configDraft = el.configEditor.value;
    updateConfigControls();
    el.configStatus.textContent = configDraft === configSnapshot?.text
      ? `Loaded revision ${shortConfigRevision(configSnapshot.revision)}.`
      : "Config has unapplied changes.";
  });
  el.configReload.addEventListener("click", () => void loadConfig());
  el.configApply.addEventListener("click", () => void applyConfig());
  el.snapshotCreate.addEventListener("click", () => void createSnapshot());
  el.snapshotsReload.addEventListener("click", () => void loadSnapshots());
  el.snapshotImport.addEventListener("click", () => void importSnapshot());
  document.addEventListener("paste", handleGraphPaste, { capture: true });
  document.addEventListener("keydown", (event) => {
    graphKeydown(event);
  }, { capture: true });
  document.addEventListener("keyup", graphKeyup, { capture: true });
}

function installTestHooks(): void {
  window.__almagestPanel = {
    call: tryBackend,
    snapshot: () => snapshot,
    selectNode: (id: number) => {
      const node = graph?.getNodeById(id);
      if (node) {
        canvas.selectNode(node);
        updateSidekick();
      }
    },
    selectNodes: (ids: number[]) => {
      selectGraphNodes(ids);
      updateSidekick();
    },
    workers: () => snapshot.workers,
    renameNode: (id: number, name: string) => {
      void tryBackend("setNodeName", { id, name });
    },
    moveNode: (id: number, x: number, y: number) => {
      const node = graph?.getNodeById(id);
      if (!node) return;
      node.pos = [x, y];
      graph.setDirtyCanvas(true, true);
      flushNodePositions();
    },
    canConnect: (from: number, to: number) => {
      const fromPort = snapshot.ports[String(from)];
      const toPort = snapshot.ports[String(to)];
      if (!fromPort || !toPort) return false;
      return LiteGraph.isValidConnection(liteGraphPortType(fromPort.types), liteGraphPortType(toPort.types));
    },
    nodeScreenPosition: (id: number) => {
      const node = graph?.getNodeById(id);
      if (!node || !canvas) return null;
      const rect = el.graphCanvas.getBoundingClientRect();
      return {
        x: rect.left + (node.pos[0] + node.size[0] * 0.5 + canvas.ds.offset[0]) * canvas.ds.scale,
        y: rect.top + (node.pos[1] + node.size[1] * 0.5 + canvas.ds.offset[1]) * canvas.ds.scale,
      };
    },
    portScreenPosition: (port: number) => {
      const portSnapshot = snapshot.ports[String(port)];
      if (!portSnapshot || !graph || !canvas) return null;
      const node = graph.getNodeById(portSnapshot.node);
      if (!node) return null;
      const nodeSnapshot = snapshot.nodes[String(portSnapshot.node)];
      const ids = portSnapshot.direction === "left" ? nodeSnapshot?.left_ports : nodeSnapshot?.right_ports;
      const index = ids?.indexOf(port) ?? -1;
      if (index < 0) return null;
      const pos = node.getConnectionPos(portSnapshot.direction === "left", index);
      const rect = el.graphCanvas.getBoundingClientRect();
      return {
        x: rect.left + (pos[0] + canvas.ds.offset[0]) * canvas.ds.scale,
        y: rect.top + (pos[1] + canvas.ds.offset[1]) * canvas.ds.scale,
      };
    },
    highlightedInputPort: () => canvas?._highlight_input_slot?.pastaPortId ?? null,
  };
}

async function boot(): Promise<void> {
  wireUI();
  initGraph();
  installTestHooks();
  el.status.textContent = "Connecting admin API";
  await connectAPI();
  const hello = await backend("hello");
  applyProjectVersionStatus(hello.project_version_status);
  const colormap = await backend("colormap");
  applyTypePalette(colormap);
  await backend("subscribeLogs");
  await backend("subscribeNotifications");
  await ensureSecSubscription();
  await requestSnapshot();
  el.status.textContent = "Admin API connected";
  setTab("graph");
}

boot().catch((error) => {
  el.status.textContent = `Panel failed: ${errorMessage(error)}`;
  log("frontend", "error", errorMessage(error));
});
