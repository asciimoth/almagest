package tlsnodes

import (
	"testing"

	"github.com/asciimoth/formular"
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

func findTLSMenuItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
}
