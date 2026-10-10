/* Linux/amd64 role setup. This program is NEVER setuid or file-capability
 * installed. Trusted bootstrap supplies held descriptors, not path strings.
 * No Go code runs between fork and exec. See docs/DAEMON_ISOLATION_LINUX_PROFILES.md. */
#define _GNU_SOURCE
#include <elf.h>
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/landlock.h>
#include <linux/magic.h>
#include <linux/mount.h>
#include <linux/securebits.h>
#include <poll.h>
#include <sched.h>
#include <seccomp.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/statfs.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

#if !defined(__x86_64__) || defined(__ILP32__)
#error This profile is qualified only for Linux amd64
#endif
#ifndef LANDLOCK_ACCESS_FS_REFER
#define LANDLOCK_ACCESS_FS_REFER (1ULL << 13)
#endif
#ifndef LANDLOCK_ACCESS_FS_TRUNCATE
#define LANDLOCK_ACCESS_FS_TRUNCATE (1ULL << 14)
#endif

enum role { RUNTIME, WRITER, COMMAND };
struct plan {
	enum role role;
	uid_t uid;
	gid_t gid;
	uint64_t caps, descriptors, file_bytes, memory_bytes, tasks, cpu;
	unsigned channels;
};

static void fail(const char *operation) {
	fprintf(stderr, "Linux isolation unavailable: %s: %s; use explicit --no-isolation\n", operation, strerror(errno));
	exit(1);
}

static void require(bool condition, const char *operation) {
	if (!condition) { errno = EINVAL; fail(operation); }
}

static uint64_t number(const char *text) {
	require(text[0] != '\0' && strlen(text) <= 20, "bounded decimal input");
	uint64_t result = 0;
	for (const char *p = text; *p; p++) {
		require(*p >= '0' && *p <= '9', "decimal input");
		unsigned digit = (unsigned)(*p - '0');
		require(result <= (UINT64_MAX - digit) / 10, "decimal overflow");
		result = result * 10 + digit;
	}
	return result;
}

static struct plan parse(int argc, char **argv) {
	require(argc == 11 && strcmp(argv[1], "v1") == 0, "launcher ABI v1");
	struct plan p = {0};
	if (strcmp(argv[2], "runtime") == 0) p.role = RUNTIME;
	else if (strcmp(argv[2], "writer") == 0) p.role = WRITER;
	else if (strcmp(argv[2], "command") == 0) p.role = COMMAND;
	else require(false, "closed role selection");
	uint64_t uid = number(argv[3]), gid = number(argv[4]);
	require(uid > 0 && uid < UINT32_MAX && gid > 0 && gid < UINT32_MAX, "non-root role identity");
	p.uid = (uid_t)uid; p.gid = (gid_t)gid;
	p.caps = number(argv[5]); p.descriptors = number(argv[6]);
	p.file_bytes = number(argv[7]); p.memory_bytes = number(argv[8]);
	p.tasks = number(argv[9]); p.cpu = number(argv[10]);
	require(p.caps == 0, "role capabilities must be zero");
	require(p.descriptors >= 32 && p.descriptors <= 65536 && p.file_bytes >= (1ULL << 20) &&
		p.file_bytes <= (1ULL << 40) && p.memory_bytes >= (32ULL << 20) && p.memory_bytes <= (1ULL << 40) &&
		p.tasks >= 8 && p.tasks <= 4096 && p.cpu >= 1000 && p.cpu <= 6400000, "resource ceilings");
	return p;
}

static void read_cgroup(const char *name, char *buffer, size_t size) {
	int fd = openat(6, name, O_RDONLY | O_NOFOLLOW | O_CLOEXEC);
	if (fd == -1) fail("open cgroup limit");
	ssize_t n = read(fd, buffer, size - 1);
	require(n > 0 && (size_t)n < size - 1, "bounded cgroup value");
	buffer[n] = '\0';
	if (close(fd) == -1) fail("close cgroup limit");
}

static uint64_t cgroup_number(const char *name) {
	char text[64]; read_cgroup(name, text, sizeof(text));
	text[strcspn(text, "\n")] = '\0';
	return number(text); /* 'max' is deliberately rejected. */
}

