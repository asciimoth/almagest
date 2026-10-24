package runtime

import (
	"context"
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

// TLSPlatformSnapshot reads one complete snapshot through the closed chunked
// broker operation. A failed or malformed transfer is never returned partly.
func (c *Client) TLSPlatformSnapshot(ctx context.Context, request tlsplatform.Request) (tlsplatform.Snapshot, error) {
	if c == nil || c.peer == nil || request.Validate() != nil {
		return tlsplatform.Snapshot{}, protocol.ErrInvalidContract
	}
	data := make([]byte, 0)
	var transfer uint64
	for {
		query := protocol.TLSPlatformRefreshRequest{Meta: c.meta(false), Source: uint8(request.Source),
			IncludeCRLs: request.IncludeCRLs, IncludeDistrust: request.IncludeDistrust,
			MaxBytes: request.MaxBytes, MaxItems: request.MaxItems, Transfer: transfer, Offset: uint32(len(data))}
		var result protocol.TLSPlatformRefreshResult
		if err := c.peer.Call(ctx, protocol.OperationTLSPlatformRefresh, query, &result); err != nil {
			return tlsplatform.Snapshot{}, err
		}
		if result.Transfer == 0 || transfer != 0 && result.Transfer != transfer || result.Offset != uint32(len(data)) ||
			result.Total == 0 || result.Total > request.MaxBytes || uint64(len(data))+uint64(len(result.Data)) > uint64(result.Total) ||
			result.Final != (uint64(len(data))+uint64(len(result.Data)) == uint64(result.Total)) || (!result.Final && len(result.Data) == 0) {
			return tlsplatform.Snapshot{}, fmt.Errorf("%w: invalid TLS platform transfer", protocol.ErrInvalidContract)
		}
		transfer = result.Transfer
		data = append(data, result.Data...)
		if result.Final {
			return tlsplatform.Unmarshal(data, request)
		}
	}
}
