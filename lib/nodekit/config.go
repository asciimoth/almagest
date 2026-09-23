package nodekit

import (
	"slices"

	"github.com/asciimoth/configer/configer"
)

// SetConfigMap stores single-segment config fields in deterministic key order.
// Node save methods commonly delete their owned fields before calling this
// helper, so stable insertion order is required for stable HuJSON snapshots.
func SetConfigMap(cfg configer.Config, values map[string]any) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	fields := make([]configer.Field, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, configer.Field{
			Path:  configer.Path{key},
			Value: values[key],
		})
	}
	return configer.SetFields(cfg, fields...)
}
