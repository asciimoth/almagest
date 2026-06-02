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
	At        string `json:"at"`
	Level     string `json:"level"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	NodeID    uint64 `json:"node_id,omitempty"`
	NodeClass string `json:"node_class,omitempty"`
}

type SubscribeLogsResponse struct {
	SubscriptionID uint64     `json:"subscription_id"`
	CacheSize      int        `json:"cache_size"`
	Entries        []LogEntry `json:"entries"`
}
