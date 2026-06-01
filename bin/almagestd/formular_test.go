package main

import (
	"encoding/json"
	"testing"

	"github.com/asciimoth/formular"
)

func TestDecodeFormularButtonPressMessage(t *testing.T) {
	msg, err := decodeFormularMessage(json.RawMessage(`{
		"type": "button.press",
		"menuId": "node:7",
		"menuGeneration": 1,
		"blockGeneration": 1,
		"blockId": "request",
		"buttonId": "send"
	}`))
	if err != nil {
		t.Fatalf("decodeFormularMessage error = %v", err)
	}

	button, ok := msg.(formular.ButtonPressMessage)
	if !ok {
		t.Fatalf("message type = %T, want formular.ButtonPressMessage", msg)
	}
	if button.MenuID != "node:7" || button.BlockID != "request" || button.ButtonID != "send" {
		t.Fatalf("unexpected button message: %#v", button)
	}
}

func TestDecodeFormularMessageRejectsUnknownType(t *testing.T) {
	_, err := decodeFormularMessage(json.RawMessage(`{"type":"menu.snapshot","menuId":"node:7"}`))
	if err == nil {
		t.Fatal("decodeFormularMessage error = nil, want error")
	}
}
