/* Linux/amd64 subprogram launcher. Trusted daemon code supplies held file
 * descriptors and validated scalar arguments. This binary is never setuid and
 * never changes the daemon process itself. */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/landlock.h>
#include <linux/mount.h>
#include <linux/securebits.h>
#include <net/if.h>
#include <poll.h>
#include <sched.h>
#include <seccomp.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/ioctl.h>
#include <sys/mount.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/sysmacros.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#if !defined(__x86_64__) || defined(__ILP32__)
#error This launcher is qualified only for Linux amd64
#endif
#ifndef LANDLOCK_ACCESS_FS_REFER
#define LANDLOCK_ACCESS_FS_REFER (1ULL << 13)
#endif
#ifndef LANDLOCK_ACCESS_FS_TRUNCATE
#define LANDLOCK_ACCESS_FS_TRUNCATE (1ULL << 14)
#endif

static int status_fd = 4;
static const char *staging_root;

static void child_changed(int signal_number) { (void)signal_number; }

static void fail(const char *operation) {
	char message[256];
	int saved = errno;
	int n = snprintf(message, sizeof(message), "%s: %s", operation, strerror(saved));
	if (n > 0) {
		ssize_t ignored = write(status_fd, message, (size_t)n < sizeof(message) ? (size_t)n : sizeof(message));
		(void)ignored;
	}
	_exit(125);
}

static void require(bool condition, const char *operation) {
	if (!condition) { errno = EINVAL; fail(operation); }
}

static uint64_t number(const char *text) {
	require(text && text[0] && strlen(text) <= 20, "bounded decimal input");
	uint64_t result = 0;
	for (const char *p = text; *p; p++) {
		require(*p >= '0' && *p <= '9', "decimal input");
		unsigned digit = (unsigned)(*p - '0');
		require(result <= (UINT64_MAX - digit) / 10, "decimal overflow");
		result = result * 10 + digit;
	}
	return result;
}

static void set_limit(int resource, uint64_t value, const char *name) {
	struct rlimit limit = {.rlim_cur = (rlim_t)value, .rlim_max = (rlim_t)value};
	if (setrlimit(resource, &limit) == -1) fail(name);
}

static void inspect_fd(int fd, bool directory, bool executable) {
	struct stat st;
	if (fstat(fd, &st) == -1) fail("inspect inherited object");
	require(directory ? S_ISDIR(st.st_mode) : S_ISREG(st.st_mode), "inherited object type");
	/* Immutable stores can hard-link identical content. A second name is safe
	 * only when no user can change the file. */
	require(directory || st.st_nlink == 1 || (st.st_mode & 0222) == 0,
		"executable hard-link alias");
	if (executable) require((st.st_mode & 0111) != 0 && (st.st_mode & 0022) == 0, "trusted executable mode");
}

static void inspect_readonly_fd(int fd) {
	struct stat st;
	if (fstat(fd, &st) == -1) fail("inspect read-only source");
	require(S_ISDIR(st.st_mode) || (S_ISREG(st.st_mode) &&
		(st.st_nlink == 1 || (st.st_mode & 0222) == 0)),
		"read-only source type");
}

static void add_path_rule(int ruleset, int parent, uint64_t access) {
	struct landlock_path_beneath_attr rule = {.allowed_access = access, .parent_fd = parent};
	if (syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &rule, 0) == -1)
		fail("add Landlock path rule");
}

static void install_landlock(int writable_count, int executable_count, int readonly_count,
	const char *executable_readonly) {
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	require(abi >= 3, "Landlock ABI 3 required");
	uint64_t handled = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_WRITE_FILE |
		LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
		LANDLOCK_ACCESS_FS_MAKE_CHAR | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_FIFO | LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM | LANDLOCK_ACCESS_FS_REFER |
		LANDLOCK_ACCESS_FS_TRUNCATE;
	struct landlock_ruleset_attr rules = {.handled_access_fs = handled};
	int ruleset = (int)syscall(SYS_landlock_create_ruleset, &rules, sizeof(rules), 0);
	if (ruleset == -1) fail("create Landlock ruleset");
	uint64_t writable = LANDLOCK_ACCESS_FS_WRITE_FILE | LANDLOCK_ACCESS_FS_READ_FILE |
		LANDLOCK_ACCESS_FS_READ_DIR | LANDLOCK_ACCESS_FS_REMOVE_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_FILE | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM | LANDLOCK_ACCESS_FS_REFER |
		LANDLOCK_ACCESS_FS_TRUNCATE;
	for (int n = 0; n < writable_count; n++) add_path_rule(ruleset, 6 + n, writable);
	int first_executable = 6 + writable_count;
	for (int n = 0; n < executable_count; n++)
		add_path_rule(ruleset, first_executable + n, LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_READ_FILE);
	int first_readonly = first_executable + executable_count;
	for (int n = 0; n < readonly_count; n++) {
		struct stat st;
		if (fstat(first_readonly + n, &st) == -1) fail("inspect read-only source");
		uint64_t access = LANDLOCK_ACCESS_FS_READ_FILE;
		if (S_ISDIR(st.st_mode)) access |= LANDLOCK_ACCESS_FS_READ_DIR;
		if (executable_readonly[n] == '1') access |= LANDLOCK_ACCESS_FS_EXECUTE;
		add_path_rule(ruleset, first_readonly + n, access);
	}
	if (syscall(SYS_landlock_restrict_self, ruleset, 0) == -1) fail("apply Landlock ruleset");
	if (close(ruleset) == -1) fail("close Landlock ruleset");
}

