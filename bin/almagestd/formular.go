package main

import (
	"encoding/json"
	"fmt"

	"github.com/asciimoth/formular"
)

func decodeFormularMessage(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("missing formular message")
	}

	var base formular.MessageBase
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	switch base.Type {
	case formular.MessageFieldUpdate:
		return decodeFormularMessageAs[formular.FieldUpdateMessage](data)
	case formular.MessageFieldValidate:
		return decodeFormularMessageAs[formular.FieldValidateMessage](data)
	case formular.MessageFormApply:
		return decodeFormularMessageAs[formular.FormApplyMessage](data)
	case formular.MessageButtonPress:
		return decodeFormularMessageAs[formular.ButtonPressMessage](data)
	case formular.MessageAutocompleteRequest:
		return decodeFormularMessageAs[formular.AutocompleteRequestMessage](data)
	case formular.MessageDialogResponse:
		return decodeFormularMessageAs[formular.DialogResponseMessage](data)
	default:
		return nil, fmt.Errorf("unsupported formular message type %q", base.Type)
	}
}

func decodeFormularMessageAs[T any](data json.RawMessage) (T, error) {
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
