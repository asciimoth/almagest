package debugnodes

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// NodeTypeDebugKV is the class name of the debug KV editor node.
	NodeTypeDebugKV = "almagest/DebugKV"

	debugKVEntryBlockID  = "entry"
	debugKVResultBlockID = "result"

	debugKVStoreField  = "store"
	debugKVPluginField = "plugin"
	debugKVPathField   = "path"
	debugKVKeyField    = "key"
	debugKVValueField  = "value"
	debugKVStringField = "string"
	debugKVHiddenField = "hidden"

	debugKVReadButton   = "read"
	debugKVSetButton    = "set"
	debugKVDeleteButton = "delete"

	debugKVStoreDefault = "default"
	debugKVStorePlugin  = "plugin"
)

var errDebugKVUnavailable = errors.New("KV storage is unavailable")

// DebugKVClass creates debug-only nodes that edit KV storage from a node menu.
type DebugKVClass struct {
	Runtime Runtime

	// Manager overrides the runtime KV manager. It is useful for standalone
	// workspaces and tests.
	Manager storage.KVManager
}

func (DebugKVClass) ClassName() string        { return NodeTypeDebugKV }
func (DebugKVClass) ShortDescription() string { return "DEBUG KV storage editor" }
func (DebugKVClass) LongDescription() string {
	return "Edits KV storage entries for debugging."
}
func (DebugKVClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{}}
}

func (c DebugKVClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	manager := c.Manager
	if manager == nil && c.Runtime != nil {
		manager = c.Runtime.KVManager()
	}
	return &debugKVNode{
		Base:      nodekit.Base{Runtime: c.Runtime},
		manager:   manager,
		storeKind: debugKVStoreDefault,
		pathText:  "[]",
		isString:  true,
		status:    "ready",
	}, nil
}

type debugKVNode struct {
	nodekit.Base

	manager storage.KVManager

	storeKind string
	plugin    string
	pathText  string
	key       string
	value     string
	isString  bool
	hidden    bool

	status  string
	errText string
}

func (n *debugKVNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.sendMenuSnapshot(false)
	return nil
}

func (n *debugKVNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FieldUpdateMessage:
		n.handleFieldUpdate(msg)
	case formular.FieldValidateMessage:
		n.handleFieldValidate(msg)
	case formular.ButtonPressMessage:
		n.handleButtonPress(msg)
	}
	return nil
}

func (n *debugKVNode) handleFieldUpdate(msg formular.FieldUpdateMessage) {
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != debugKVEntryBlockID || len(msg.Field.ElementPath) != 0 {
		return
	}

	valid := true
	switch msg.Field.FieldID {
	case debugKVStoreField:
		value, ok := formular.StringValue(msg.Value)
		if !ok || value != debugKVStoreDefault && value != debugKVStorePlugin {
			valid = false
		} else {
			n.storeKind = value
		}
	case debugKVPluginField:
		value, ok := formular.StringValue(msg.Value)
		valid = ok
		if ok {
			n.plugin = value
		}
	case debugKVPathField:
		value, ok := formular.StringValue(msg.Value)
		valid = ok
		if ok {
			n.pathText = value
		}
	case debugKVKeyField:
		value, ok := formular.StringValue(msg.Value)
		valid = ok
		if ok {
			n.key = value
		}
	case debugKVValueField:
		value, ok := formular.StringValue(msg.Value)
		valid = ok
		if ok {
			n.value = value
		}
	case debugKVStringField:
		value, ok := formular.BoolValue(msg.Value)
		valid = ok
		if ok {
			n.isString = value
		}
	case debugKVHiddenField:
		value, ok := formular.BoolValue(msg.Value)
		valid = ok
		if ok {
			n.hidden = value
		}
	default:
		return
	}

	if !valid {
		n.sendEntryBlock()
		return
	}
	n.status = "ready"
	n.errText = ""
	n.sendEntryBlock()
	n.sendResultBlock()
}

