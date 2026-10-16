//go:build !windows

package direct

func helperWindowsSecurityResult(string) string { return "unsupported" }
func helperWindowsHandleResult(string) string   { return "unsupported" }