static void deny_syscall(scmp_filter_ctx filter, int call) {
	if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), call, 0) < 0) fail("add seccomp rule");
}

static void install_seccomp(void) {
	scmp_filter_ctx filter = seccomp_init(SCMP_ACT_ALLOW);
	if (!filter) fail("create seccomp filter");
	if (seccomp_arch_add(filter, SCMP_ARCH_X32) < 0) fail("cover x32 syscall ABI");
	int calls[] = {SCMP_SYS(mount), SCMP_SYS(umount2), SCMP_SYS(pivot_root),
		SCMP_SYS(open_tree), SCMP_SYS(move_mount), SCMP_SYS(fsopen), SCMP_SYS(fsconfig),
		SCMP_SYS(fsmount), SCMP_SYS(fspick), SCMP_SYS(mount_setattr),
		SCMP_SYS(unshare), SCMP_SYS(setns), SCMP_SYS(ptrace),
		SCMP_SYS(process_vm_readv), SCMP_SYS(process_vm_writev), SCMP_SYS(bpf),
		SCMP_SYS(perf_event_open), SCMP_SYS(keyctl), SCMP_SYS(add_key),
		SCMP_SYS(request_key), SCMP_SYS(init_module), SCMP_SYS(finit_module),
		SCMP_SYS(delete_module), SCMP_SYS(reboot), SCMP_SYS(kexec_load),
		SCMP_SYS(open_by_handle_at), SCMP_SYS(name_to_handle_at),
		SCMP_SYS(userfaultfd), SCMP_SYS(pidfd_getfd), SCMP_SYS(pidfd_send_signal),
		SCMP_SYS(process_madvise), SCMP_SYS(kcmp), SCMP_SYS(io_uring_setup), SCMP_SYS(io_uring_enter),
		SCMP_SYS(io_uring_register)};
	for (size_t n = 0; n < sizeof(calls) / sizeof(calls[0]); n++) deny_syscall(filter, calls[n]);
	if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(ENOSYS), SCMP_SYS(clone3), 0) < 0)
		fail("disable clone3 with compatible fallback");
	uint64_t namespace_flags[] = {CLONE_NEWCGROUP, CLONE_NEWIPC, CLONE_NEWNET,
		CLONE_NEWNS, CLONE_NEWPID, CLONE_NEWTIME, CLONE_NEWUSER, CLONE_NEWUTS};
	for (size_t n = 0; n < sizeof(namespace_flags) / sizeof(namespace_flags[0]); n++) {
		if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(clone), 1,
			SCMP_A0(SCMP_CMP_MASKED_EQ, namespace_flags[n], namespace_flags[n])) < 0)
			fail("deny namespace clone");
	}
	/* The worker needs pathname/abstract Unix sockets and loopback IP only.
	 * Use an allowlist so a new kernel address family is not admitted by
	 * default. Network-namespace setup happens before this filter is loaded. */
	if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(EAFNOSUPPORT), SCMP_SYS(socket), 1,
		SCMP_A0(SCMP_CMP_GT, AF_INET6)) < 0)
		fail("restrict high socket families");
	int low_socket_families[] = {AF_UNSPEC, AF_AX25, AF_IPX, AF_APPLETALK,
		AF_NETROM, AF_BRIDGE, AF_ATMPVC, AF_X25};
	for (size_t n = 0; n < sizeof(low_socket_families) / sizeof(low_socket_families[0]); n++)
		if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(EAFNOSUPPORT), SCMP_SYS(socket), 1,
			SCMP_A0(SCMP_CMP_EQ, (scmp_datum_t)low_socket_families[n])) < 0)
			fail("restrict low socket family");
	/* PID 1 is the trusted namespace reaper and controller-existence target.
	 * Permit signal 0 probes, but do not let sandbox peers stop or alter it. */
	if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(kill), 2,
		SCMP_A0(SCMP_CMP_EQ, 1), SCMP_A1(SCMP_CMP_NE, 0)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(tkill), 2,
		SCMP_A0(SCMP_CMP_EQ, 1), SCMP_A1(SCMP_CMP_NE, 0)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(tgkill), 2,
		SCMP_A0(SCMP_CMP_EQ, 1), SCMP_A2(SCMP_CMP_NE, 0)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(rt_sigqueueinfo), 2,
		SCMP_A0(SCMP_CMP_EQ, 1), SCMP_A1(SCMP_CMP_NE, 0)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(rt_tgsigqueueinfo), 2,
		SCMP_A0(SCMP_CMP_EQ, 1), SCMP_A2(SCMP_CMP_NE, 0)) < 0)
		fail("protect namespace reaper signals");
	if (seccomp_attr_set(filter, SCMP_FLTATR_CTL_NNP, 0) < 0 || seccomp_load(filter) < 0)
		fail("apply seccomp filter");
	seccomp_release(filter);
}

