// Package certcat provides a generated certificate policy catalog.
//
// The catalog is not wired into almagestd yet. It is a data module for exact
// certificate and SPKI matches that need warnings, deny rules, or narrow scope.
package certcat

//go:generate go run ./cmd/certcat-generate -manual data/manual.json -machine data/machine.json -out catalog_gen.go
