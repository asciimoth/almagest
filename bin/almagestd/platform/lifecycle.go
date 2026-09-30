package platform

import "context"

// LifecycleRun is the daemon body used by console and service hosts. The host
// cancels ctx for every supported stop request and waits for the body to return.
// The body calls ready once startup is complete; duplicate calls are safe.
type LifecycleRun func(context.Context, func()) error