static void remove_authority(uid_t uid, gid_t gid) {
	if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) == -1 && errno != EINVAL)
		fail("clear ambient capabilities");
	for (int capability = 0; capability < 64; capability++) {
		if (prctl(PR_CAPBSET_DROP, capability, 0, 0, 0) == -1 && errno != EINVAL && errno != EPERM)
			fail("clear capability bounding set");
	}
	if (geteuid() == 0) {
		unsigned securebits = SECBIT_NOROOT | SECBIT_NOROOT_LOCKED |
			SECBIT_NO_SETUID_FIXUP | SECBIT_NO_SETUID_FIXUP_LOCKED |
			SECBIT_KEEP_CAPS_LOCKED | SECBIT_NO_CAP_AMBIENT_RAISE |
			SECBIT_NO_CAP_AMBIENT_RAISE_LOCKED;
		if (prctl(PR_SET_SECUREBITS, securebits, 0, 0, 0) == -1) fail("lock securebits");
		if (setgroups(0, NULL) == -1 || setresgid(gid, gid, gid) == -1 || setresuid(uid, uid, uid) == -1)
			fail("select subprogram identity");
	} else {
		require(getuid() == uid && geteuid() == uid && getgid() == gid && getegid() == gid,
			"current-user identity mismatch");
	}
	struct __user_cap_header_struct header = {_LINUX_CAPABILITY_VERSION_3, 0};
	struct __user_cap_data_struct data[2] = {{0}, {0}};
	if (syscall(SYS_capset, &header, data) == -1) fail("clear capabilities");
}

extern char **environ;

static void enter_cgroup(int fd) {
	char value[32];
	int length = snprintf(value, sizeof(value), "%ld", (long)getpid());
	require(length > 0 && length < (int)sizeof(value), "format cgroup PID");
	if (write(fd, value, (size_t)length) != length) fail("enter delegated cgroup");
}

static bool safe_target(const char *path) {
	if (!path || path[0] != '/' || strlen(path) > 4096 || strstr(path, "//")) return false;
	for (const char *part = path; (part = strstr(part, "..")); part += 2) {
		if ((part == path || part[-1] == '/') && (part[2] == 0 || part[2] == '/')) return false;
	}
	return strcmp(path, "/") != 0;
}

static const char *relative_mount_target(const char *path) {
	require(path && path[0], "mount target");
	const char *relative = path[0] == '/' ? path + 1 : path;
	require(relative[0] && strlen(relative) <= 4096 && !strstr(relative, "//"), "bounded mount target");
	for (const char *part = relative; (part = strstr(part, "..")); part += 2)
		require(!((part == relative || part[-1] == '/') && (part[2] == 0 || part[2] == '/')), "safe mount target");
	return relative;
}

static void make_parents(const char *path, bool directory) {
	require(path && path[0] && strlen(path) <= 4096, "bounded mount target");
	char value[4097];
	require(strlen(path) < sizeof(value), "bounded mount target");
	strcpy(value, path);
	for (char *p = value + 1; *p; p++) {
		if (*p != '/') continue;
		*p = 0;
		if (mkdir(value, 0755) == -1 && errno != EEXIST) fail("create mount target parent");
		*p = '/';
	}
	if (directory) {
		if (mkdir(value, 0755) == -1 && errno != EEXIST) fail("create directory mount target");
	} else {
		int fd = open(value, O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC | O_NOFOLLOW, 0600);
		if (fd == -1) fail("create file mount target");
		if (close(fd) == -1) fail("close file mount target");
	}
}

static void set_mount_restrictions(const char *target, bool readonly, bool executable) {
	struct mount_attr attributes = {
		.attr_set = MOUNT_ATTR_NOSUID | MOUNT_ATTR_NODEV |
			(readonly ? MOUNT_ATTR_RDONLY : 0) | (executable ? 0 : MOUNT_ATTR_NOEXEC),
	};
	if (syscall(SYS_mount_setattr, AT_FDCWD, target, AT_RECURSIVE, &attributes, sizeof(attributes)) == -1)
		fail("restrict recursive mount");
}

static void bind_held(int fd, const char *target, bool readonly, bool executable) {
	struct stat st;
	if (fstat(fd, &st) == -1) fail("inspect bind source");
	require(S_ISDIR(st.st_mode) || S_ISREG(st.st_mode), "bind source type");
	const char *relative = relative_mount_target(target);
	char attached[8192];
	if (staging_root) {
		int attached_length = snprintf(attached, sizeof(attached), "%s/%s", staging_root, relative);
		require(attached_length > 0 && (size_t)attached_length < sizeof(attached), "bounded attached mount path");
	} else {
		strcpy(attached, relative);
	}
	make_parents(attached, S_ISDIR(st.st_mode));
	char source[64];
	int length = snprintf(source, sizeof(source), "/proc/self/fd/%d", fd);
	require(length > 0 && (size_t)length < sizeof(source), "format bind source");
	char resolved[4097] = {0};
	ssize_t resolved_length = readlink(source, resolved, sizeof(resolved) - 1);
	require(resolved_length > 0 && resolved_length < (ssize_t)sizeof(resolved) - 1 && resolved[0] == '/',
		"resolve held bind source");
	unsigned long flags = MS_BIND | (S_ISDIR(st.st_mode) ? MS_REC : 0);
	if (mount(resolved, attached, NULL, flags, NULL) == -1) {
		fail("bind held source");
	}
	struct stat mounted;
	if (stat(attached, &mounted) == -1 || mounted.st_dev != st.st_dev || mounted.st_ino != st.st_ino)
		fail("verify attached held mount");
	set_mount_restrictions(attached, readonly, executable);
}

static void bring_up_loopback(void) {
	int fd = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
	if (fd == -1) fail("open loopback control socket");
	struct ifreq request = {0};
	strncpy(request.ifr_name, "lo", IFNAMSIZ - 1);
	if (ioctl(fd, SIOCGIFFLAGS, &request) == -1) fail("read loopback flags");
	request.ifr_flags |= IFF_UP | IFF_RUNNING;
	if (ioctl(fd, SIOCSIFFLAGS, &request) == -1) fail("enable loopback");
	if (close(fd) == -1) fail("close loopback control socket");
}

