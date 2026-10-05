// Command windows-qualify validates one complete Windows qualification record.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/internal/windowssupport"
)

func main() {
	entry := flag.String("entry", "", "support-matrix platform ID")
	evidencePath := flag.String("evidence", "", "qualification evidence JSON file")
	flag.Parse()
	if *entry == "" || *evidencePath == "" || flag.NArg() != 0 {
		fail("usage: windows-qualify -entry ENTRY -evidence EVIDENCE")
	}

	matrix := readMatrix(filepath.Join("contracts", "windows", "support-matrix.json"))
	manifest := readManifest(filepath.Join("contracts", "windows", "test-manifest.json"))
	data, err := os.ReadFile(*evidencePath)
	if err != nil {
		fail("read evidence: %v", err)
	}
	evidence, err := windowssupport.DecodeQualificationEvidence(data)
	if err != nil {
		fail("%v", err)
	}
	if evidence.PlatformID != *entry {
		fail("evidence platform %q does not match requested entry %q", evidence.PlatformID, *entry)
	}
	if err := windowssupport.ValidateQualificationEvidence(matrix, manifest, evidence); err != nil {
		fail("qualification failed: %v", err)
	}
	fmt.Printf("Windows qualification evidence passed for %s\n", *entry)
}

func readMatrix(path string) windowssupport.SupportMatrix {
	data, err := os.ReadFile(path)
	if err != nil {
		fail("read support matrix: %v", err)
	}
	value, err := windowssupport.DecodeSupportMatrix(data)
	if err != nil {
		fail("%v", err)
	}
	return value
}

func readManifest(path string) windowssupport.TestManifest {
	data, err := os.ReadFile(path)
	if err != nil {
		fail("read test manifest: %v", err)
	}
	value, err := windowssupport.DecodeTestManifest(data)
	if err != nil {
		fail("%v", err)
	}
	return value
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "windows-qualify: "+format+"\n", arguments...)
	os.Exit(1)
}
