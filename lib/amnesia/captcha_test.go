package amnesianodes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
)

const amnesiaTestCaptchaPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestManagedAmnesiaCaptchaAddsPopupAndTopMenuButton(t *testing.T) {
	logFactory := logging.NewDebugLogFactory(64)
	w := pasta.NewWorkspace(logFactory)
	t.Cleanup(w.Close)
	node := &managedAmnesiaNode{config: managedAmnesiaConfig{enabled: true, connectionSecret: "connection"}}
	id, err := w.AddNode(node, NodeTypeAmnesia, "captcha test")
	if err != nil {
		t.Fatalf("add node: %v", err)
	}

	prompt := testAmnesiaCaptchaPrompt(t, 8, 1)
	w.Lock()
	node.handleRuntimeResult(managedAmnesiaRuntimeResult{
		generation: node.generation.Load(),
		format:     wgoamnesia.InputFormatServiceKey,
		captcha:    prompt,
	})
	w.Unlock()

	snapshot, ok := w.NodeSnapshot(id)
	if !ok || len(snapshot.Popups) != 1 {
		t.Fatalf("captcha node snapshot = %#v", snapshot)
	}
	if popup := snapshot.Popups[0]; popup.Type != pasta.NodePopupWard || popup.Text != amnesiaCaptchaPopup {
		t.Fatalf("captcha popup = %#v", popup)
	}
	if node.stateText() != "waiting for captcha" {
		t.Fatalf("captcha node state = %q", node.stateText())
	}
	blocks := node.menuBlocks()
	if len(blocks) != 3 || blocks[0].ID != amnesiaCaptchaBlockID || blocks[0].Order != 0 {
		t.Fatalf("captcha menu blocks = %#v", blocks)
	}
	button, ok := amnesiaMenuItem(blocks[0], amnesiaCaptchaButtonID)
	if !ok || button.Type != formular.ItemButton || button.Label != "Complete captcha" {
		t.Fatalf("captcha menu button = %#v", button)
	}
	logs := drainAmnesiaCaptchaLogs(logFactory)
	for _, expected := range []string{"captcha prompt published", "captcha popup added"} {
		if !strings.Contains(logs, expected) {
			t.Errorf("captcha UI logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
}

func TestManagedAmnesiaCaptchaButtonCreatesDialog(t *testing.T) {
	logFactory := logging.NewDebugLogFactory(64)
	w := pasta.NewWorkspace(logFactory)
	t.Cleanup(w.Close)
	node := &managedAmnesiaNode{}
	id, err := w.AddNode(node, NodeTypeAmnesia, "captcha dialog test")
	if err != nil {
		t.Fatalf("add node: %v", err)
	}
	node.captchaPrompt = testAmnesiaCaptchaPrompt(t, 5, 2)

	var notifications []pasta.WorkspaceNotification
	subscription := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		notifications = append(notifications, notification)
	})
	if !w.SubscribeNodeMenu(id, subscription) {
		t.Fatal("subscribe to node menu returned false")
	}
	w.SendNodeFormularMsg(id, formular.ButtonPressMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(id)},
		BlockID:     amnesiaCaptchaBlockID,
		ButtonID:    amnesiaCaptchaButtonID,
	})

	var created *formular.DialogCreateMessage
	for i := range notifications {
		if message, ok := notifications[i].Formular.(formular.DialogCreateMessage); ok {
			created = &message
		}
	}
	if created == nil {
		t.Fatalf("node menu notifications do not contain a captcha dialog: %#v", notifications)
	}
	if err := created.Validate(); err != nil {
		t.Fatalf("captcha dialog validation: %v", err)
	}
	if created.Dialog.ID != node.captchaPrompt.dialogID || created.Dialog.Kind != formular.DialogKindCaptcha ||
		created.Dialog.Title != "Complete captcha" || len(created.Dialog.Resources) != 1 ||
		created.Dialog.Resources[0].Data != amnesiaTestCaptchaPNG {
		t.Fatalf("captcha dialog = %#v", created.Dialog)
	}
	if created.Dialog.Text != "Verify this request.\n\nHint: digits only" {
		t.Fatalf("captcha dialog text = %q", created.Dialog.Text)
	}
	w.SendNodeFormularMsg(id, formular.DialogResponseMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageDialogResponse, MenuID: pasta.NodeMenuID(id)},
		DialogID:    node.captchaPrompt.dialogID,
		Value:       nil,
	})
	logs := drainAmnesiaCaptchaLogs(logFactory)
	for _, expected := range []string{"captcha dialog requested", "captcha dialog canceled"} {
		if !strings.Contains(logs, expected) {
			t.Errorf("captcha dialog logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
}

func TestManagedAmnesiaCaptchaCancelKeepsPromptAndAnswerQueuesResume(t *testing.T) {
	prompt := testAmnesiaCaptchaPrompt(t, 13, 1)
	node := &managedAmnesiaNode{captchaPrompt: cloneAmnesiaCaptchaPrompt(prompt)}
	node.ID = 7
	base := formular.MessageBase{Type: formular.MessageDialogResponse, MenuID: pasta.NodeMenuID(node.ID)}

	node.handleCaptchaResponse(formular.DialogResponseMessage{
		MessageBase: base,
		DialogID:    prompt.dialogID,
		Value:       nil,
	})
	if node.captchaPrompt == nil || node.captchaResponse != nil {
		t.Fatal("canceling the captcha did not keep the pending prompt")
	}

	node.handleCaptchaResponse(formular.DialogResponseMessage{
		MessageBase: base,
		DialogID:    prompt.dialogID,
		Value:       " １２ 3a ",
	})
	if node.captchaPrompt != nil || node.captchaResponse == nil {
		t.Fatal("submitting the captcha did not queue a resume request")
	}
	if response := node.captchaResponse; response.runtimeID != prompt.runtimeID ||
		response.dialogID != prompt.dialogID || response.solution != " １２ 3a " {
		t.Fatalf("captcha resume request = %#v", response)
	}
}

func TestManagedAmnesiaCaptchaResumesSameImportAndHandlesRefresh(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})
	privateKey := amnesiaTestPrivateKey(t)
	profile := amnesiaTestProfile(privateKey.PublicKey(), amnesiaTestPrivateKey(t).PublicKey(), "de")
	profile.Interface.PrivateKey = privateKey.Base64()
	firstRequest := testAmnesiaCaptchaPrompt(t, 1, 1).request
	firstRequest.Message = "secret initial captcha message"
	firstRequest.Captcha.ID = "secret-initial-challenge-id"
	firstRequest.Captcha.Reason = wgoamnesia.ErrorCaptchaRequired
	refreshedRequest := testAmnesiaCaptchaPrompt(t, 1, 2).request
	refreshedRequest.Message = "secret refreshed captcha message"
	refreshedRequest.Captcha.ID = "secret-refreshed-challenge-id"
	refreshedRequest.Captcha.Reason = wgoamnesia.ErrorCaptchaInvalid
	session := &recordingCaptchaImportSession{
		next: wgoamnesia.Step{
			State:       wgoamnesia.NegotiationAwaitingInteraction,
			Interaction: &firstRequest,
		},
		resume: []wgoamnesia.Step{
			{State: wgoamnesia.NegotiationAwaitingInteraction, Interaction: &refreshedRequest},
			{State: wgoamnesia.NegotiationComplete, Profile: profile},
		},
	}
	client := &captchaTestClient{}
	logFactory := logging.NewDebugLogFactory(128)
	node := &managedAmnesiaNode{
		Base: nodekit.Base{Logger: logFactory.NodeLogger(42, NodeTypeAmnesia)},
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "captcha"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: func(wgoamnesia.ClientOptions) (Client, error) { return client, nil },
		startImport: func(
			gotClient Client,
			_ string,
			options wgoamnesia.NegotiationOptions,
		) (managedAmnesiaImportSession, error) {
			if gotClient != client {
				t.Fatal("resumable import received a different client")
			}
			if options.InteractionPolicy != wgoamnesia.PauseOnInteraction {
				t.Fatalf("interaction policy = %d, want PauseOnInteraction", options.InteractionPolicy)
			}
			return session, nil
		},
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource, network: network,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	first := runManagedAmnesiaReconcile(t, node, &req)
	if first.captcha == nil || first.runtimeID == 0 || node.runtimeImport != session {
		t.Fatalf("initial captcha result = %#v", first)
	}
	runtimeID := first.runtimeID
	req.captcha = &managedAmnesiaCaptchaResponse{
		runtimeID: runtimeID,
		dialogID:  first.captcha.dialogID,
		solution:  "secret-wrong-answer",
	}
	refreshed := runManagedAmnesiaReconcile(t, node, &req)
	if refreshed.captcha == nil || refreshed.runtimeID != runtimeID ||
		refreshed.captcha.dialogID == first.captcha.dialogID {
		t.Fatalf("refreshed captcha result = %#v", refreshed)
	}
	req.captcha = &managedAmnesiaCaptchaResponse{
		runtimeID: runtimeID,
		dialogID:  refreshed.captcha.dialogID,
		solution:  "secret-correct-answer",
	}
	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !prepared.prepared || prepared.runtimeID != runtimeID || node.runtimeImport != nil || node.runtimeConfig != profile {
		t.Fatalf("completed captcha import = %#v", prepared)
	}
	if len(session.responses) != 2 || session.responses[0].CaptchaSolution != "secret-wrong-answer" ||
		session.responses[1].CaptchaSolution != "secret-correct-answer" {
		t.Fatalf("captcha responses = %#v", session.responses)
	}
	logs := drainAmnesiaCaptchaLogs(logFactory)
	for _, expected := range []string{
		"resumable import started",
		"captcha challenge received",
		"reason=captcha_required",
		"captcha submission started",
		"captcha submission returned another challenge",
		"previous_attempt=1",
		"reason=captcha_invalid",
		"captcha submission completed",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("captcha runtime logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
	for _, secret := range []string{
		amnesiaTestCaptchaPNG,
		"secret initial captcha message",
		"secret refreshed captcha message",
		"secret-initial-challenge-id",
		"secret-refreshed-challenge-id",
		"secret-wrong-answer",
		"secret-correct-answer",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("captcha runtime logs expose secret %q\nlogs:\n%s", secret, logs)
		}
	}
}

func testAmnesiaCaptchaPrompt(t *testing.T, runtimeID uint64, attempt int) *amnesiaCaptchaPrompt {
	t.Helper()
	prompt, err := newAmnesiaCaptchaPrompt(runtimeID, &wgoamnesia.InteractionRequest{
		Kind:    wgoamnesia.InteractionCaptcha,
		Message: "Verify this request.",
		Attempt: attempt,
		Captcha: &wgoamnesia.CaptchaChallenge{
			ID:          "server-challenge",
			ImageBase64: amnesiaTestCaptchaPNG,
			Hint:        "digits only",
		},
	})
	if err != nil {
		t.Fatalf("create captcha prompt: %v", err)
	}
	return prompt
}

func drainAmnesiaCaptchaLogs(factory *logging.DebugLogFactory) string {
	var messages strings.Builder
	for _, entry := range factory.Drain() {
		messages.WriteString(entry.Message)
		messages.WriteByte('\n')
	}
	return messages.String()
}

type captchaTestClient struct{}

func (*captchaTestClient) ImportNonInteractive(
	context.Context,
	string,
	wgoamnesia.NegotiationOptions,
) (*wgoamnesia.Profile, error) {
	return nil, errors.New("unexpected non-interactive import")
}

type recordingCaptchaImportSession struct {
	next      wgoamnesia.Step
	resume    []wgoamnesia.Step
	responses []wgoamnesia.InteractionResponse
}

func (s *recordingCaptchaImportSession) Next(context.Context) (wgoamnesia.Step, error) {
	return s.next, nil
}

func (s *recordingCaptchaImportSession) Resume(
	_ context.Context,
	response wgoamnesia.InteractionResponse,
) (wgoamnesia.Step, error) {
	s.responses = append(s.responses, response)
	if len(s.resume) == 0 {
		return wgoamnesia.Step{}, errors.New("unexpected extra captcha response")
	}
	step := s.resume[0]
	s.resume = s.resume[1:]
	return step, nil
}