static void setup_root(int root_fd, const char *root_path, int temporary_fd, int persistent_fd,
	int first_program, int program_count, int first_readonly, int readonly_count,
	const char *executable_readonly, char **targets, uid_t uid, gid_t gid, uint64_t temporary_bytes) {
	if (mount(NULL, "/", NULL, MS_REC | MS_PRIVATE, NULL) == -1) fail("make mounts private");
	struct stat held, named;
	require(safe_target(root_path), "safe root mount path");
	if (fstat(root_fd, &held) == -1 || lstat(root_path, &named) == -1 || !S_ISDIR(named.st_mode) ||
		held.st_dev != named.st_dev || held.st_ino != named.st_ino)
		fail("verify root mount path");
	if (mount("tmpfs", root_path, "tmpfs", MS_NOSUID | MS_NODEV, "size=67108864,mode=0755") == -1)
		fail("mount private root");
	staging_root = root_path;
	if (chdir(root_path) == -1) fail("enter private root staging path");
	if (mkdir("almagest", 0755) == -1 || mkdir("almagest/temp", 0700) == -1 ||
		mkdir("almagest/program", 0755) == -1) fail("create sandbox root layout");
	inspect_fd(temporary_fd, true, false);
	char temporary_target[8192], temporary_options[160];
	int target_length = snprintf(temporary_target, sizeof(temporary_target), "%s/almagest/temp", root_path);
	int options_length = snprintf(temporary_options, sizeof(temporary_options), "size=%llu,mode=0700,uid=%u,gid=%u",
		(unsigned long long)temporary_bytes, (unsigned)uid, (unsigned)gid);
	require(target_length > 0 && (size_t)target_length < sizeof(temporary_target) &&
		options_length > 0 && (size_t)options_length < sizeof(temporary_options), "temporary mount options");
	if (mount("tmpfs", temporary_target, "tmpfs", MS_NOSUID | MS_NODEV | MS_NOEXEC, temporary_options) == -1)
		fail("mount quota-limited temporary storage");
	if (persistent_fd >= 0) {
		if (mkdir("almagest/state", 0700) == -1) fail("create persistent mount target");
		bind_held(persistent_fd, "almagest/state", false, false);
	}
	for (int n = 0; n < program_count; n++) bind_held(first_program + n, targets[n], true, true);
	for (int n = 0; n < readonly_count; n++)
		bind_held(first_readonly + n, targets[program_count + n], true, executable_readonly[n] == '1');
	if (mkdir("proc", 0555) == -1 || mkdir("dev", 0755) == -1) fail("create synthetic system paths");
	if (mount("tmpfs", "dev", "tmpfs", MS_NOSUID | MS_NOEXEC, "size=1048576,mode=0755") == -1)
		fail("mount private dev");
	if (mknod("dev/null", S_IFCHR | 0666, makedev(1, 3)) == -1 ||
		mknod("dev/zero", S_IFCHR | 0666, makedev(1, 5)) == -1 ||
		mknod("dev/random", S_IFCHR | 0666, makedev(1, 8)) == -1 ||
		mknod("dev/urandom", S_IFCHR | 0666, makedev(1, 9)) == -1)
		fail("create private devices");
	if (chmod("dev/null", 0666) == -1 || chmod("dev/zero", 0666) == -1 ||
		chmod("dev/random", 0666) == -1 || chmod("dev/urandom", 0666) == -1)
		fail("set private device modes");
	if (mkdir("dev/shm", 01777) == -1 ||
		mount("tmpfs", "dev/shm", "tmpfs", MS_NOSUID | MS_NODEV | MS_NOEXEC, "size=16777216,mode=1777") == -1)
		fail("mount private shared memory");
	if (mkdir(".old-root", 0700) == -1 || syscall(SYS_pivot_root, ".", ".old-root") == -1)
		fail("pivot private root");
	if (chdir("/") == -1 || umount2("/.old-root", MNT_DETACH) == -1 || rmdir("/.old-root") == -1)
		fail("remove old root");
	staging_root = NULL;
	if (mount(NULL, "/", NULL, MS_REMOUNT | MS_RDONLY | MS_NOSUID | MS_NODEV, NULL) == -1)
		fail("seal private root");
}