static void check_cgroup(const struct plan *p) {
	struct statfs st;
	if (fstatfs(6, &st) == -1) fail("inspect cgroup descriptor");
	require(st.f_type == CGROUP2_SUPER_MAGIC, "cgroup v2 required");
	uint64_t memory = cgroup_number("memory.max"), tasks = cgroup_number("pids.max");
	require(memory > 0 && memory <= p->memory_bytes && tasks > 0 && tasks <= p->tasks, "cgroup memory/task limits");
	require(cgroup_number("memory.swap.max") == 0, "cgroup swap must be disabled");
	char cpu[128]; read_cgroup("cpu.max", cpu, sizeof(cpu));
	char *space = strchr(cpu, ' ');
	require(space != NULL, "cgroup CPU format"); *space++ = '\0';
	space[strcspn(space, "\n")] = '\0';
	uint64_t quota = number(cpu), period = number(space);
	require(period == 100000 && quota > 0 && quota <= p->cpu, "cgroup CPU limit");
	char members[65536]; read_cgroup("cgroup.procs", members, sizeof(members));
	char *save = NULL;
	bool found = false;
	for (char *line = strtok_r(members, "\n", &save); line; line = strtok_r(NULL, "\n", &save)) {
		if (number(line) == (uint64_t)getpid()) found = true;
	}
	require(found, "launcher must already belong to supplied cgroup");
}

static void check_executable(int fd) {
	struct stat st;
	if (fstat(fd, &st) == -1) fail("inspect executable");
	require(S_ISREG(st.st_mode) && st.st_uid == 0 && !(st.st_mode & 0022) &&
		(st.st_mode & 0111) && st.st_nlink == 1 && st.st_size > 0 && st.st_size <= (128 << 20), "trusted static executable");
	Elf64_Ehdr eh;
	require(pread(fd, &eh, sizeof(eh), 0) == sizeof(eh), "read ELF header");
	require(memcmp(eh.e_ident, ELFMAG, SELFMAG) == 0 && eh.e_ident[EI_CLASS] == ELFCLASS64 &&
		eh.e_ident[EI_DATA] == ELFDATA2LSB && eh.e_machine == EM_X86_64 && eh.e_type == ET_EXEC &&
		eh.e_phentsize == sizeof(Elf64_Phdr) && eh.e_phnum > 0 && eh.e_phnum <= 128 &&
		eh.e_phoff <= (uint64_t)st.st_size && (uint64_t)eh.e_phnum * sizeof(Elf64_Phdr) <= (uint64_t)st.st_size - eh.e_phoff,
		"static amd64 ELF required");
	for (unsigned i = 0; i < eh.e_phnum; i++) {
		Elf64_Phdr ph;
		require(pread(fd, &ph, sizeof(ph), (off_t)(eh.e_phoff + i * sizeof(ph))) == sizeof(ph), "read ELF program header");
		require(ph.p_type != PT_INTERP && ph.p_type != PT_DYNAMIC, "dynamic code loading prohibited");
	}
}

static void check_descriptors(struct plan *p) {
	/* Logs must be pipes or connected Unix sockets, never a host file, device,
	 * native socket or terminal. Stdin becomes an EOF pipe before exec. */
	for (int fd = 1; fd <= 3; fd++) {
		struct stat st;
		if (fstat(fd, &st) == -1) fail("inspect inherited channel");
		if (fd < 3 && S_ISFIFO(st.st_mode)) continue;
		int domain = 0, type = 0; socklen_t length = sizeof(int);
		if (getsockopt(fd, SOL_SOCKET, SO_DOMAIN, &domain, &length) == -1) fail("channel domain");
		if (getsockopt(fd, SOL_SOCKET, SO_TYPE, &type, &length) == -1) fail("channel type");
		struct sockaddr_storage peer; length = sizeof(peer);
		if (getpeername(fd, (struct sockaddr *)&peer, &length) == -1) fail("connected channel required");
		require(domain == AF_UNIX && type == SOCK_STREAM, "Unix stream channel required");
	}
	for (int fd = 9; fd <= 11; fd++) {
		if (fcntl(fd, F_GETFD) == -1) { require(errno == EBADF, "inspect optional channel"); continue; }
		require(fd == 10 || p->role == RUNTIME, "role channel selection");
		int domain = 0, type = 0; socklen_t length = sizeof(int);
		if (getsockopt(fd, SOL_SOCKET, SO_DOMAIN, &domain, &length) == -1 ||
			getsockopt(fd, SOL_SOCKET, SO_TYPE, &type, &length) == -1) fail("optional channel type");
		struct sockaddr_storage peer; length = sizeof(peer);
		if (getpeername(fd, (struct sockaddr *)&peer, &length) == -1) fail("optional connected channel");
		require(domain == AF_UNIX && type == SOCK_STREAM, "optional Unix stream required");
		p->channels |= 1U << (fd - 9);
	}
	check_executable(4);
	if (p->role == COMMAND) check_executable(7);
	struct stat st;
	if (fstat(5, &st) == -1) fail("inspect role storage");
	require(S_ISDIR(st.st_mode) && st.st_uid == p->uid && st.st_gid == p->gid &&
		(st.st_mode & 0777) == 0700, "private provisioned role directory");
	check_cgroup(p);
}

