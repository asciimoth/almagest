// Package daemonargs defines the public daemon command-line contract without
// importing daemon runtime or node code. Trusted isolated bootstrap and the
// direct daemon parse the same structure.
package daemonargs

type Args struct {
	// If StdInConfig is true, config is loaded from stdin. Protected config is
	// loaded from its top-level Protected key when present.
	StdInConfig     bool   `arg:"--stdin" help:"read config from stdin"`
	Config          string `arg:"-c,--config,env:ALMAGEST_CONF" help:"config path"`
	ProtectedConfig string `arg:"-p,--protected,env:ALMAGEST_PROTECTED_CONF" help:"protected config path"`
	UnsafeConfig    bool   `arg:"--unsafe-config" help:"do not check regular config file permissions"`
	Snapshot        string `arg:"--snapshot,env:ALMAGEST_SNAPSHOT" help:"load startup state from snapshot archive path; use - for stdin"`

	FileStorage    string `arg:"--file-storage,env:ALMAGEST_FILE_STORAGE" help:"file storage directory"`
	TmpFileStorage bool   `arg:"--tmp-file-storage,env:ALMAGEST_TMP_FILE_STORAGE" help:"use temporary file storage and remove it on exit"`

	LogLevel   string `arg:"-l,--lvl,env:ALMAGEST_LOG_LVL" help:"logging level debug|info|warn|error|fatal"`
	LogWorkers bool   `arg:"--log-workers" help:"log engine worker lifecycle events"`
	NoPool     bool   `arg:"--no-pool" help:"disable daemon global buffer pool"`

	NoDNSCacheExpiration   bool `arg:"--no-dns-cache-expiration,env:ALMAGEST_NO_DNS_CACHE_EXPIRATION" help:"disable DNS cache entry expiration"`
	DiscoverSystemPrograms bool `arg:"--discover-system-programs,env:ALMAGEST_DISCOVER_SYSTEM_PROGRAMS" help:"discover supported sandbox programs from the host PATH"`

	LogCacheDebug *int `arg:"--log-cache-debug,env:ALMAGEST_LOG_CACHE_DEBUG" help:"daemon debug log cache size"`
	LogCacheInfo  *int `arg:"--log-cache-info,env:ALMAGEST_LOG_CACHE_INFO" help:"daemon info log cache size"`
	LogCacheWarn  *int `arg:"--log-cache-warn,env:ALMAGEST_LOG_CACHE_WARN" help:"daemon warn log cache size"`
	LogCacheError *int `arg:"--log-cache-error,env:ALMAGEST_LOG_CACHE_ERROR" help:"daemon error log cache size"`
	LogCacheFatal *int `arg:"--log-cache-fatal,env:ALMAGEST_LOG_CACHE_FATAL" help:"daemon fatal log cache size"`

	WebAdmin                 *uint16  `arg:"-w,--web,env:ALMAGEST_WEB_ADMIN" help:"port to run local web admin panel on"`
	WebAdminFilterUIDs       []uint32 `arg:"--web-filter-uid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_UIDS" help:"allowed web admin peer owner UID; repeat flag or comma-separate env values"`
	WebAdminFilterGIDs       []uint32 `arg:"--web-filter-gid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_GIDS" help:"allowed web admin peer owner GID; repeat flag or comma-separate env values"`
	WebAdminFilterUsernames  []string `arg:"--web-filter-user,separate,env:ALMAGEST_WEB_ADMIN_FILTER_USERS" help:"allowed web admin peer owner username; repeat flag or comma-separate env values"`
	WebAdminPProf            bool     `arg:"--web-pprof,env:ALMAGEST_WEB_ADMIN_PPROF" help:"enable net/http/pprof handlers on the web admin listener under /debug/pprof/"`
	WebAdminAllowCrossOrigin bool     `arg:"--web-allow-cross-origin,env:ALMAGEST_WEB_ADMIN_ALLOW_CROSS_ORIGIN" help:"allow any browser origin to use or embed the web admin; unsafe"`

	UnixAdmin                *string  `arg:"--unix,env:ALMAGEST_UNIX_ADMIN" help:"unix socket path for local admin API; empty disables it"`
	TmpUnixAdmin             bool     `arg:"--tmp-unix-admin,env:ALMAGEST_TMP_UNIX_ADMIN" help:"create the local admin API unix socket at a random temporary alm-sock-* path"`
	UnixAdminFilterUIDs      []uint32 `arg:"--unix-filter-uid,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_UIDS" help:"allowed unix admin peer UID; repeat flag or comma-separate env values"`
	UnixAdminFilterGIDs      []uint32 `arg:"--unix-filter-gid,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_GIDS" help:"allowed unix admin peer GID; repeat flag or comma-separate env values"`
	UnixAdminFilterUsernames []string `arg:"--unix-filter-user,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_USERS" help:"allowed unix admin peer username; repeat flag or comma-separate env values"`
	PipeAdmin                *string  `arg:"--pipe,env:ALMAGEST_PIPE_ADMIN" help:"Windows local admin named-pipe path; empty disables it"`

	DebugNodes         bool   `arg:"--debug-nodes" help:"enable debug node classes"`
	Experimental       bool   `arg:"--experimental" help:"enable experimental features and node classes; requires direct mode"`
	DumpWebdemoClasses bool   `arg:"--dump-webdemo-node-classes" help:"write the web demo node preview cache to stdout and exit"`
	NoIsolation        bool   `arg:"--no-isolation" help:"select direct mode; accepted as a no-op where direct mode is the platform default"`
	Isolation          string `arg:"--isolation,env:ALMAGEST_ISOLATION" help:"isolation policy strict|auto|off"`
	IsolationStateDir  string `arg:"--isolation-state-dir" help:"instance state directory for non-root automatic isolation"`

	IsolationMemoryBytes *uint64 `arg:"--isolation-memory-bytes,env:ALMAGEST_ISOLATION_MEMORY_BYTES" help:"Linux isolation memory bytes per role (default 8589934592; range 33554432..1099511627776)"`
	IsolationTasks       *uint64 `arg:"--isolation-tasks,env:ALMAGEST_ISOLATION_TASKS" help:"Linux isolation tasks per role, including threads (default 4096; range 8..4096)"`
	IsolationCPUQuota    *uint64 `arg:"--isolation-cpu-quota,env:ALMAGEST_ISOLATION_CPU_QUOTA" help:"Linux isolation CPU microseconds per 100000 microseconds per role (default 6400000; range 1000..6400000)"`
	IsolationDescriptors *uint64 `arg:"--isolation-descriptors,env:ALMAGEST_ISOLATION_DESCRIPTORS" help:"Linux isolation file descriptors per process (default 65536; range 32..65536)"`
	IsolationFileBytes   *uint64 `arg:"--isolation-file-bytes,env:ALMAGEST_ISOLATION_FILE_BYTES" help:"Linux isolation maximum file size in bytes (default 1099511627776; range 1048576..1099511627776)"`
}
