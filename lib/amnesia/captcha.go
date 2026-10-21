package amnesianodes

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
)

const (
	amnesiaCaptchaBlockID  = "captcha"
	amnesiaCaptchaButtonID = "completeCaptcha"
	amnesiaCaptchaPopup    = "@amnesia_captcha_required"
)

type amnesiaCaptchaPrompt struct {
	runtimeID uint64
	dialogID  string
	request   wgoamnesia.InteractionRequest
}

func newAmnesiaCaptchaPrompt(runtimeID uint64, request *wgoamnesia.InteractionRequest) (*amnesiaCaptchaPrompt, error) {
	if request == nil || request.Kind != wgoamnesia.InteractionCaptcha || request.Captcha == nil {
		return nil, errors.New("amnezia API requested an unsupported interaction")
	}
	if strings.TrimSpace(request.Captcha.ImageBase64) == "" {
		return nil, errors.New("amnezia API returned a captcha without an image")
	}
	if _, err := base64.StdEncoding.DecodeString(request.Captcha.ImageBase64); err != nil {
		return nil, fmt.Errorf("amnezia API returned an invalid captcha image: %w", err)
	}
	prompt := &amnesiaCaptchaPrompt{
		runtimeID: runtimeID,
		dialogID:  fmt.Sprintf("amnesia-captcha-%d-%d", runtimeID, request.Attempt),
		request:   cloneAmnesiaInteractionRequest(*request),
	}
	if err := prompt.dialog().Validate(); err != nil {
		return nil, fmt.Errorf("create Amnezia captcha dialog: %w", err)
	}
	return prompt, nil
}

func cloneAmnesiaCaptchaPrompt(prompt *amnesiaCaptchaPrompt) *amnesiaCaptchaPrompt {
	if prompt == nil {
		return nil
	}
	return &amnesiaCaptchaPrompt{
		runtimeID: prompt.runtimeID,
		dialogID:  prompt.dialogID,
		request:   cloneAmnesiaInteractionRequest(prompt.request),
	}
}

func cloneAmnesiaInteractionRequest(request wgoamnesia.InteractionRequest) wgoamnesia.InteractionRequest {
	if request.Captcha != nil {
		captcha := *request.Captcha
		request.Captcha = &captcha
	}
	return request
}

func (p *amnesiaCaptchaPrompt) dialog() formular.Dialog {
	captcha := p.request.Captcha
	mimeType := "image/png"
	if image, err := base64.StdEncoding.DecodeString(captcha.ImageBase64); err == nil {
		if detected := http.DetectContentType(image); strings.HasPrefix(detected, "image/") {
			mimeType = detected
		}
	}
	text := strings.TrimSpace(p.request.Message)
	if text == "" {
		text = "Enter the text shown in the image."
	}
	if hint := strings.TrimSpace(captcha.Hint); hint != "" {
		text += "\n\nHint: " + hint
	}
	dialog := formular.CaptchaDialog(p.dialogID, "Complete captcha", text, formular.DialogResource{
		ID:       "challenge",
		MIMEType: mimeType,
		Data:     captcha.ImageBase64,
		Alt:      "Captcha challenge",
	})
	dialog.Placeholder = "Captcha text"
	dialog.SubmitLabel = "Complete captcha"
	return dialog
}

func (n *managedAmnesiaNode) captchaBlock() formular.Block {
	return formular.Block{ID: amnesiaCaptchaBlockID, Order: 0, Generation: 1, Items: []formular.Item{
		formular.Button(amnesiaCaptchaButtonID, "Complete captcha"),
	}}
}

func (n *managedAmnesiaNode) showCaptchaDialog() {
	if n.W == nil || n.ID == 0 || n.captchaPrompt == nil {
		return
	}
	n.logDebugf(
		"Amnesia captcha dialog requested: runtime=%d attempt=%d",
		n.captchaPrompt.runtimeID, n.captchaPrompt.request.Attempt,
	)
	n.W.SendNodeMenuMsgLocked(n.ID, formular.DialogCreate(
		pasta.NodeMenuID(n.ID), 1, n.captchaPrompt.dialog(),
	))
}

func (n *managedAmnesiaNode) handleCaptchaResponse(msg formular.DialogResponseMessage) {
	prompt := n.captchaPrompt
	if prompt == nil || msg.MenuID != pasta.NodeMenuID(n.ID) || msg.DialogID != prompt.dialogID {
		n.logDebugf("Amnesia captcha response ignored: current_prompt=%t", prompt != nil)
		return
	}
	if msg.Value == nil {
		n.logDebugf("Amnesia captcha dialog canceled: runtime=%d attempt=%d", prompt.runtimeID, prompt.request.Attempt)
		return
	}
	solution, ok := formular.StringValue(msg.Value)
	if !ok {
		n.logDebugf(
			"Amnesia captcha response ignored: runtime=%d attempt=%d invalid_value_type=%T",
			prompt.runtimeID, prompt.request.Attempt, msg.Value,
		)
		return
	}
	if strings.TrimSpace(solution) == "" {
		n.logDebugf("Amnesia captcha response ignored: runtime=%d attempt=%d empty=true", prompt.runtimeID, prompt.request.Attempt)
		return
	}
	n.logDebugf("Amnesia captcha response queued: runtime=%d attempt=%d", prompt.runtimeID, prompt.request.Attempt)
	n.captchaResponse = &managedAmnesiaCaptchaResponse{
		runtimeID: prompt.runtimeID,
		dialogID:  prompt.dialogID,
		solution:  solution,
	}
	n.clearCaptchaPrompt()
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile(true)
}

func (n *managedAmnesiaNode) setCaptchaPrompt(prompt *amnesiaCaptchaPrompt) {
	n.captchaPrompt = cloneAmnesiaCaptchaPrompt(prompt)
	n.logDebugf(
		"Amnesia captcha prompt published: runtime=%d attempt=%d reason=%s",
		prompt.runtimeID, prompt.request.Attempt, prompt.request.Captcha.Reason,
	)
	if n.W == nil || n.ID == 0 || n.captchaPopupID != 0 {
		return
	}
	id, err := n.W.AddNodePopupLocked(n.ID, pasta.NodePopupWard, amnesiaCaptchaPopup, true)
	if err != nil {
		if n.Logger != nil {
			n.Logger.Errf("failed to add Amnesia captcha popup node=%d: %v", n.ID, err)
		}
		return
	}
	n.captchaPopupID = id
	n.logDebugf("Amnesia captcha popup added: runtime=%d popup=%d", prompt.runtimeID, id)
}

func (n *managedAmnesiaNode) clearCaptchaPrompt() {
	prompt := n.captchaPrompt
	popupID := n.captchaPopupID
	n.captchaPrompt = nil
	if n.W != nil && n.ID != 0 && n.captchaPopupID != 0 {
		if err := n.W.RemoveNodePopupLocked(n.ID, n.captchaPopupID); err != nil && n.Logger != nil {
			n.Logger.Errf("failed to remove Amnesia captcha popup node=%d popup=%d: %v", n.ID, n.captchaPopupID, err)
		}
	}
	n.captchaPopupID = 0
	if prompt != nil {
		n.logDebugf(
			"Amnesia captcha prompt cleared: runtime=%d attempt=%d popup=%d",
			prompt.runtimeID, prompt.request.Attempt, popupID,
		)
	}
}