static void make_dir(const char *path, mode_t mode) {
	if (mkdir(path, mode) == -1) fail("create private directory");
}

static void copy_executable(int source, const char *target) {
	int out = open(target, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0500);
	if (out == -1) fail("create private executable");
	char buffer[65536]; off_t offset = 0;
	for (;;) {
		ssize_t n = pread(source, buffer, sizeof(buffer), offset);
		if (n == 0) break;
		if (n < 0) fail("read executable");
		require(offset <= (128 << 20) - n, "executable copy bound");
		for (ssize_t written = 0; written < n;) {
			ssize_t count = write(out, buffer + written, (size_t)(n - written));
			if (count <= 0) fail("copy executable");
			written += count;
		}
		offset += n;
	}
	if (fchmod(out, 0555) == -1 || close(out) == -1) fail("seal executable");
}

static void filesystem(const struct plan *p) {
	/* Clone the held directory into a detached mount. A /proc/self/fd bind
	 * after unshare would still refer to the old namespace's mount and fail
	 * check_mnt; resolving a pathname again would reintroduce a path race. */
	int state_mount = (int)syscall(SYS_open_tree, 5, "", AT_EMPTY_PATH | OPEN_TREE_CLONE | OPEN_TREE_CLOEXEC);
	if (state_mount == -1) fail("clone held role directory");
	/* unshare precedes propagation changes: never alter the parent mount tree. */
	int flags = CLONE_NEWNS | CLONE_NEWIPC | CLONE_NEWUTS | CLONE_NEWNET;
	if (unshare(flags) == -1) fail("create role namespaces");
	if (mount(NULL, "/", NULL, MS_REC | MS_PRIVATE, NULL) == -1) fail("private mount propagation");
	if (sethostname("almagest-role", 13) == -1) fail("private hostname");
	/* The parent creates a unique mount point and removes it after child exit. */
	if (mount("tmpfs", ".", "tmpfs", MS_NOSUID | MS_NODEV, "mode=0755,size=272m") == -1) fail("private root");
	/* Enter the new mount, not the covered working-directory dentry. */
	char path[4096];
	if (!getcwd(path, sizeof(path)) || chdir(path) == -1) fail("enter private root");
	make_dir("state", 0700); make_dir("tmp", 0700); make_dir("oldroot", 0700);
	if (chown("tmp", p->uid, p->gid) == -1) fail("own private temporary storage");
	/* The bind uses a held descriptor. Do not recursively import submounts. */
	if (syscall(SYS_move_mount, state_mount, "", AT_FDCWD, "state", MOVE_MOUNT_F_EMPTY_PATH) == -1) fail("mount held role directory");
	if (close(state_mount) == -1) fail("close detached mount");
	if (mount(NULL, "state", NULL, MS_BIND | MS_REMOUNT | MS_NOSUID | MS_NODEV | MS_NOEXEC, NULL) == -1) fail("restrict role directory");
	copy_executable(4, "role");
	if (p->role == COMMAND) copy_executable(7, "command");
	/* A readable sentinel verifies Landlock separately from namespace hiding. */
	int blocked = open("blocked", O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0444);
	if (blocked == -1 || close(blocked) == -1) fail("create denial sentinel");
	if (syscall(SYS_pivot_root, ".", "oldroot") == -1 || chdir("/") == -1 ||
		umount2("/oldroot", MNT_DETACH) == -1 || rmdir("/oldroot") == -1) fail("detach host filesystem");
}

