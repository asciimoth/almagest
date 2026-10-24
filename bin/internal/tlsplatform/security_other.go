//go:build !linux && !windows

package tlsplatform

import "context"

func systemSecuritySnapshot(ctx context.Context, request Request) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	state := ProviderUnsupported
	if !request.IncludeDistrust && !request.IncludeCRLs {
		state = ProviderDisabled
	}
	return Snapshot{Providers: []ProviderStatus{{Source: "system:distrust", State: state}}}, nil
}
