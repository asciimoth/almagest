package args

type Args struct {
	// If StdInConfig==true, config should be loaded from stdin. Protected
	// config should be loaded from the stdin config's top-level Protected key
	// when present, otherwise protected defaults should be used.
	// Else Config and ProtectedConfig path should be used to load HuJSON configs.
	// If Config is a directory, it should be treated as "{Config}/alm.hujson".
	// If Config is not set, platform defaults should be used.
	// If ProtectedConfig is not set, it should be treated as a "protected_alm.hujson"
	// in same dir where main config placed.
	// If any of config files is not present, defaults should be used.
	StdInConfig     bool   `arg:"--stdin" help:"read config from stdin"`
	Config          string `arg:"-c,--config,env:ALMAGEST_CONF" help:"config path"`
	ProtectedConfig string `arg:"-p,--protected,env:ALMAGEST_PROTECTED_CONF" help:"protected config path"`
	UnsafeConfig    bool   `arg:"--unsafe-config" help:"do not check config file permissions"`
	// TODO: Read Only Config toggle

	LogLevel string `arg:"-l,--lvl,env:ALMAGEST_LOG_LVL" help:"logging level debug|info|warn|error|fatal"`

	WebAdmin                *uint16  `arg:"-w,--web,env:ALMAGEST_WEB_ADMIN" help:"port to run local web admin panel on"`
	WebAdminFilterUIDs      []uint32 `arg:"--web-filter-uid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_UIDS" help:"allowed web admin peer owner UID; repeat flag or comma-separate env values"`
	WebAdminFilterGIDs      []uint32 `arg:"--web-filter-gid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_GIDS" help:"allowed web admin peer owner GID; repeat flag or comma-separate env values"`
	WebAdminFilterUsernames []string `arg:"--web-filter-user,separate,env:ALMAGEST_WEB_ADMIN_FILTER_USERS" help:"allowed web admin peer owner username; repeat flag or comma-separate env values"`

	UnixAdmin                *string  `arg:"--unix,env:ALMAGEST_UNIX_ADMIN" help:"unix socket path for local admin API; empty disables it"`
	UnixAdminFilterUIDs      []uint32 `arg:"--unix-filter-uid,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_UIDS" help:"allowed unix admin peer UID; repeat flag or comma-separate env values"`
	UnixAdminFilterGIDs      []uint32 `arg:"--unix-filter-gid,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_GIDS" help:"allowed unix admin peer GID; repeat flag or comma-separate env values"`
	UnixAdminFilterUsernames []string `arg:"--unix-filter-user,separate,env:ALMAGEST_UNIX_ADMIN_FILTER_USERS" help:"allowed unix admin peer username; repeat flag or comma-separate env values"`

	DebugNodes bool `arg:"--debug-nodes" help:"enable debug node classes"`
}