static void drop_identity(const struct plan *p) {
	struct __user_cap_header_struct header = {.version = _LINUX_CAPABILITY_VERSION_3};
	struct __user_cap_data_struct data[2] = {{0}};
	for (int cap = 0; cap < 64; cap++) {
		if (prctl(PR_CAPBSET_DROP, cap, 0, 0, 0) == -1 && errno != EINVAL) fail("drop capability bounding set");
	}
	unsigned bits = SECBIT_NOROOT | SECBIT_NOROOT_LOCKED | SECBIT_NO_SETUID_FIXUP |
		SECBIT_NO_SETUID_FIXUP_LOCKED | SECBIT_KEEP_CAPS_LOCKED |
		SECBIT_NO_CAP_AMBIENT_RAISE | SECBIT_NO_CAP_AMBIENT_RAISE_LOCKED;
	if (prctl(PR_SET_SECUREBITS, bits, 0, 0, 0) == -1 || setgroups(0, NULL) == -1 ||
		setresgid(p->gid, p->gid, p->gid) == -1 || setresuid(p->uid, p->uid, p->uid) == -1) fail("drop role identity");
	if (syscall(SYS_capset, &header, data) == -1 || prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) == -1) fail("drop capability sets");
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1 || prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) == -1) fail("lock process privileges");
}

static void landlock_rule(int ruleset, const char *path, uint64_t rights) {
	int fd = open(path, O_PATH | O_CLOEXEC);
	if (fd == -1) fail("open Landlock path");
	struct landlock_path_beneath_attr rule = {.allowed_access = rights, .parent_fd = fd};
	if (syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &rule, 0) == -1) fail("add Landlock rule");
	if (close(fd) == -1) fail("close Landlock path");
}

static int landlock(const struct plan *p) {
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	require(abi >= 3, "Landlock ABI 3 required");
	uint64_t handled = (1ULL << 15) - 1;
	struct landlock_ruleset_attr attr = {.handled_access_fs = handled};
	int fd = (int)syscall(SYS_landlock_create_ruleset, &attr, sizeof(attr), 0);
	if (fd == -1) fail("create Landlock ruleset");
	landlock_rule(fd, "/", LANDLOCK_ACCESS_FS_READ_DIR);
	landlock_rule(fd, "/role", LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_EXECUTE);
	if (p->role == COMMAND) landlock_rule(fd, "/command", LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_EXECUTE);
	uint64_t writable = LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_WRITE_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SYM | LANDLOCK_ACCESS_FS_REFER | LANDLOCK_ACCESS_FS_TRUNCATE;
	landlock_rule(fd, "/state", writable); landlock_rule(fd, "/tmp", writable);
	if (syscall(SYS_landlock_restrict_self, fd, 0) == -1 || close(fd) == -1) fail("apply Landlock");
	return abi;
}

#include "syscalls.h"

static void limit_resource(int resource, uint64_t maximum) {
	struct rlimit old;
	if (getrlimit(resource, &old) == -1) fail("read resource limit");
	if (old.rlim_max < maximum) maximum = old.rlim_max;
	struct rlimit limit = {.rlim_cur = maximum, .rlim_max = maximum};
	if (setrlimit(resource, &limit) == -1) fail("set hard resource limit");
}

