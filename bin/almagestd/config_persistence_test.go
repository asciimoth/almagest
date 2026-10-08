package main

import (
	"bytes"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localconfig"
)

type legacyConfigTestWriter interface {
	WriteConfig([]byte) error
}

func newTestConfigPersistence(t *testing.T, config *Config, writer legacyConfigTestWriter) daemonservices.ConfigPersistence {
	t.Helper()
	data, err := config.MainToBytes()
	if err != nil {
		t.Fatal(err)
	}
	current := bytes.Clone(data)
	return localconfig.New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func(next []byte) error {
			if err := writer.WriteConfig(next); err != nil {
				return err
			}
			current = bytes.Clone(next)
			return nil
		},
	)
}
