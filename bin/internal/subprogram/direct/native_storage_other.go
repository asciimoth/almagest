//go:build !linux

package direct

func prepareNativeStorageParent(string, uint32, uint32) error { return nil }
func prepareNativeStorage(string, uint32, uint32) error       { return nil }