func (n *debugKVNode) handleFieldValidate(msg formular.FieldValidateMessage) {
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != debugKVEntryBlockID || len(msg.Field.ElementPath) != 0 {
		return
	}

	var err error
	switch msg.Field.FieldID {
	case debugKVPluginField:
		value, ok := formular.StringValue(msg.Value)
		if !ok || n.storeKind == debugKVStorePlugin && value == "" {
			err = errors.New("plugin name is required for a plugin store")
		}
	case debugKVPathField:
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			err = errors.New("path must be text")
		} else {
			_, err = parseDebugKVPath(value)
		}
	case debugKVKeyField:
		value, ok := formular.StringValue(msg.Value)
		if !ok || value == "" {
			err = errors.New("key is required")
		}
	case debugKVValueField:
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			err = errors.New("value must be text")
		} else if !n.isString {
			_, err = base64.StdEncoding.DecodeString(value)
			if err != nil {
				err = errors.New("value is not valid base64")
			}
		}
	default:
		return
	}
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *debugKVNode) handleButtonPress(msg formular.ButtonPressMessage) {
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != debugKVEntryBlockID || len(msg.ElementPath) != 0 {
		return
	}

	switch msg.ButtonID {
	case debugKVReadButton:
		n.read()
	case debugKVSetButton:
		n.set()
	case debugKVDeleteButton:
		n.delete()
	}
}

func (n *debugKVNode) read() {
	store, err := n.store()
	if err != nil {
		n.setError("read failed", err)
		return
	}

	var value *storage.KValue
	err = store.View(n.Context(), func(tx storage.KVReadTx) error {
		stored, found, getErr := tx.Get(n.key)
		if getErr != nil || !found {
			return getErr
		}
		clone := stored.Clone()
		value = &clone
		return nil
	})
	if err != nil {
		n.setError("read failed", err)
		return
	}
	if value == nil {
		n.status = "not found"
		n.errText = ""
		n.sendResultBlock()
		return
	}

	n.isString = value.String
	n.hidden = value.Hidden
	if value.String {
		n.value = string(value.Data)
	} else {
		n.value = base64.StdEncoding.EncodeToString(value.Data)
	}
	n.status = "read"
	n.errText = ""
	n.log("read")
	n.sendEntryBlock()
	n.sendResultBlock()
}

func (n *debugKVNode) set() {
	store, err := n.store()
	if err != nil {
		n.setError("set failed", err)
		return
	}
	value, err := n.storageValue()
	if err != nil {
		n.setError("set failed", err)
		return
	}

	updated := false
	err = store.Update(n.Context(), func(tx storage.KVWriteTx) error {
		_, found, getErr := tx.Get(n.key)
		if getErr != nil {
			return getErr
		}
		updated = found
		return tx.Set(n.key, value)
	})
	if err != nil {
		n.setError("set failed", err)
		return
	}
	if updated {
		n.status = "updated"
	} else {
		n.status = "created"
	}
	n.errText = ""
	n.log(n.status)
	n.sendResultBlock()
}

func (n *debugKVNode) delete() {
	store, err := n.store()
	if err != nil {
		n.setError("delete failed", err)
		return
	}

	deleted := false
	err = store.Update(n.Context(), func(tx storage.KVWriteTx) error {
		_, found, getErr := tx.Get(n.key)
		if getErr != nil || !found {
			return getErr
		}
		deleted = true
		return tx.Delete(n.key)
	})
	if err != nil {
		n.setError("delete failed", err)
		return
	}
	if deleted {
		n.status = "deleted"
		n.log("deleted")
	} else {
		n.status = "not found"
	}
	n.errText = ""
	n.sendResultBlock()
}

