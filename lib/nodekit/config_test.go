package nodekit

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/asciimoth/configer/configer"
)

func TestSetConfigMapUsesDeterministicKeyOrder(t *testing.T) {
	for iteration := range 100 {
		cfg := &recordingConfig{}
		err := SetConfigMap(cfg, map[string]any{
			"z-last":  3,
			"a-first": 1,
			"m-mid":   2,
		})
		if err != nil {
			t.Fatalf("iteration %d: SetConfigMap: %v", iteration, err)
		}
		want := []string{"a-first", "m-mid", "z-last"}
		if !slices.Equal(cfg.setKeys, want) {
			t.Fatalf("iteration %d: set keys = %v, want %v", iteration, cfg.setKeys, want)
		}
	}
}

func TestSetConfigMapCopiesValuesToMatchingKeys(t *testing.T) {
	cfg := &recordingConfig{}
	values := map[string]any{
		"number": 7,
		"text":   "value",
		"nested": map[string]any{"enabled": true},
	}
	if err := SetConfigMap(cfg, values); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.setValues, values) {
		t.Fatalf("set values = %#v, want %#v", cfg.setValues, values)
	}
}

func TestSetConfigMapRejectsNilConfig(t *testing.T) {
	if err := SetConfigMap(nil, map[string]any{"key": "value"}); !errors.Is(err, configer.ErrNilConfig) {
		t.Fatalf("error = %v, want %v", err, configer.ErrNilConfig)
	}
}

type recordingConfig struct {
	*configer.Memory
	setKeys   []string
	setValues map[string]any
}

func (c *recordingConfig) Set(path configer.Path, value any) error {
	c.setKeys = append(c.setKeys, path[0])
	if c.setValues == nil {
		c.setValues = make(map[string]any)
	}
	c.setValues[path[0]] = value
	return nil
}
