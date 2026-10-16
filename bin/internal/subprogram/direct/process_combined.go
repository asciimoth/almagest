package direct

import "errors"

type commandProcessBoundary struct {
	processBoundary
	cgroup *nativeInstanceBoundary
}

func (b *commandProcessBoundary) Kill() error {
	return errors.Join(b.processBoundary.Kill(), b.cgroup.Close())
}

func (b *commandProcessBoundary) Close() error {
	return errors.Join(b.processBoundary.Close(), b.cgroup.Close())
}
