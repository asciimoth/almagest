package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/protected"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

//go:embed static
var webStatic embed.FS

type AdminAPI struct {
	webListener  net.Listener
	webServer    *http.Server
	webPProf     bool
	unixListener net.Listener
	unixPath     string

	projectVersionStatus api.ProjectVersionStatus
	configAutosaver      *configAutosaver
	kv                   *bboltKVManager
	dns                  *bboltKVManager

	leaseMu       sync.Mutex
	mutationLease *mutationLease
}

type mutationLease struct {
	id        string
	holder    string
	expiresAt time.Time
}

func (a *AdminAPI) Stop() {
	if a.webServer != nil {
		golog.Debug("Closing web admin server")
		_ = a.webServer.Close()
	}
	if a.webListener != nil {
		golog.Debug("Closing web admin listener")
		_ = a.webListener.Close()
	}
	if a.unixListener != nil {
		golog.Debug("Closing unix admin listener")
		_ = a.unixListener.Close()
	}
	if a.unixPath != "" {
		_ = cleanupUnixAdminSocket(a.unixPath)
	}
}

func (a *AdminAPI) Run(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	engine almagest.Engine,
	sys sysnet.System,
	logf *LogFactory,
) {
	_ = sys

	if a.webListener != nil {
		mux := http.NewServeMux()
		static, err := fs.Sub(webStatic, "static")
		if err != nil {
			golog.Errorf("Static web admin setup failed: %v", err)
			cancel()
			return
		}
		mux.Handle("/", http.FileServer(http.FS(static)))
		a.registerAPIHandlers(ctx, mux, engine, sys, logf)
		if a.webPProf {
			registerPProfHandlers(mux)
		}

		server := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		a.webServer = server

		wg.Go(func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = server.Shutdown(shutdownCtx)
		})

		wg.Go(func() {
			golog.Infof("Starting web admin listener on http://%s", a.webListener.Addr())
			if err := server.Serve(a.webListener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) &&
				!errors.Is(err, net.ErrClosed) {
				golog.Errorf("Web admin server failed: %v", err)
				cancel()
			}
		})
	}
	if a.unixListener != nil {
		wg.Go(func() {
			<-ctx.Done()
			_ = a.unixListener.Close()
		})

		wg.Go(func() {
			golog.Infof("Starting unix admin listener on %s", a.unixListener.Addr())
			a.serveUnix(ctx, cancel, wg, engine, sys, logf)
		})
	}
}

type apiSession struct {
	admin  *AdminAPI
	engine almagest.Engine
	logf   *LogFactory
	send   func(api.Message) error

	engineSubscriptions []uint64
	logSubscriptions    []uint64

	dnsMu                   sync.Mutex
	dnsNextSubscriptionID   uint64
	dnsChangeSubscriptionID uint64
	dnsSubscriptions        map[uint64]dnsSubscription

	kvMu                   sync.Mutex
	kvNextSubscriptionID   uint64
	kvChangeSubscriptionID uint64
	kvSubscriptions        map[uint64]kvSubscription
}

func (a *AdminAPI) newAPISession(engine almagest.Engine, logf *LogFactory, send func(api.Message) error) *apiSession {
	return &apiSession{
		admin:  a,
		engine: engine,
		logf:   logf,
		send:   send,
	}
}

func (s *apiSession) close() {
	for _, id := range s.engineSubscriptions {
		s.engine.UnsubscribeNotifications(id)
	}
	s.engineSubscriptions = nil
	s.closeDNSSubscriptions()
	s.closeKVSubscriptions()
	if s.logf == nil {
		return
	}
	for _, id := range s.logSubscriptions {
		s.logf.Unsubscribe(id)
	}
	s.logSubscriptions = nil
}

func (s *apiSession) handleMessage(msg api.Message) (api.Message, error) {
	if msg.Kind != api.MessageRequest {
		return newRequestError(
			msg.ID,
			api.ErrorBadRequest,
			"api endpoint accepts request messages",
		)
	}

	switch msg.Surface {
	case api.SurfaceAdmin:
		return s.handleAdminMessage(msg)
	case api.SurfaceDNS:
		return s.handleDNSMessage(msg)
	case api.SurfaceEngine:
		return s.handleEngineMessage(msg)
	case api.SurfaceKV:
		return s.handleKVMessage(msg)
	case api.SurfaceLog:
		return s.handleLogMessage(msg)
	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api surface is not implemented")
	}
}

