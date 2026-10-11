//go:build !windows && (!linux || !amd64)

package roleentry

func SealConfigWriter() error  { return ErrStandaloneRole }
func SealCommandHelper() error { return ErrStandaloneRole }
func SealRuntime() error       { return ErrStandaloneRole }