static void run(const struct plan *p, int setup, int parent_pidfd) {
	filesystem(p);
	/* The trusted C child has detached the host root. The waiting parent can
	 * now remove its empty mount point before dropping setup authority. */
	if (write(setup, "1", 1) != 1 || close(setup) == -1) fail("complete filesystem setup");
	close(0);
	int input[2];
	if (pipe2(input, O_CLOEXEC) == -1 || input[0] != 0 || close(input[1]) == -1 ||
		fcntl(0, F_SETFD, 0) == -1) fail("install empty stdin");
	if (fcntl(3, F_SETFD, 0) == -1) fail("inherit role channel");
	limit_resource(RLIMIT_CORE, 0); limit_resource(RLIMIT_NOFILE, p->descriptors);
	limit_resource(RLIMIT_FSIZE, p->file_bytes); limit_resource(RLIMIT_MEMLOCK, 0);
	umask(0077);
	drop_identity(p);
	/* Credential changes clear PDEATHSIG. Reapply it after the final drop. */
	if (prctl(PR_SET_PDEATHSIG, SIGKILL) == -1) fail("bind final role lifetime");
	/* A held parent pidfd closes the credential-change race even though the
	 * new PID namespace cannot name its parent. Never pass this handle to Go. */
	struct pollfd parent = {.fd = parent_pidfd, .events = POLLIN};
	require(poll(&parent, 1, 0) == 0, "launcher parent must still be alive");
	/* close_range handles descriptors above the current soft limit as well. */
	if (syscall(SYS_close_range, 4, 8, 0) == -1 || syscall(SYS_close_range, 12, ~0U, 0) == -1) fail("close all setup descriptors");
	for (int fd = 9; fd <= 11; fd++) {
		if (p->channels & (1U << (fd - 9))) {
			if (fcntl(fd, F_SETFD, 0) == -1) fail("inherit verified role channel");
		} else close(fd);
	}
	if (chdir("/state") == -1) fail("fixed role working directory");
	int abi = landlock(p);
	char role[64], abi_env[64];
	const char *names[] = {"runtime", "writer", "command"};
	snprintf(role, sizeof(role), "ALMAGEST_LINUX_ROLE=%s", names[p->role]);
	snprintf(abi_env, sizeof(abi_env), "ALMAGEST_LANDLOCK_ABI=%d", abi);
	char *environment[] = {role, abi_env, "ALMAGEST_RETAINED_CAPS=0",
		p->channels & 1 ? "ALMAGEST_DATA_FD=9" : "ALMAGEST_DATA_FD=0",
		p->channels & 2 ? "ALMAGEST_DIAGNOSTICS_FD=10" : "ALMAGEST_DIAGNOSTICS_FD=0",
		p->channels & 4 ? "ALMAGEST_ADMIN_FD=11" : "ALMAGEST_ADMIN_FD=0",
		"ALMAGEST_BOOTSTRAP_FD=3", "GOMAXPROCS=2", "GODEBUG=netdns=go", NULL};
	char *arguments[] = {"/role", NULL};
	apply_seccomp(p);
	execve(arguments[0], arguments, environment);
	fail("execute restricted role");
}

int main(int argc, char **argv) {
	struct plan p = parse(argc, argv);
	require(getuid() == 0 && geteuid() == 0, "rootless profile is not qualified; no elevation is attempted");
	check_descriptors(&p);
	int parent_pidfd = (int)syscall(SYS_pidfd_open, getpid(), 0);
	if (parent_pidfd == -1) fail("hold launcher lifetime");
	/* Do this before creating filesystem state so absent setup authority
	 * cannot leave an empty mount directory behind. */
	if (unshare(CLONE_NEWPID) == -1) fail("create private PID namespace");
	char root[] = "/tmp/almagest-role-XXXXXX";
	if (!mkdtemp(root) || chdir(root) == -1) fail("unique role root");
	int setup[2];
	if (pipe2(setup, O_CLOEXEC) == -1) fail("setup completion pipe");
	/* Set the PID namespace only for restricted children. Trusted N stays in
	 * bootstrap and does not use this launcher. */
	pid_t child = fork();
	if (child == -1) fail("start role namespace");
	if (child == 0) {
		close(setup[0]);
		if (prctl(PR_SET_PDEATHSIG, SIGKILL) == -1) fail("bind launcher lifetime");
		/* The new namespace reports parent PID zero. The control peer and later
		 * supervisor own liveness; this wrapper is not a restart service. */
		run(&p, setup[1], parent_pidfd);
	}
	close(setup[1]);
	char complete;
	ssize_t n;
	do { n = read(setup[0], &complete, 1); } while (n < 0 && errno == EINTR);
	if (n < 0) fail("wait for filesystem setup");
	if (rmdir(root) == -1) fail("remove empty role mount point");
	/* The waiting launcher has no channels or writable authority after setup. */
	if (syscall(SYS_close_range, 3, ~0U, 0) == -1) fail("close parent descriptors");
	drop_identity(&p);
	int status;
	while (waitpid(child, &status, 0) == -1) { if (errno != EINTR) fail("wait for role"); }
	if (WIFSIGNALED(status)) fprintf(stderr, "restricted role terminated by signal %d\n", WTERMSIG(status));
	return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
}
