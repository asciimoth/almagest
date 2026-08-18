package tlsnodes

import (
	"errors"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestTLSNodeConfigFieldsHaveExamplePlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		block formular.Block
		want  map[string]string
	}{
		{
			name:  "wrapper",
			block: newTLSWrapperNode(nil, nil).configBlock(),
			want: map[string]string{
				fieldClientServerName: placeholderClientServerName,
				fieldClientALPN:       placeholderALPN,
				fieldServerName:       placeholderServerName,
				fieldServerALPN:       placeholderALPN,
				fieldLeafTTLSeconds:   placeholderLeafTTLSeconds,
			},
		},
		{
			name:  "mitm",
			block: newTLSMITMNode(nil, nil).configBlock(),
			want: map[string]string{
				fieldSniffBufferSize: placeholderSniffBufferBytes,
				fieldLeafTTLSeconds:  placeholderLeafTTLSeconds,
				fieldFilterMode:      placeholderFilterMode,
				fieldFilterRules:     placeholderFilterRules,
			},
		},
		{
			name:  "terminator",
			block: newTLSTerminatorNode(nil, nil).configBlock(),
			want: map[string]string{
				fieldSniffBufferSize:   placeholderSniffBufferBytes,
				fieldLeafTTLSeconds:    placeholderLeafTTLSeconds,
				fieldServerALPN:        placeholderALPN,
				fieldDestinationRemaps: placeholderDestinationRemaps,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for field, want := range tc.want {
				item, ok := findTLSMenuItem(tc.block, field)
				if !ok {
					t.Fatalf("field %q not found", field)
				}
				if item.Field == nil {
					t.Fatalf("field %q has no field data", field)
				}
				if item.Placeholder != want {
					t.Fatalf("field %q placeholder = %q, want %q", field, item.Placeholder, want)
				}
			}
		})
	}
}

func TestReportWrappedNetworkErrorAddsDeduplicatedPopup(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(TLSWrapperClass{}); err != nil {
		t.Fatalf("AddNodeClass TLS wrapper: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeTLSWrapper, "wrapper")
	if err != nil {
		t.Fatalf("AddNodeByClass TLS wrapper: %v", err)
	}

	w.Lock()
	reportWrappedNetworkError(w, node, errors.New("wrap failed"))
	reportWrappedNetworkError(w, node, errors.New("wrap failed"))
	snapshot, ok := w.NodeSnapshotLocked(node)
	w.Unlock()
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if len(snapshot.Popups) != 1 {
		t.Fatalf("popups = %#v, want one deduplicated error", snapshot.Popups)
	}
	if snapshot.Popups[0].Type != pasta.NodePopupErr || snapshot.Popups[0].Text != "wrap failed" {
		t.Fatalf("popup = %#v, want wrap failed error", snapshot.Popups[0])
	}
}

func findTLSMenuItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
}
