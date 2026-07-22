package api

type SecretsMethod string

const (
	MethodSecretsSnapshot    SecretsMethod = "snapshot"
	MethodSecretsSubscribe   SecretsMethod = "subscribe"
	MethodSecretsUnsubscribe SecretsMethod = "unsubscribe"
	MethodSecretsSetValue    SecretsMethod = "set_value"
	MethodSecretsSetNodes    SecretsMethod = "set_nodes"
	MethodSecretsRename      SecretsMethod = "rename"
	MethodSecretsDelete      SecretsMethod = "delete"

	EventSecretsUpdate SecretsMethod = "update"
)

// SecretsMethods returns request methods for the secrets admin surface.
func SecretsMethods() []string {
	return []string{
		string(MethodSecretsSnapshot),
		string(MethodSecretsSubscribe),
		string(MethodSecretsUnsubscribe),
		string(MethodSecretsSetValue),
		string(MethodSecretsSetNodes),
		string(MethodSecretsRename),
		string(MethodSecretsDelete),
	}
}

func IsSecretsMutation(method string) bool {
	switch SecretsMethod(method) {
	case MethodSecretsSetValue,
		MethodSecretsSetNodes,
		MethodSecretsRename,
		MethodSecretsDelete:
		return true
	default:
		return false
	}
}

type SecretSource string

const (
	SecretSourceMutable   SecretSource = "mutable"
	SecretSourceProtected SecretSource = "protected"
	SecretSourceEnv       SecretSource = "env"
)

// SecretEntry is the effective secret visible to admin clients. When mutable
// and immutable secrets share a key, the immutable entry is reported here.
type SecretEntry struct {
	Key      string       `json:"key"`
	Value    string       `json:"value"`
	Nodes    []string     `json:"nodes,omitempty"`
	ReadOnly bool         `json:"read_only,omitempty"`
	Source   SecretSource `json:"source,omitempty"`
}

// SecretsSnapshot lists all effective secrets, including their values and
// current node allow lists.
type SecretsSnapshot struct {
	Secrets []SecretEntry `json:"secrets,omitempty"`
}

// SecretsSubscribeResponse returns the initial snapshot for a live list
// subscription. Later updates are incremental SecretsEvent messages.
type SecretsSubscribeResponse struct {
	SubscriptionID uint64          `json:"subscription_id"`
	Snapshot       SecretsSnapshot `json:"snapshot"`
}

// SecretChange carries one effective secret upsert or deletion.
type SecretChange struct {
	Key     string       `json:"key"`
	Deleted bool         `json:"deleted,omitempty"`
	Entry   *SecretEntry `json:"entry,omitempty"`
}

// SecretsEvent reports one list change for a subscription.
type SecretsEvent struct {
	SubscriptionID uint64       `json:"subscription_id"`
	Change         SecretChange `json:"change"`
}

// SecretKeyRequest identifies one secret by key.
type SecretKeyRequest struct {
	Key string `json:"key"`
}

// SecretsSetValueRequest creates or updates a mutable secret value.
type SecretsSetValueRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SecretsSetNodesRequest replaces a mutable secret's node allow list.
type SecretsSetNodesRequest struct {
	Key   string   `json:"key"`
	Nodes []string `json:"nodes,omitempty"`
}

// SecretsRenameRequest renames one mutable secret key.
type SecretsRenameRequest struct {
	Key    string `json:"key"`
	NewKey string `json:"new_key"`
}

// SecretsSubscriptionRequest identifies a live secrets subscription.
type SecretsSubscriptionRequest struct {
	SubscriptionID uint64 `json:"subscription_id"`
}
