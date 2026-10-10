//go:build !windows && (!linux || !amd64)

package roleentry

func SealConfigWriter() error { return ErrStandaloneRole }
