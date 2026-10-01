package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/internal/windowssupport"
)

func main() {
	manifestPath := flag.String("manifest", "", "execution manifest path")
	suiteID := flag.String("suite", "", "suite identifier")
	identityPath := flag.String("identity", "", "identity evidence path")
	processPath := flag.String("processes", "", "process evidence path")
	eventDirectory := flag.String("events", "", "Go test event directory")
	outputPath := flag.String("out", "", "suite evidence output path")
	flag.Parse()
	if *manifestPath == "" || *suiteID == "" || *identityPath == "" || *processPath == "" || *eventDirectory == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "all arguments are required")
		os.Exit(2)
	}

	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		fail(err)
	}
	manifest, err := windowssupport.DecodeExecutionManifest(manifestData)
	if err != nil {
		fail(err)
	}
	var identity windowssupport.ExecutionIdentity
	if err := decodeFile(*identityPath, &identity); err != nil {
		fail(err)
	}
	var processes windowssupport.ProcessEvidence
	if err := decodeFile(*processPath, &processes); err != nil {
		fail(err)
	}
	evidence, validationErr := windowssupport.ValidateExecutionSuite(manifest, *suiteID, identity, processes, func(name string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(*eventDirectory, name))
	})
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err == nil {
		err = os.WriteFile(*outputPath, append(data, '\n'), 0o600)
	}
	if err != nil {
		fail(err)
	}
	if validationErr != nil {
		fail(validationErr)
	}
}

func decodeFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Windows PowerShell 5 writes a UTF-8 byte-order mark when Set-Content
	// uses -Encoding utf8. Accept that native evidence format.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
