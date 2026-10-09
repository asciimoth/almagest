package api

type LogMethod string

const (
	MethodLogSubscribe   LogMethod = "subscribe"
	MethodLogUnsubscribe LogMethod = "unsubscribe"

	EventLogEntry LogMethod = "entry"
)

func LogMethods() []string {
	return []string{
		string(MethodLogSubscribe),
		string(MethodLogUnsubscribe),
	}
}

type LogEntry struct {
	At                 string `json:"at"`
	Level              string `json:"level"`
	Source             string `json:"source"`
	Message            string `json:"message"`
	NodeID             uint64 `json:"node_id,omitempty"`
	NodeClass          string `json:"node_class,omitempty"`
	Role               string `json:"role,omitempty"`
	LaunchID           string `json:"launch_id,omitempty"`
	ProcessInstance    string `json:"process_instance,omitempty"`
	Sequence           uint64 `json:"sequence,omitempty"`
	DroppedBefore      uint64 `json:"dropped_before,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	ResourceID         string `json:"resource_id,omitempty"`
	ResourceGeneration uint64 `json:"resource_generation,omitempty"`
	OperationID        string `json:"operation_id,omitempty"`
	Operation          string `json:"operation,omitempty"`
	Decision           string `json:"decision,omitempty"`
	Outcome            string `json:"outcome,omitempty"`
}

type SubscribeLogsResponse struct {
	SubscriptionID uint64     `json:"subscription_id"`
	CacheSize      int        `json:"cache_size"`
	Entries        []LogEntry `json:"entries"`
}
