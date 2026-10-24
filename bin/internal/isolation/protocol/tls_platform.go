package protocol

// TLSPlatformRefreshRequest asks N for the next bounded part of a complete
// broker-owned platform TLS snapshot. It contains no host selector.
type TLSPlatformRefreshRequest struct {
	Meta            RequestMeta
	Source          uint8
	IncludeCRLs     bool
	IncludeDistrust bool
	MaxBytes        uint32
	MaxItems        uint32
	Transfer        uint64
	Offset          uint32
}

// TLSPlatformRefreshResult is one ordered part of a serialized snapshot.
// Transfer is selected by N on the first request and repeated by R.
type TLSPlatformRefreshResult struct {
	Transfer uint64
	Offset   uint32
	Total    uint32
	Final    bool
	Data     []byte
}

// TLSRevocationCheckRequest contains only already-authorized certificate DER
// chains. It cannot select a store, URL, provider, or network mode.
type TLSRevocationCheckRequest struct {
	Meta   RequestMeta
	Chains [][][]byte
}

type TLSRevocationCheckResult struct {
	States []string
}
