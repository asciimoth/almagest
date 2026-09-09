//go:build !linux

package links

func socketPathMTU(any, bool) (int, error) { return 0, nil }
