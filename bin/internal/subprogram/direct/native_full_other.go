//go:build !linux || !amd64

package direct

import (
	"os"

	"github.com/asciimoth/almagest/lib/sandbox"
)

type nativeInstanceBoundary struct{}

func (b *nativeInstanceBoundary) Child(string) (*nativeInstanceBoundary, error) {
	return nil, sandbox.ErrUnavailable
}

func fullNativePrerequisites(string, string) error             { return sandbox.ErrUnavailable }
func probeFullNativeLauncher(string, os.FileInfo) error        { return sandbox.ErrUnavailable }
func (b *nativeInstanceBoundary) ProcsFile() (*os.File, error) { return nil, sandbox.ErrUnavailable }
func (b *nativeInstanceBoundary) Close() error                 { return nil }
