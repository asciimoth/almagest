package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestCapabilityPreservesUnknownAttempt(t *testing.T) {
	unknown := protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}
	if unknown.State != protocol.CapabilityUnknown || !unknown.CanAttempt() {
		t.Fatalf("unknown capability = %#v, want attempt allowed", unknown)
	}
	unsupported := protocol.Capability{State: protocol.CapabilityUnsupported}
	if unsupported.CanAttempt() {
		t.Fatalf("unsupported capability = %#v, want attempt denied", unsupported)
	}
}

func TestUnknownOperationIsInvalid(t *testing.T) {
	if protocol.Operation("ioctl").Valid() {
		t.Fatal("generic ioctl operation is valid")
	}
	if !protocol.OperationCreateTun.Valid() {
		t.Fatal("typed create-TUN operation is invalid")
	}
}

func TestMutationResultDistinguishesUncertain(t *testing.T) {
	result := protocol.MutationResult{OperationID: "tun-replace-1", Outcome: protocol.OutcomeUncertain}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	result.Outcome = "success-ish"
	if err := result.Validate(); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("unknown result outcome error = %v", err)
	}
}

func TestResourceReferenceRejectsForgedForeignAndStaleValues(t *testing.T) {
	session := testSession(t)
	resourceID, err := protocol.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.ResourceRef{ID: resourceID, SessionID: session.ID, Type: protocol.ResourceTun, Generation: 2, GraphEpoch: 3}
	if err := ref.Validate(session, protocol.ResourceTun, 3, 2); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		mutate func(*protocol.ResourceRef)
		want   error
	}{
		"forged ID":        {func(ref *protocol.ResourceRef) { ref.ID = protocol.ResourceID{} }, protocol.ErrInvalidContract},
		"foreign session":  {func(ref *protocol.ResourceRef) { ref.SessionID = protocol.SessionID{1} }, protocol.ErrForeignResource},
		"wrong type":       {func(ref *protocol.ResourceRef) { ref.Type = protocol.ResourceSocket }, protocol.ErrResourceType},
		"stale generation": {func(ref *protocol.ResourceRef) { ref.Generation-- }, protocol.ErrStaleGeneration},
		"stale graph":      {func(ref *protocol.ResourceRef) { ref.GraphEpoch-- }, protocol.ErrStaleGraphEpoch},
	} {
		t.Run(name, func(t *testing.T) {
			changed := ref
			test.mutate(&changed)
			if err := changed.Validate(session, protocol.ResourceTun, 3, 2); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestFakeMutationServiceCancellationAndLostReply(t *testing.T) {
	session := testSession(t)
	service := newFakeMutationService(session)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	meta := testMutationMeta(session, "replace-1")
	result, err := service.replace(ctx, meta, "revision-a", "content-a", true)
	if !errors.Is(err, context.Canceled) || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("lost reply = %#v, %v; want committed receipt and canceled reply", result, err)
	}

	recovered, err := service.result("replace-1")
	if err != nil || recovered != result {
		t.Fatalf("GetResult() = %#v, %v; want %#v", recovered, err, result)
	}
	if _, err := service.replace(context.Background(), meta, "revision-a", "different", false); !errors.Is(err, protocol.ErrOperationConflict) {
		t.Fatalf("reused operation ID error = %v, want conflict", err)
	}
	if _, err := service.result("expired"); !errors.Is(err, protocol.ErrOperationUnknown) {
		t.Fatalf("unknown receipt error = %v", err)
	}
}

func TestFakeMutationServiceRejectsRevisionConflictBeforeCommit(t *testing.T) {
	session := testSession(t)
	service := newFakeMutationService(session)
	result, err := service.replace(context.Background(), testMutationMeta(session, "replace-conflict"), "wrong", "content", false)
	if err == nil || result.Outcome != protocol.OutcomeRejected || service.revision != "revision-a" {
		t.Fatalf("conflict = %#v, %v, revision %q", result, err, service.revision)
	}
}

func testSession(t *testing.T) protocol.Session {
	t.Helper()
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}
}

func testMutationMeta(session protocol.Session, operationID string) protocol.RequestMeta {
	return protocol.RequestMeta{RequestID: 1, SessionID: session.ID, SessionEpoch: session.Epoch, GraphEpoch: 1, OperationID: operationID}
}

type fakeMutationService struct {
	mu       sync.Mutex
	session  protocol.Session
	revision string
	receipts map[string]fakeReceipt
}

type fakeReceipt struct {
	result   protocol.MutationResult
	expected string
	content  string
}

func newFakeMutationService(session protocol.Session) *fakeMutationService {
	return &fakeMutationService{session: session, revision: "revision-a", receipts: make(map[string]fakeReceipt)}
}

func (s *fakeMutationService) replace(ctx context.Context, meta protocol.RequestMeta, expected, content string, loseReply bool) (protocol.MutationResult, error) {
	if err := meta.Validate(s.session, true); err != nil {
		return protocol.MutationResult{OperationID: meta.OperationID, Outcome: protocol.OutcomeRejected}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.receipts[meta.OperationID]; exists {
		if old.expected != expected || old.content != content {
			return protocol.MutationResult{}, protocol.ErrOperationConflict
		}
		return old.result, nil
	}
	if expected != s.revision {
		result := protocol.MutationResult{OperationID: meta.OperationID, Outcome: protocol.OutcomeRejected, Code: "revision_conflict"}
		s.receipts[meta.OperationID] = fakeReceipt{result: result, expected: expected, content: content}
		return result, errors.New("revision conflict")
	}
	s.revision = "revision-b"
	result := protocol.MutationResult{OperationID: meta.OperationID, Outcome: protocol.OutcomeCommitted}
	s.receipts[meta.OperationID] = fakeReceipt{result: result, expected: expected, content: content}
	if loseReply {
		return result, ctx.Err()
	}
	return result, nil
}

func (s *fakeMutationService) result(operationID string) (protocol.MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, exists := s.receipts[operationID]
	if !exists {
		return protocol.MutationResult{}, protocol.ErrOperationUnknown
	}
	return receipt.result, nil
}
