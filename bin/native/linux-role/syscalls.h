/* Reviewed Linux amd64 / static Go 1.26 allowlist. Default deny includes x32,
 * sockets in R/C/H, io_uring, BPF, namespace entry, ptrace and device ioctls. */
static void allow_call(scmp_filter_ctx filter, int call) {
	if (seccomp_rule_add(filter, SCMP_ACT_ALLOW, call, 0) < 0) fail("seccomp allow rule");
}

static void apply_seccomp(const struct plan *p) {
	scmp_filter_ctx f = seccomp_init(SCMP_ACT_ERRNO(EPERM));
	if (!f) fail("create seccomp filter");
	if (seccomp_attr_set(f, SCMP_FLTATR_ACT_BADARCH, SCMP_ACT_KILL_PROCESS) < 0) fail("deny alternate syscall ABIs");
	const int allowed[] = {
		SCMP_SYS(read), SCMP_SYS(write), SCMP_SYS(readv), SCMP_SYS(writev),
		SCMP_SYS(pread64), SCMP_SYS(pwrite64), SCMP_SYS(close), SCMP_SYS(lseek),
		SCMP_SYS(dup), SCMP_SYS(dup2), SCMP_SYS(dup3),
		SCMP_SYS(mmap), SCMP_SYS(mprotect), SCMP_SYS(munmap), SCMP_SYS(mremap),
		SCMP_SYS(madvise), SCMP_SYS(brk), SCMP_SYS(rt_sigaction), SCMP_SYS(rt_sigprocmask),
		SCMP_SYS(rt_sigreturn), SCMP_SYS(sigaltstack), SCMP_SYS(futex), SCMP_SYS(sched_yield),
		SCMP_SYS(sched_getaffinity), SCMP_SYS(nanosleep), SCMP_SYS(clock_nanosleep),
		SCMP_SYS(clock_gettime), SCMP_SYS(gettimeofday), SCMP_SYS(epoll_create1),
		SCMP_SYS(epoll_ctl), SCMP_SYS(epoll_pwait), SCMP_SYS(epoll_wait), SCMP_SYS(eventfd2),
		SCMP_SYS(pipe2), SCMP_SYS(openat), SCMP_SYS(openat2), SCMP_SYS(newfstatat),
		SCMP_SYS(fstat), SCMP_SYS(statx), SCMP_SYS(getdents64), SCMP_SYS(readlinkat),
		SCMP_SYS(mkdirat), SCMP_SYS(unlinkat), SCMP_SYS(renameat), SCMP_SYS(renameat2),
		SCMP_SYS(symlinkat), SCMP_SYS(fsync), SCMP_SYS(fdatasync), SCMP_SYS(flock),
		SCMP_SYS(ftruncate), SCMP_SYS(fallocate), SCMP_SYS(umask),
		SCMP_SYS(fchmod), SCMP_SYS(fchown),
		SCMP_SYS(getrandom), SCMP_SYS(getrlimit), SCMP_SYS(rseq), SCMP_SYS(getrusage),
		SCMP_SYS(poll), SCMP_SYS(ppoll), SCMP_SYS(set_robust_list), SCMP_SYS(arch_prctl),
		SCMP_SYS(uname), SCMP_SYS(getpid), SCMP_SYS(getppid), SCMP_SYS(gettid),
		SCMP_SYS(getuid), SCMP_SYS(geteuid), SCMP_SYS(getgid), SCMP_SYS(getegid),
		SCMP_SYS(getresuid), SCMP_SYS(getresgid), SCMP_SYS(getgroups), SCMP_SYS(capget),
		SCMP_SYS(sendto), SCMP_SYS(recvfrom), SCMP_SYS(sendmsg), SCMP_SYS(recvmsg),
		SCMP_SYS(getsockname), SCMP_SYS(getpeername), SCMP_SYS(getsockopt), SCMP_SYS(shutdown),
		SCMP_SYS(seccomp), SCMP_SYS(exit), SCMP_SYS(exit_group),
		SCMP_SYS(restart_syscall),
	};
	for (size_t i = 0; i < sizeof(allowed) / sizeof(allowed[0]); i++) allow_call(f, allowed[i]);
	if (p->user_profile) {
		const int fcntl_queries[] = {F_GETFD, F_GETFL, F_DUPFD_CLOEXEC};
		for (size_t i = 0; i < sizeof(fcntl_queries) / sizeof(fcntl_queries[0]); i++) {
			if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(fcntl), 1,
				SCMP_A1(SCMP_CMP_EQ, fcntl_queries[i])) < 0) fail("user fcntl query rule");
		}
		if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(fcntl), 2,
			SCMP_A1(SCMP_CMP_EQ, F_SETFD), SCMP_A2(SCMP_CMP_MASKED_EQ, ~((scmp_datum_t)FD_CLOEXEC), 0)) < 0 ||
			seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(fcntl), 2,
			SCMP_A1(SCMP_CMP_EQ, F_SETFL), SCMP_A2(SCMP_CMP_MASKED_EQ, O_ASYNC, 0)) < 0)
			fail("user fcntl mutation rule");
		if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(prlimit64), 1,
			SCMP_A0(SCMP_CMP_EQ, 0)) < 0 ||
			seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(tgkill), 1,
			SCMP_A0(SCMP_CMP_EQ, (scmp_datum_t)getpid())) < 0)
			fail("user self process rule");
		/* Snapshot publication links a temporary regular file inside the
		 * writable state tree. Landlock still denies references from code or
		 * any directory outside the role's writable domain. */
		allow_call(f, SCMP_SYS(linkat));
		allow_call(f, SCMP_SYS(execveat));
	} else {
		allow_call(f, SCMP_SYS(fcntl)); allow_call(f, SCMP_SYS(prlimit64));
		allow_call(f, SCMP_SYS(tgkill)); allow_call(f, SCMP_SYS(tkill));
		allow_call(f, SCMP_SYS(linkat)); allow_call(f, SCMP_SYS(truncate));
		allow_call(f, SCMP_SYS(fchmodat)); allow_call(f, SCMP_SYS(execve));
	}
	if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(clone), 1, SCMP_A0(SCMP_CMP_EQ, 0xd0f00)) < 0 ||
		seccomp_rule_add(f, SCMP_ACT_ERRNO(ENOSYS), SCMP_SYS(clone3), 0) < 0) fail("Go thread rules");
	const int queries[] = {PR_GET_NO_NEW_PRIVS, PR_GET_SECCOMP, PR_GET_SECUREBITS, PR_CAPBSET_READ, PR_GET_DUMPABLE};
	for (size_t i = 0; i < sizeof(queries) / sizeof(queries[0]); i++) {
		if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 1, SCMP_A0(SCMP_CMP_EQ, queries[i])) < 0) fail("read-only prctl rule");
	}
	if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 2,
		SCMP_A0(SCMP_CMP_EQ, PR_CAP_AMBIENT), SCMP_A1(SCMP_CMP_EQ, PR_CAP_AMBIENT_IS_SET)) < 0)
		fail("ambient capability query rule");
	if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 2,
		SCMP_A0(SCMP_CMP_EQ, PR_SET_DUMPABLE), SCMP_A1(SCMP_CMP_EQ, 0)) < 0) fail("disable dumpability rule");
	if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(setfsuid), 1,
		SCMP_A0(SCMP_CMP_EQ, (scmp_datum_t)-1)) < 0 ||
		seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(setfsgid), 1,
		SCMP_A0(SCMP_CMP_EQ, (scmp_datum_t)-1)) < 0)
		fail("filesystem credential query rule");
	if (p->role == COMMAND) {
		/* Go os/exec fallback: CLONE_VM|CLONE_VFORK|SIGCHLD. A fixed static
		 * command is the only additional executable in the filesystem. */
		if (seccomp_rule_add(f, SCMP_ACT_ALLOW, SCMP_SYS(clone), 1, SCMP_A0(SCMP_CMP_EQ, 0x4111)) < 0) fail("fixed command clone rule");
		const int calls[] = {SCMP_SYS(wait4), SCMP_SYS(waitid), SCMP_SYS(setpgid), SCMP_SYS(getpgid),
			SCMP_SYS(kill), SCMP_SYS(chdir), SCMP_SYS(close_range)};
		for (size_t i = 0; i < sizeof(calls) / sizeof(calls[0]); i++) allow_call(f, calls[i]);
	}
	if (seccomp_attr_set(f, SCMP_FLTATR_CTL_NNP, 0) < 0 || seccomp_load(f) < 0) fail("install seccomp allowlist");
	seccomp_release(f);
}
