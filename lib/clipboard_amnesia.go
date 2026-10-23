//go:build !wasm

package almagest

import amnesianodes "github.com/asciimoth/almagest/lib/amnesia"

func sanitizeAmnesiaClipboard(data string) string {
	return amnesianodes.SanitizeClipboard(data)
}
