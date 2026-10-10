//go:build windows

package supervision

// ReplaceFileW and MoveFileExW use write-through in durablefile. Windows does
// not provide the Unix directory fsync operation.
func syncDirectory(string) error { return nil }