func (s *apiSession) handleAdminMessage(msg api.Message) (api.Message, error) {
	switch api.AdminMethod(msg.Method) {
	case api.MethodAdminHello:
		_, err := decodePayload[api.HelloRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, api.HelloResponse{
			Version:              api.Version,
			ProjectVersionStatus: s.admin.projectVersionStatus,
			Surfaces: map[string]any{
				string(api.SurfaceAdmin): map[string]any{
					"methods": api.AdminMethods(),
				},
				string(api.SurfaceDNS): map[string]any{
					"methods": api.DNSMethods(),
				},
				string(api.SurfaceEngine): map[string]any{
					"methods": api.EngineMethods(),
				},
				string(api.SurfaceKV): map[string]any{
					"methods": api.KVMethods(),
				},
				string(api.SurfaceLog): map[string]any{
					"methods": api.LogMethods(),
				},
				string(api.SurfaceSystem): map[string]any{
					"methods": []string{},
				},
			},
		})

	case api.MethodAdminPing:
		req, err := decodePayload[api.PingRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, api.PingResponse(req))

	case api.MethodAdminColorMap:
		colormap := make(map[string]almagest.TypePalette)
		maps.Copy(colormap, almagest.DefaultColorMap)
		return api.NewResponse(msg.ID, colormap)

	case api.MethodAdminAcquireMutation:
		req, err := decodePayload[api.AcquireMutationRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, s.admin.acquireMutation(req))

	case api.MethodAdminRenewMutation:
		req, err := decodePayload[api.RenewMutationRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		lease, ok := s.admin.renewMutation(req)
		if !ok {
			return newRequestError(msg.ID, api.ErrorConflict, "mutation lease is not active")
		}
		return api.NewResponse(msg.ID, lease)

	case api.MethodAdminReleaseMutation:
		req, err := decodePayload[api.ReleaseMutationRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.admin.releaseMutation(req)
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (s *apiSession) handleEngineMessage(msg api.Message) (api.Message, error) {
	switch api.EngineMethod(msg.Method) {
	case api.MethodEngineSubscribeNotifications:
		_, err := decodePayload[api.WorkspaceRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		id := s.engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
			event, err := api.NewEvent(api.SurfaceEngine, string(api.EventEngineNotification), api.EngineNotificationEvent{
				SubscriptionID: notification.SubscriptionID,
				Notification:   notification,
			})
			if err == nil {
				_ = s.send(event)
			}
		})
		if id == 0 {
			return newRequestError(msg.ID, api.ErrorUnavailable, "notification subscription failed")
		}
		s.engineSubscriptions = append(s.engineSubscriptions, id)
		return api.NewResponse(msg.ID, api.SubscribeNotificationsResponse{SubscriptionID: id})

	case api.MethodEngineUnsubscribeNotifications:
		req, err := decodePayload[api.SubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.UnsubscribeNotifications(req.SubscriptionID)
		s.engineSubscriptions = withoutSubscription(s.engineSubscriptions, req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRequestFullSnapshot:
		req, err := decodePayload[api.SubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RequestFullSnapshot(req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineSubscribeNodeMenu:
		req, err := decodePayload[api.NodeMenuSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.SubscribeNodeMenu(req.Workspace, req.Node, req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineUnsubscribeNodeMenu:
		req, err := decodePayload[api.NodeMenuSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.UnsubscribeNodeMenu(req.Workspace, req.Node, req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineWorkspaceSnapshot:
		req, err := decodePayload[api.WorkspaceRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.WorkspaceSnapshot(req.Workspace)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		return api.NewResponse(msg.ID, api.WorkspaceSnapshotResponse{Snapshot: snapshot})

	case api.MethodEngineWorkersSnapshot:
		req, err := decodePayload[api.WorkspaceRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.WorkspaceSnapshot(req.Workspace)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		workers, ok := s.engine.WorkersSnapshot(req.Workspace)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		return api.NewResponse(msg.ID, workersSnapshotResponse(snapshot.Nodes, workers))

	case api.MethodEngineNodeClassSnapshot:
		req, err := decodePayload[api.ClassRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.NodeClassSnapshot(req.Workspace, req.Class)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "node class was not found")
		}
		return api.NewResponse(msg.ID, api.NodeClassSnapshotResponse{Snapshot: snapshot})

	case api.MethodEngineNodeSnapshot:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.NodeSnapshot(req.Workspace, req.ID)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "node was not found")
		}
		return api.NewResponse(msg.ID, api.NodeSnapshotResponse{Snapshot: snapshot})

	case api.MethodEnginePortSnapshot:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.PortSnapshot(req.Workspace, req.ID)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "port was not found")
		}
		return api.NewResponse(msg.ID, api.PortSnapshotResponse{Snapshot: snapshot})

	case api.MethodEngineLinkSnapshot:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, ok := s.engine.LinkSnapshot(req.Workspace, req.ID)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "link was not found")
		}
		return api.NewResponse(msg.ID, api.LinkSnapshotResponse{Snapshot: snapshot})

	case api.MethodEngineNodeClassLongDescription:
		req, err := decodePayload[api.ClassRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		description, ok := s.engine.NodeClassLongDescription(req.Workspace, req.Class)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "node class was not found")
		}
		return api.NewResponse(msg.ID, api.NodeClassLongDescriptionResponse{Description: description})

	case api.MethodEngineAddNode:
		req, err := decodePayload[api.AddNodeRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		id, err := s.engine.AddNode(req.Workspace, req.Class, req.Name)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, api.AddNodeResponse{ID: id})

	case api.MethodEngineReplaceNodeWithPlaceholder:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.engine.ReplaceNodeWithPlaceholder(req.Workspace, req.ID); err != nil {
			return newRequestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineAddLink:
		req, err := decodePayload[api.AddLinkRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		id, typ, err := s.engine.AddLink(req.Workspace, req.PortA, req.PortB)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, api.AddLinkResponse{ID: id, Type: typ})

	case api.MethodEngineRemoveNode:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNode(req.Workspace, req.ID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveNodes:
		req, err := decodePayload[api.IDsRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNodes(req.Workspace, req.IDs)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveNodesByClass:
		req, err := decodePayload[api.ClassRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNodesByClass(req.Workspace, req.Class)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveLink:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveLink(req.Workspace, req.ID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineSetNodePosition:
		req, err := decodePayload[api.SetNodePositionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.SetNodePosition(req.Workspace, req.ID, req.Position)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineMoveNodes:
		req, err := decodePayload[api.MoveNodesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.MoveNodes(req.Workspace, req.Positions)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineSetNodeName:
		req, err := decodePayload[api.SetNodeNameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.SetNodeName(req.Workspace, req.ID, req.Name)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveNodePopup:
		req, err := decodePayload[api.NodePopupRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNodePopup(req.Workspace, req.Node, req.Popup)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveNodePopups:
		req, err := decodePayload[api.NodePopupRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNodePopups(req.Workspace, req.Node)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRemoveNodePopupsByType:
		req, err := decodePayload[api.NodePopupRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.RemoveNodePopupsByType(req.Workspace, req.Node, req.Type)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineCopy:
		req, err := decodePayload[api.CopyRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, api.CopyResponse{Data: s.engine.Copy(req.Workspace, req.IDs)})

	case api.MethodEnginePaste:
		req, err := decodePayload[api.PasteRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, api.PasteResponse{IDs: s.engine.Paste(req.Workspace, req.Data)})

	case api.MethodEngineSendInbox:
		req, err := decodePayload[api.SendInboxRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.SendInbox(req.Workspace, req.InboxMessage())
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineTriggerNode:
		req, err := decodePayload[api.IDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.engine.Trigger(req.Workspace, req.ID); err != nil {
			return newRequestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineUndo:
		req, err := decodePayload[api.WorkspaceRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.Undo(req.Workspace)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineRedo:
		req, err := decodePayload[api.WorkspaceRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.Redo(req.Workspace)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodEngineSendNodeFormularMessage:
		req, err := decodePayload[api.SendNodeFormularMessageRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		message, err := decodeFormularMessage(req.Message)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.engine.SendNodeFormularMsg(req.Workspace, req.Node, message)
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

// workersSnapshotResponse trims workspace and worker snapshots down to worker-system
// diagnostics. The node map intentionally keeps only live worker parents, which
// lets clients distinguish placeholders from workers whose parent node is gone.
func workersSnapshotResponse(snapshotNodes map[uint64]pasta.NodeSnapshot, workers map[uint64]almagest.WorkerSnapshot) api.WorkersSnapshotResponse {
	nodes := make(map[uint64]pasta.NodeSnapshot)
	for _, worker := range workers {
		node, ok := snapshotNodes[worker.Node]
		if !ok {
			continue
		}
		nodes[worker.Node] = node
	}
	if workers == nil {
		workers = map[uint64]almagest.WorkerSnapshot{}
	}
	return api.WorkersSnapshotResponse{
		Nodes:   nodes,
		Workers: workers,
	}
}

func (s *apiSession) handleLogMessage(msg api.Message) (api.Message, error) {
	switch api.LogMethod(msg.Method) {
	case api.MethodLogSubscribe:
		id, entries := s.logf.Subscribe(func(entry api.LogEntry) {
			event, err := api.NewEvent(api.SurfaceLog, string(api.EventLogEntry), entry)
			if err == nil {
				_ = s.send(event)
			}
		})
		s.logSubscriptions = append(s.logSubscriptions, id)
		return api.NewResponse(msg.ID, api.SubscribeLogsResponse{
			SubscriptionID: id,
			CacheSize:      s.logf.TotalCacheSize(),
			Entries:        entries,
		})

	case api.MethodLogUnsubscribe:
		req, err := decodePayload[api.SubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.logf.Unsubscribe(req.SubscriptionID)
		s.logSubscriptions = withoutSubscription(s.logSubscriptions, req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func withoutSubscription(ids []uint64, target uint64) []uint64 {
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

func decodePayload[T any](msg api.Message) (T, error) {
	return api.DecodePayload[T](msg.Payload)
}

func newRequestError(id string, code api.ErrorCode, message string) (api.Message, error) {
	if id == "" {
		id = "0"
	}
	return api.NewErrorResponse(id, code, message, nil)
}

func (a *AdminAPI) acquireMutation(req api.AcquireMutationRequest) api.AcquireMutationResponse {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()

	now := time.Now()
	if a.mutationLease != nil && a.mutationLease.expiresAt.After(now) {
		return api.AcquireMutationResponse{
			Acquired: false,
			Active:   a.mutationLease.apiLease(),
		}
	}

	ttl := mutationLeaseTTL(req.TTLMilliseconds)
	a.mutationLease = &mutationLease{
		id:        strconv.FormatInt(now.UnixNano(), 36),
		holder:    req.Holder,
		expiresAt: now.Add(ttl),
	}
	return api.AcquireMutationResponse{
		Acquired: true,
		Lease:    a.mutationLease.apiLease(),
	}
}

func (a *AdminAPI) renewMutation(req api.RenewMutationRequest) (*api.MutationLease, bool) {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()

	if a.mutationLease == nil || a.mutationLease.id != req.LeaseID || !a.mutationLease.expiresAt.After(time.Now()) {
		return nil, false
	}
	a.mutationLease.expiresAt = time.Now().Add(mutationLeaseTTL(req.TTLMilliseconds))
	return a.mutationLease.apiLease(), true
}

func (a *AdminAPI) releaseMutation(req api.ReleaseMutationRequest) {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()

	if a.mutationLease != nil && a.mutationLease.id == req.LeaseID {
		a.mutationLease = nil
	}
}

func (l *mutationLease) apiLease() *api.MutationLease {
	return &api.MutationLease{
		ID:        l.id,
		Holder:    l.holder,
		ExpiresAt: l.expiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func mutationLeaseTTL(milliseconds int64) time.Duration {
	if milliseconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func PrepareAdminAPI(cfg *Config) (*AdminAPI, error) {
	api := AdminAPI{}
	if cfg.Protected.Admin.Web != 0 {
		golog.Warn("Web admin API is enabled; unix socket admin API is safer for local administration")
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(cfg.Protected.Admin.Web)))
		listener, err := listenWebAdmin(addr, cfg.Protected.Admin.WebFilters)
		if err != nil {
			return nil, err
		}
		api.webListener = listener
		api.webPProf = cfg.Protected.Admin.WebPProf
		if api.webPProf {
			golog.Warn("Web admin pprof endpoint is enabled on /debug/pprof/")
		}
	}
	if cfg.Protected.Admin.Unix != "" {
		listener, err := listenUnixAdmin(cfg.Protected.Admin.Unix, cfg.Protected.Admin.UnixFilters)
		if err != nil {
			return nil, err
		}
		api.unixListener = listener
		api.unixPath = cfg.Protected.Admin.Unix
	}
	return &api, nil
}

func listenWebAdmin(address string, filters AdminPeerFiltersConfig) (net.Listener, error) {
	if !filters.Enabled() {
		return net.Listen("tcp", address)
	}
	return protected.Listen("tcp", address, protected.Rules{
		UIDs:      filters.UIDs,
		GIDs:      filters.GIDs,
		Usernames: filters.Usernames,
	})
}
