//go:build !linux

package direct

func helperNamespaceResult() string { return "unsupported" }