static void worker_loop(int life_fd, int ready_fd, uid_t uid, gid_t gid) {
	if (mount("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, "hidepid=2") == -1)
		fail("mount private proc");
	bring_up_loopback();
	if (prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) == -1) fail("disable worker dumps");
	remove_authority(uid, gid);
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("set worker no_new_privs");
	install_seccomp();
	struct sigaction action = {.sa_handler = child_changed};
	if (sigemptyset(&action.sa_mask) == -1 || sigaction(SIGCHLD, &action, NULL) == -1)
		fail("install namespace reaper signal handler");
	char ready = '1';
	if (write(ready_fd, &ready, 1) != 1) fail("report worker readiness");
	if (close(ready_fd) == -1) fail("close worker readiness channel");
	for (;;) {
		struct pollfd descriptor = {.fd = life_fd, .events = POLLIN | POLLHUP};
		int result = poll(&descriptor, 1, -1);
		if (result == -1 && errno == EINTR) {
			while (waitpid(-1, NULL, WNOHANG) > 0) {}
			continue;
		}
		if (result == -1) fail("wait for supervisor");
		if (descriptor.revents & (POLLHUP | POLLERR | POLLNVAL)) break;
		char byte;
		ssize_t count = read(life_fd, &byte, 1);
		if (count <= 0) break;
	}
	(void)kill(-1, SIGKILL);
	while (waitpid(-1, NULL, 0) > 0 || errno == EINTR) {}
	_exit(0);
}

static int worker_main(int argc, char **argv) {
	require(argc >= 11 && strcmp(argv[1], "worker-v3") == 0 && strcmp(argv[10], "--") == 0, "worker ABI");
	uid_t uid = (uid_t)number(argv[2]);
	gid_t gid = (gid_t)number(argv[3]);
	int writable_count = (int)number(argv[4]);
	int program_count = (int)number(argv[5]);
	int readonly_count = (int)number(argv[6]);
	uint64_t temporary_bytes = number(argv[7]);
	const char *executable_readonly = argv[9];
	require(uid > 0 && gid > 0 && (writable_count == 1 || writable_count == 2) &&
		program_count >= 1 && program_count <= 256 && readonly_count >= 0 && readonly_count <= 128 &&
		strlen(executable_readonly) == (size_t)readonly_count && argc == 11 + program_count + readonly_count,
		"worker bounds");
	for (int n = 0; n < readonly_count; n++)
		require(executable_readonly[n] == '0' || executable_readonly[n] == '1', "worker executable flags");
	if (fcntl(status_fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal worker status channel");
	inspect_fd(3, false, true);
	inspect_fd(7, true, false);
	inspect_fd(8, true, false);
	int persistent_fd = writable_count == 2 ? 9 : -1;
	if (persistent_fd >= 0) inspect_fd(persistent_fd, true, false);
	int first_program = 8 + writable_count;
	for (int n = 0; n < program_count; n++) inspect_fd(first_program + n, false, true);
	int first_readonly = first_program + program_count;
	for (int n = 0; n < readonly_count; n++) inspect_readonly_fd(first_readonly + n);
	enter_cgroup(6);
	if (unshare(CLONE_NEWCGROUP | CLONE_NEWIPC | CLONE_NEWNET | CLONE_NEWNS | CLONE_NEWPID | CLONE_NEWUTS) == -1)
		fail("create sandbox namespaces");
	if (sethostname("almagest-sandbox", 16) == -1) fail("set private hostname");
	setup_root(7, argv[8], 8, persistent_fd, first_program, program_count, first_readonly, readonly_count,
		executable_readonly, argv + 11,
		uid, gid, temporary_bytes);
	int ready[2];
	if (pipe2(ready, O_CLOEXEC) == -1) fail("create worker readiness channel");
	pid_t child = fork();
	if (child == -1) fail("start namespace reaper");
	if (child == 0) {
		if (close(ready[0]) == -1 || close(status_fd) == -1) fail("close worker parent channels");
		if (close(3) == -1 || close(6) == -1 || close(7) == -1) fail("close worker setup descriptors");
		for (int fd = 8; fd < first_readonly + readonly_count; fd++)
			if (close(fd) == -1) fail("close worker source descriptors");
		worker_loop(5, ready[1], uid, gid);
	}
	if (close(ready[1]) == -1) fail("close worker child channel");
	if (close(3) == -1 || close(5) == -1 || close(6) == -1 || close(7) == -1)
		fail("close worker parent setup descriptors");
	for (int fd = 8; fd < first_readonly + readonly_count; fd++)
		if (close(fd) == -1) fail("close worker parent source descriptors");
	char marker;
	if (read(ready[0], &marker, 1) != 1 || marker != '1') fail("worker setup failed");
	if (close(ready[0]) == -1) fail("close worker readiness channel");
	char message[64];
	int length = snprintf(message, sizeof(message), "READY %ld\n", (long)child);
	if (length <= 0 || write(status_fd, message, (size_t)length) != length) fail("report worker PID");
	if (close(status_fd) == -1) _exit(125);
	int state;
	while (waitpid(child, &state, 0) == -1 && errno == EINTR) {}
	if (WIFEXITED(state)) return WEXITSTATUS(state);
	if (WIFSIGNALED(state)) {
		signal(WTERMSIG(state), SIG_DFL);
		raise(WTERMSIG(state));
	}
	return 125;
}

static void install_full_landlock(int first_executable, int executable_count) {
	int root = open("/", O_PATH | O_DIRECTORY | O_CLOEXEC);
	int temporary = open("/almagest/temp", O_PATH | O_DIRECTORY | O_CLOEXEC);
	int persistent = open("/almagest/state", O_PATH | O_DIRECTORY | O_CLOEXEC);
	int null_device = open("/dev/null", O_PATH | O_CLOEXEC);
	int zero_device = open("/dev/zero", O_PATH | O_CLOEXEC);
	if (root == -1 || temporary == -1 || (persistent == -1 && errno != ENOENT))
		fail("open confined Landlock paths");
	if (null_device == -1 || zero_device == -1) fail("open confined synthetic devices");
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	require(abi >= 3, "Landlock ABI 3 required");
	uint64_t handled = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_WRITE_FILE |
		LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
		LANDLOCK_ACCESS_FS_MAKE_CHAR | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_FIFO | LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM | LANDLOCK_ACCESS_FS_REFER | LANDLOCK_ACCESS_FS_TRUNCATE;
	struct landlock_ruleset_attr rules = {.handled_access_fs = handled};
	int ruleset = (int)syscall(SYS_landlock_create_ruleset, &rules, sizeof(rules), 0);
	if (ruleset == -1) fail("create confined Landlock ruleset");
	// Mount-level noexec and the per-inode rules below form the executable
	// allowlist. Executable read-only dependencies are included in the held
	// descriptor set after the approved program images.
	add_path_rule(ruleset, root, LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR);
	for (int n = 0; n < executable_count; n++)
		add_path_rule(ruleset, first_executable + n, LANDLOCK_ACCESS_FS_EXECUTE);
	uint64_t writable = LANDLOCK_ACCESS_FS_WRITE_FILE | LANDLOCK_ACCESS_FS_READ_FILE |
		LANDLOCK_ACCESS_FS_READ_DIR | LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
		LANDLOCK_ACCESS_FS_MAKE_DIR | LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM | LANDLOCK_ACCESS_FS_REFER | LANDLOCK_ACCESS_FS_TRUNCATE;
	add_path_rule(ruleset, temporary, writable);
	if (persistent >= 0) add_path_rule(ruleset, persistent, writable);
	add_path_rule(ruleset, null_device, LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_WRITE_FILE);
	add_path_rule(ruleset, zero_device, LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_WRITE_FILE);
	if (syscall(SYS_landlock_restrict_self, ruleset, 0) == -1) fail("apply confined Landlock ruleset");
	if (close(root) == -1 || close(temporary) == -1 || (persistent >= 0 && close(persistent) == -1) ||
		close(null_device) == -1 || close(zero_device) == -1 || close(ruleset) == -1)
		fail("close confined Landlock descriptors");
}

static int launch_main(int argc, char **argv) {
	require(argc >= 12 && strcmp(argv[1], "launch-v4") == 0 && strcmp(argv[11], "--") == 0, "full launcher ABI");
	uid_t uid = (uid_t)number(argv[2]);
	gid_t gid = (gid_t)number(argv[3]);
	uint64_t descriptors = number(argv[4]);
	uint64_t memory_bytes = number(argv[5]), tasks = number(argv[6]);
	const char *program_path = argv[7];
	const char *cwd = argv[8];
	int program_count = (int)number(argv[9]);
	int executable_count = (int)number(argv[10]);
	require(uid > 0 && gid > 0 && descriptors >= 32 && descriptors <= 65536 &&
		memory_bytes >= (32ULL << 20) && tasks >= 1 && tasks <= 4096 &&
		program_count >= 1 && program_count <= 256 && executable_count >= program_count &&
		executable_count <= 384 && safe_target(program_path) && safe_target(cwd), "full launcher bounds");
	if (fcntl(status_fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal exec-status channel");
	inspect_fd(3, false, true);
	inspect_fd(5, true, false);
	for (int fd = 13; fd < 13 + program_count; fd++) inspect_fd(fd, false, true);
	for (int fd = 13 + program_count; fd < 13 + executable_count; fd++) inspect_fd(fd, false, false);
	enter_cgroup(12);
	for (int fd = 6; fd <= 9; fd++) if (setns(fd, 0) == -1) fail("join sandbox namespace");
	if (setns(11, CLONE_NEWCGROUP) == -1) fail("join sandbox cgroup namespace");
	if (setns(10, CLONE_NEWPID) == -1) fail("join sandbox PID namespace");
	pid_t child = fork();
	if (child == -1) fail("start sandbox command");
	if (child != 0) {
		if (close(status_fd) == -1) _exit(125);
		for (int fd = 3; fd < 13 + executable_count; fd++)
			if (fd != status_fd && close(fd) == -1) _exit(125);
		int state;
		while (waitpid(child, &state, 0) == -1 && errno == EINTR) {}
		if (WIFEXITED(state)) return WEXITSTATUS(state);
		if (WIFSIGNALED(state)) {
			signal(WTERMSIG(state), SIG_DFL);
			raise(WTERMSIG(state));
		}
		return 125;
	}
	if (fchdir(5) == -1 || chroot(".") == -1 || chdir(cwd) == -1) fail("enter sandbox root");
	umask(0077);
	struct stat approved, mounted;
	if (fstat(13, &approved) == -1 || stat(program_path, &mounted) == -1 ||
		approved.st_dev != mounted.st_dev || approved.st_ino != mounted.st_ino)
		fail("verify mounted approved executable");
	set_limit(RLIMIT_NOFILE, descriptors, "set descriptor limit");
	set_limit(RLIMIT_AS, memory_bytes, "set address-space limit");
	set_limit(RLIMIT_NPROC, tasks, "set task limit");
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("set no_new_privs");
	install_full_landlock(13, executable_count);
	install_seccomp();
	remove_authority(uid, gid);
	for (int fd = 3; fd < 13 + executable_count; fd++) if (fd != status_fd && close(fd) == -1) fail("close inherited authority");
	char **target = calloc((size_t)argc - 10, sizeof(char *));
	if (!target) fail("allocate target arguments");
	target[0] = (char *)"sandbox-program";
	for (int n = 12; n < argc; n++) target[n - 11] = argv[n];
	target[argc - 11] = NULL;
	if (execve(program_path, target, environ) == -1) fail("execute approved image");
	return 125;
}

static void send_descriptor(int channel, int descriptor) {
	char byte = '1';
	struct iovec vector = {.iov_base = &byte, .iov_len = 1};
	char control[CMSG_SPACE(sizeof(int))] = {0};
	struct msghdr message = {
		.msg_iov = &vector,
		.msg_iovlen = 1,
		.msg_control = control,
		.msg_controllen = sizeof(control),
	};
	struct cmsghdr *header = CMSG_FIRSTHDR(&message);
	header->cmsg_level = SOL_SOCKET;
	header->cmsg_type = SCM_RIGHTS;
	header->cmsg_len = CMSG_LEN(sizeof(int));
	memcpy(CMSG_DATA(header), &descriptor, sizeof(descriptor));
	if (sendmsg(channel, &message, MSG_NOSIGNAL) != 1) fail("return sandbox socket");
}

static int internet_socket(const char *operation, const char *network, const char *address) {
	const char *separator = strrchr(address, ':');
	require(separator && separator != address && separator[1], "numeric socket address");
	char host[INET6_ADDRSTRLEN + 1];
	const char *host_start = address;
	size_t host_length = (size_t)(separator - address);
	if (address[0] == '[') {
		require(separator > address + 2 && separator[-1] == ']', "bracketed socket address");
		host_start++;
		host_length -= 2;
	}
	require(host_length > 0 && host_length < sizeof(host), "bounded socket host");
	memcpy(host, host_start, host_length);
	host[host_length] = 0;
	uint64_t port = number(separator + 1);
	require(port <= 65535, "socket port");
	int type = strncmp(network, "tcp", 3) == 0 ? SOCK_STREAM : SOCK_DGRAM;
	struct sockaddr_storage storage = {0};
	socklen_t address_length;
	int family;
	if (strchr(host, ':')) {
		require(strcmp(network, "tcp4") != 0 && strcmp(network, "udp4") != 0, "IPv6 network");
		struct sockaddr_in6 *target = (struct sockaddr_in6 *)&storage;
		target->sin6_family = AF_INET6;
		target->sin6_port = htons((uint16_t)port);
		require(inet_pton(AF_INET6, host, &target->sin6_addr) == 1 && IN6_IS_ADDR_LOOPBACK(&target->sin6_addr),
			"numeric IPv6 loopback address");
		family = AF_INET6;
		address_length = sizeof(*target);
	} else {
		require(strcmp(network, "tcp6") != 0 && strcmp(network, "udp6") != 0, "IPv4 network");
		struct sockaddr_in *target = (struct sockaddr_in *)&storage;
		target->sin_family = AF_INET;
		target->sin_port = htons((uint16_t)port);
		require(inet_pton(AF_INET, host, &target->sin_addr) == 1 &&
			(ntohl(target->sin_addr.s_addr) >> 24) == 127, "numeric IPv4 loopback address");
		family = AF_INET;
		address_length = sizeof(*target);
	}
	int fd = socket(family, type | SOCK_CLOEXEC, 0);
	if (fd == -1) fail("create sandbox socket");
	if (strcmp(operation, "dial") == 0) {
		if (connect(fd, (struct sockaddr *)&storage, address_length) == -1) fail("connect sandbox socket");
	} else {
		int enabled = 1;
		if (setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &enabled, sizeof(enabled)) == -1 ||
			bind(fd, (struct sockaddr *)&storage, address_length) == -1) fail("bind sandbox socket");
		if (type == SOCK_STREAM && listen(fd, 128) == -1) fail("listen on sandbox socket");
	}
	return fd;
}

static int unix_socket(const char *operation, const char *address) {
	require(safe_target(address) && strlen(address) < sizeof(((struct sockaddr_un *)0)->sun_path),
		"safe Unix socket path");
	int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
	if (fd == -1) fail("create sandbox Unix socket");
	struct sockaddr_un target = {.sun_family = AF_UNIX};
	strcpy(target.sun_path, address);
	if (strcmp(operation, "dial") == 0) {
		if (connect(fd, (struct sockaddr *)&target, sizeof(target)) == -1) fail("connect sandbox Unix socket");
	} else {
		if (bind(fd, (struct sockaddr *)&target, sizeof(target)) == -1 || listen(fd, 128) == -1)
			fail("listen on sandbox Unix socket");
	}
	return fd;
}

static int socket_main(int argc, char **argv) {
	require(argc == 7 && (strcmp(argv[4], "dial") == 0 || strcmp(argv[4], "listen") == 0 ||
		strcmp(argv[4], "packet") == 0), "socket helper ABI");
	uid_t uid = (uid_t)number(argv[2]);
	gid_t gid = (gid_t)number(argv[3]);
	require(uid > 0 && gid > 0, "socket helper identity");
	if (fcntl(status_fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal socket status channel");
	inspect_fd(3, false, true);
	inspect_fd(6, true, false);
	if (setns(7, CLONE_NEWNS) == -1 || setns(8, CLONE_NEWNET) == -1) fail("join sandbox socket context");
	if (fchdir(6) == -1 || chroot(".") == -1 || chdir("/") == -1) fail("enter sandbox socket root");
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("set socket no_new_privs");
	remove_authority(uid, gid);
	install_seccomp();
	int fd;
	if (strcmp(argv[5], "unix") == 0) {
		require(strcmp(argv[4], "packet") != 0, "Unix packet sockets are unsupported");
		fd = unix_socket(argv[4], argv[6]);
	} else {
		require(strcmp(argv[5], "tcp") == 0 || strcmp(argv[5], "tcp4") == 0 || strcmp(argv[5], "tcp6") == 0 ||
			strcmp(argv[5], "udp") == 0 || strcmp(argv[5], "udp4") == 0 || strcmp(argv[5], "udp6") == 0,
			"socket network");
		if (strcmp(argv[4], "packet") == 0) require(strncmp(argv[5], "udp", 3) == 0, "packet network");
		fd = internet_socket(argv[4], argv[5], argv[6]);
	}
	send_descriptor(5, fd);
	if (close(fd) == -1 || close(status_fd) == -1) return 125;
	return 0;
}

static int probe_main(int argc, char **argv) {
	(void)argv;
	require(argc == 2, "probe ABI");
	if (fcntl(status_fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal probe status channel");
	inspect_fd(3, false, true);
	if (unshare(CLONE_NEWCGROUP | CLONE_NEWIPC | CLONE_NEWNET | CLONE_NEWNS | CLONE_NEWPID | CLONE_NEWUTS) == -1)
		fail("probe sandbox namespaces");
	if (mount(NULL, "/", NULL, MS_REC | MS_PRIVATE, NULL) == -1) fail("probe private mounts");
	char root[] = "/tmp/almagest-subprogram-probe-XXXXXX";
	if (!mkdtemp(root) || mount("tmpfs", root, "tmpfs", MS_NOSUID | MS_NODEV, "size=1048576") == -1)
		fail("probe private root mount");
	struct mount_attr attributes = {.attr_set = MOUNT_ATTR_RDONLY | MOUNT_ATTR_NOSUID | MOUNT_ATTR_NODEV};
	if (syscall(SYS_mount_setattr, AT_FDCWD, root, AT_RECURSIVE, &attributes, sizeof(attributes)) == -1)
		fail("probe recursive mount attributes");
	bring_up_loopback();
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	require(abi >= 3, "probe Landlock ABI");
	if (umount2(root, MNT_DETACH) == -1 || rmdir(root) == -1) fail("clean probe root mount");
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("probe no_new_privs");
	install_seccomp();
	if (close(status_fd) == -1) return 125;
	return 0;
}

static int legacy_main(int argc, char **argv) {
	require(argc >= 12 && strcmp(argv[1], "v3") == 0 && strcmp(argv[11], "--") == 0,
		"launcher ABI");
	uid_t uid = (uid_t)number(argv[2]);
	gid_t gid = (gid_t)number(argv[3]);
	uint64_t descriptors = number(argv[4]);
	uint64_t memory_bytes = number(argv[5]), tasks = number(argv[6]);
	int writable_count = (int)number(argv[7]);
	int executable_count = (int)number(argv[8]);
	int readonly_count = (int)number(argv[9]);
	const char *executable_readonly = argv[10];
	require(uid > 0 && gid > 0 && descriptors >= 32 && descriptors <= 65536 &&
		memory_bytes >= (32ULL << 20) &&
		tasks >= 1 && tasks <= 4096 && (writable_count == 1 || writable_count == 2) &&
		executable_count >= 1 && executable_count <= 65 && readonly_count >= 0 && readonly_count <= 128 &&
		strlen(executable_readonly) == (size_t)readonly_count,
		"launcher bounds");
	for (int n = 0; n < readonly_count; n++)
		require(executable_readonly[n] == '0' || executable_readonly[n] == '1', "launcher executable flags");
	if (fcntl(status_fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal exec-status channel");
	inspect_fd(3, false, true);
	inspect_fd(5, true, false);
	for (int n = 0; n < writable_count; n++) inspect_fd(6 + n, true, false);
	int first_executable = 6 + writable_count;
	for (int n = 0; n < executable_count; n++) inspect_fd(first_executable + n, false, true);
	for (int n = 0; n < readonly_count; n++) inspect_readonly_fd(first_executable + executable_count + n);
	set_limit(RLIMIT_NOFILE, descriptors, "set descriptor limit");
	set_limit(RLIMIT_AS, memory_bytes, "set address-space limit");
	/* RLIMIT_NPROC counts all tasks for the real UID. The partial profile uses
	 * the daemon user's UID, so applying the template limit here can prevent
	 * all thread creation when unrelated host tasks already exceed it. */
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("set no_new_privs");
	install_landlock(writable_count, executable_count, readonly_count, executable_readonly);
	install_seccomp();
	if (fchdir(5) == -1) fail("select working directory");
	remove_authority(uid, gid);
	int last_input = first_executable + executable_count + readonly_count;
	if (close(3) == -1) fail("close launcher image");
	for (int fd = 5; fd < last_input; fd++) {
		if (fd == first_executable) {
			if (fcntl(fd, F_SETFD, FD_CLOEXEC) == -1) fail("seal executable descriptor");
		} else if (close(fd) == -1) {
			fail("close inherited authority");
		}
	}
	char **target = calloc((size_t)argc - 10, sizeof(char *));
	if (!target) fail("allocate target arguments");
	target[0] = (char *)"sandbox-program";
	for (int n = 12; n < argc; n++) target[n - 11] = argv[n];
	target[argc - 11] = NULL;
	if (syscall(SYS_execveat, first_executable, "", target, environ, AT_EMPTY_PATH) == -1)
		fail("execute approved image");
	return 125;
}

int main(int argc, char **argv) {
	if (argc >= 2 && strcmp(argv[1], "worker-v3") == 0) return worker_main(argc, argv);
	if (argc >= 2 && strcmp(argv[1], "launch-v4") == 0) return launch_main(argc, argv);
	if (argc >= 2 && strcmp(argv[1], "socket-v2") == 0) return socket_main(argc, argv);
	if (argc >= 2 && strcmp(argv[1], "probe-v2") == 0) return probe_main(argc, argv);
	return legacy_main(argc, argv);
}
