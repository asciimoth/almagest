// Package transport implements private, bounded isolation control channels.
// It is not used by the direct daemon. Bootstrap owns endpoint distribution and
// verifies the final child restrictions before it starts a session.
package transport

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// Decode rejects duplicate (including escaped duplicate), unknown and
// incorrectly cased fields, invalid UTF-8, excessive depth, and trailing data.
// The destination must be a concrete wire type, not an interface or raw map.
func Decode(data []byte, dst any) error {
	if len(data) == 0 || len(data) > protocol.MaxControlFrameBytes || !utf8.Valid(data) {
		return protocol.ErrLimitExceeded
	}
	if !validEscapes(data) {
		return protocol.ErrInvalidContract
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := checkValue(d, 0, reflect.TypeOf(dst)); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return protocol.ErrInvalidContract
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return protocol.ErrInvalidContract
	}
	return nil
}

func checkValue(d *json.Decoder, depth int, typ reflect.Type) error {
	t, err := d.Token()
	if err != nil {
		return protocol.ErrInvalidContract
	}
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	delim, ok := t.(json.Delim)
	if !ok {
		if typ != nil && typ.Kind() == reflect.Array {
			return protocol.ErrInvalidContract
		}
		return nil
	}
	if depth >= protocol.MaxControlDepth {
		return protocol.ErrLimitExceeded
	}
	if delim == '{' {
		seen := make(map[string]bool)
		for d.More() {
			if len(seen) >= protocol.MaxAddressOrRouteSetItems {
				return protocol.ErrLimitExceeded
			}
			key, err := d.Token()
			if err != nil {
				return protocol.ErrInvalidContract
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return protocol.ErrInvalidContract
			}
			seen[name] = true
			var child reflect.Type
			if typ != nil && typ.Kind() == reflect.Struct {
				child = fieldType(typ, name)
				if child == nil {
					return protocol.ErrInvalidContract
				}
			} else if typ != nil && typ.Kind() == reflect.Map {
				child = typ.Elem()
			}
			if err := checkValue(d, depth+1, child); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return protocol.ErrInvalidContract
		}
		return nil
	}
	if delim != '[' {
		return protocol.ErrInvalidContract
	}
	var child reflect.Type
	if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) && typ != reflect.TypeFor[json.RawMessage]() {
		child = typ.Elem()
	}
	items := 0
	for d.More() {
		items++
		if items > protocol.MaxAddressOrRouteSetItems {
			return protocol.ErrLimitExceeded
		}
		if err := checkValue(d, depth+1, child); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim(']') {
		return protocol.ErrInvalidContract
	}
	if typ != nil && typ.Kind() == reflect.Array && items != typ.Len() {
		return protocol.ErrInvalidContract
	}
	return nil
}

func fieldType(t reflect.Type, name string) reflect.Type {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			inner := f.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if inner.Kind() == reflect.Struct {
				if found := fieldType(inner, name); found != nil {
					return found
				}
			}
		}
		if tag == "" {
			tag = f.Name
		}
		if tag == name {
			return f.Type
		}
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogates with U+FFFD. Reject them
// before decoding, so distinct wire strings cannot collapse to one identity.
func validEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n < 0xd800 || n > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