func (n *debugKVNode) store() (storage.KVStore, error) {
	if n.manager == nil {
		return nil, errDebugKVUnavailable
	}
	path, err := parseDebugKVPath(n.pathText)
	if err != nil {
		return nil, err
	}
	if n.key == "" {
		return nil, errors.New("key is required")
	}

	var store storage.KVStore
	switch n.storeKind {
	case debugKVStoreDefault:
		store = n.manager.DefaultStore()
	case debugKVStorePlugin:
		if n.plugin == "" {
			return nil, errors.New("plugin name is required for a plugin store")
		}
		store = n.manager.PluginStore(n.plugin)
	default:
		return nil, fmt.Errorf("unknown store kind %q", n.storeKind)
	}
	if store == nil {
		return nil, errDebugKVUnavailable
	}
	return store.Subtree(path...), nil
}

func (n *debugKVNode) storageValue() (storage.KValue, error) {
	data := []byte(n.value)
	if !n.isString {
		decoded, err := base64.StdEncoding.DecodeString(n.value)
		if err != nil {
			return storage.KValue{}, errors.New("value is not valid base64")
		}
		data = decoded
	}
	return storage.KValue{Data: data, Hidden: n.hidden, String: n.isString}, nil
}

func parseDebugKVPath(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("path must be a JSON array of strings")
	}
	var path []string
	if err := json.Unmarshal([]byte(text), &path); err != nil || path == nil {
		return nil, errors.New("path must be a JSON array of strings")
	}
	for _, name := range path {
		if name == "" {
			return nil, errors.New("path names must not be empty")
		}
	}
	return path, nil
}

func (n *debugKVNode) setError(status string, err error) {
	n.status = status
	n.errText = err.Error()
	if n.Logger != nil {
		n.Logger.Debugf("debug KV %s: %v", status, err)
	}
	n.sendResultBlock()
}

func (n *debugKVNode) log(operation string) {
	if n.Logger != nil {
		n.Logger.Debugf("debug KV %s store=%s plugin=%q path=%s key=%q", operation, n.storeKind, n.plugin, n.pathText, n.key)
	}
}

func (n *debugKVNode) sendMenuSnapshot(force bool) {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, force, n.entryBlock(), n.resultBlock())
}

func (n *debugKVNode) sendEntryBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.entryBlock())
}

func (n *debugKVNode) sendResultBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.resultBlock())
}

func (n *debugKVNode) entryBlock() formular.Block {
	unavailable := n.manager == nil
	return formular.Block{ID: debugKVEntryBlockID, Order: 10, Generation: 1, Items: []formular.Item{
		formular.RadioField(debugKVStoreField, "Store", n.storeKind, []formular.Choice{
			{Value: debugKVStoreDefault},
			{Value: debugKVStorePlugin},
		}),
		formular.TextField(debugKVPluginField, "Plugin name", n.plugin,
			formular.Validation,
			func(item *formular.Item) { item.Readonly = n.storeKind != debugKVStorePlugin },
		),
		formular.MultilineTextField(debugKVPathField, "Subtree path (JSON array)", n.pathText,
			formular.Validation,
			formular.Placeholder(`["nodes", "example"]`),
		),
		formular.TextField(debugKVKeyField, "Key", n.key, formular.Validation),
		formular.MultilineTextField(debugKVValueField, "Value", n.value,
			formular.Validation,
			func(item *formular.Item) { item.Secret = n.hidden },
		),
		formular.CheckboxField(debugKVStringField, "String value", n.isString),
		formular.CheckboxField(debugKVHiddenField, "Hidden value", n.hidden),
		{Type: formular.ItemButton, ID: debugKVReadButton, Label: "Read", Inactive: unavailable},
		{Type: formular.ItemButton, ID: debugKVSetButton, Label: "Set / update", Inactive: unavailable},
		{Type: formular.ItemButton, ID: debugKVDeleteButton, Label: "Delete", Inactive: unavailable},
	}}
}

func (n *debugKVNode) resultBlock() formular.Block {
	return formular.Block{ID: debugKVResultBlockID, Order: 20, Generation: 1, Items: []formular.Item{
		formular.PlainLabel("status", "Status: "+n.status),
		formular.ReadonlyTextField("error", "Error", n.errText),
	}}
}
