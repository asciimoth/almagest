#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/landlock.h>
#include <linux/securebits.h>
#include <netinet/in.h>
#include <sched.h>
#include <seccomp.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/eventfd.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#ifndef LANDLOCK_ACCESS_FS_REFER
#define LANDLOCK_ACCESS_FS_REFER (1ULL << 13)
#endif
#ifndef LANDLOCK_ACCESS_FS_TRUNCATE
#define LANDLOCK_ACCESS_FS_TRUNCATE (1ULL << 14)
#endif

static void fail(const char *operation) {
	fprintf(stderr, "linux-isolation launcher: %s: %s\n", operation, strerror(errno));
	exit(1);
}

static void make_dir(const char *path, mode_t mode) {
	if (mkdir(path, mode) == -1 && errno != EEXIST) {
		fail(path);
	}
}

static void copy_file(const char *source, const char *target) {
	int input = open(source, O_RDONLY | O_CLOEXEC);
	if (input == -1) {
		fail("open probe");
	}
	int output = open(target, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0555);
	if (output == -1) {
		fail("create private probe");
	}
	char buffer[65536];
	for (;;) {
		ssize_t count = read(input, buffer, sizeof(buffer));
		if (count == 0) {
			break;
		}
		if (count < 0) {
			fail("read probe");
		}
		for (ssize_t offset = 0; offset < count;) {
			ssize_t written = write(output, buffer + offset, (size_t)(count - offset));
			if (written < 0) {
				fail("write private probe");
			}
			offset += written;
		}
	}
	if (close(output) == -1 || close(input) == -1) {
		fail("close copied probe");
	}
}

static void prepare_filesystem(const char *probe) {
	const char *root = "/tmp/almagest-linux-isolation-root";
	make_dir(root, 0700);
	if (mount("tmpfs", root, "tmpfs", MS_NOSUID | MS_NODEV, "mode=0755,size=64m") == -1) {
		fail("mount private root");
	}
	char path[256];
	snprintf(path, sizeof(path), "%s/storage", root);
	make_dir(path, 0700);
	if (chown(path, 65534, 65534) == -1) {
		fail("own private storage");
	}
	snprintf(path, sizeof(path), "%s/tmp", root);
	make_dir(path, 0700);
	if (chown(path, 65534, 65534) == -1) {
		fail("own private temporary directory");
	}
	snprintf(path, sizeof(path), "%s/oldroot", root);
	make_dir(path, 0700);
	snprintf(path, sizeof(path), "%s/blocked", root);
	int blocked = open(path, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
	if (blocked == -1 || write(blocked, "blocked\n", 8) != 8 || close(blocked) == -1) {
		fail("create Landlock sentinel");
	}
	snprintf(path, sizeof(path), "%s/probe", root);
	copy_file(probe, path);

	if (chdir(root) == -1 || syscall(SYS_pivot_root, ".", "oldroot") == -1) {
		fail("pivot private root");
	}
	if (chdir("/") == -1 || umount2("/oldroot", MNT_DETACH) == -1 || rmdir("/oldroot") == -1) {
		fail("detach old root");
	}
}

static uint64_t landlock_rights_for_abi(int abi) {
	uint64_t rights = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_WRITE_FILE |
		LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
		LANDLOCK_ACCESS_FS_MAKE_CHAR | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_FIFO | LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM;
	if (abi >= 2) {
		rights |= LANDLOCK_ACCESS_FS_REFER;
	}
	if (abi >= 3) {
		rights |= LANDLOCK_ACCESS_FS_TRUNCATE;
	}
	return rights;
}

static void add_landlock_rule(int ruleset, const char *path, uint64_t rights) {
	int parent = open(path, O_PATH | O_CLOEXEC);
	if (parent == -1) {
		fail("open Landlock path");
	}
	struct landlock_path_beneath_attr rule = {
		.allowed_access = rights,
		.parent_fd = parent,
	};
	if (syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &rule, 0) == -1) {
		fail("add Landlock rule");
	}
	close(parent);
}

static int apply_landlock(void) {
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	if (abi < 3) {
		if (abi == -1) {
			fail("query Landlock ABI");
		}
		fprintf(stderr, "linux-isolation launcher: Landlock ABI %d lacks truncate mediation\n", abi);
		exit(1);
	}
	uint64_t handled = landlock_rights_for_abi(abi);
	struct landlock_ruleset_attr ruleset_attr = {.handled_access_fs = handled};
	int ruleset = (int)syscall(SYS_landlock_create_ruleset, &ruleset_attr,
				 sizeof(ruleset_attr), 0);
	if (ruleset == -1) {
		fail("create Landlock ruleset");
	}
	add_landlock_rule(ruleset, "/", LANDLOCK_ACCESS_FS_READ_DIR);
	add_landlock_rule(ruleset, "/probe", LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_READ_FILE);
	uint64_t writable = handled & ~LANDLOCK_ACCESS_FS_EXECUTE;
	add_landlock_rule(ruleset, "/storage", writable);
	add_landlock_rule(ruleset, "/tmp", writable);
	if (syscall(SYS_landlock_restrict_self, ruleset, 0) == -1) {
		fail("apply Landlock ruleset");
	}
	close(ruleset);
	return abi;
}

static void drop_capabilities_and_identity(void) {
	int last_cap = 63;
	FILE *file = fopen("/proc/sys/kernel/cap_last_cap", "re");
	if (file != NULL) {
		if (fscanf(file, "%d", &last_cap) != 1) {
			last_cap = 63;
		}
		fclose(file);
	}
	for (int capability = 0; capability <= last_cap; capability++) {
		if (capability != CAP_SETPCAP && prctl(PR_CAPBSET_DROP, capability, 0, 0, 0) == -1 && errno != EINVAL) {
			fail("drop capability from bounding set");
		}
	}
	if (prctl(PR_CAPBSET_DROP, CAP_SETPCAP, 0, 0, 0) == -1 && errno != EINVAL) {
		fail("drop CAP_SETPCAP from bounding set");
	}
	if (setgroups(0, NULL) == -1) {
		fail("clear supplementary groups");
	}
	unsigned int securebits = SECBIT_NOROOT | SECBIT_NOROOT_LOCKED |
		SECBIT_NO_SETUID_FIXUP | SECBIT_NO_SETUID_FIXUP_LOCKED |
		SECBIT_KEEP_CAPS_LOCKED | SECBIT_NO_CAP_AMBIENT_RAISE |
		SECBIT_NO_CAP_AMBIENT_RAISE_LOCKED;
	if (prctl(PR_SET_SECUREBITS, securebits, 0, 0, 0) == -1) {
		fail("lock securebits");
	}
	if (setresgid(65534, 65534, 65534) == -1 || setresuid(65534, 65534, 65534) == -1) {
		fail("drop UID and GID");
	}
	struct __user_cap_header_struct header = {
		.version = _LINUX_CAPABILITY_VERSION_3,
		.pid = 0,
	};
	struct __user_cap_data_struct data[2] = {{0}};
	if (syscall(SYS_capset, &header, data) == -1) {
		fail("clear capability sets");
	}
	if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) == -1 && errno != EINVAL) {
		fail("clear ambient capabilities");
	}
}

static void allow_syscall(scmp_filter_ctx filter, int syscall_number) {
	if (seccomp_rule_add(filter, SCMP_ACT_ALLOW, syscall_number, 0) < 0) {
		errno = EINVAL;
		fail("add seccomp allow rule");
	}
}

static void apply_seccomp(void) {
	scmp_filter_ctx filter = seccomp_init(SCMP_ACT_ERRNO(EPERM));
	if (filter == NULL) {
		fail("create seccomp filter");
	}
	const int allowed[] = {
		SCMP_SYS(read), SCMP_SYS(write), SCMP_SYS(readv), SCMP_SYS(writev),
		SCMP_SYS(pread64), SCMP_SYS(pwrite64), SCMP_SYS(close), SCMP_SYS(lseek),
		SCMP_SYS(fcntl), SCMP_SYS(dup), SCMP_SYS(dup2), SCMP_SYS(dup3),
		SCMP_SYS(mmap), SCMP_SYS(mprotect), SCMP_SYS(munmap), SCMP_SYS(mremap),
		SCMP_SYS(madvise), SCMP_SYS(brk), SCMP_SYS(rt_sigaction),
		SCMP_SYS(rt_sigprocmask), SCMP_SYS(rt_sigreturn), SCMP_SYS(sigaltstack),
		SCMP_SYS(futex), SCMP_SYS(sched_yield), SCMP_SYS(sched_getaffinity),
		SCMP_SYS(nanosleep), SCMP_SYS(clock_nanosleep), SCMP_SYS(clock_gettime),
		SCMP_SYS(gettimeofday), SCMP_SYS(epoll_create1), SCMP_SYS(epoll_ctl),
		SCMP_SYS(epoll_pwait), SCMP_SYS(epoll_wait), SCMP_SYS(eventfd2),
		SCMP_SYS(pipe2), SCMP_SYS(openat), SCMP_SYS(newfstatat), SCMP_SYS(fstat),
		SCMP_SYS(statx), SCMP_SYS(getdents64), SCMP_SYS(readlinkat),
		SCMP_SYS(mkdirat), SCMP_SYS(unlinkat), SCMP_SYS(renameat),
		SCMP_SYS(renameat2), SCMP_SYS(fsync), SCMP_SYS(fdatasync),
		SCMP_SYS(truncate), SCMP_SYS(ftruncate), SCMP_SYS(fallocate), SCMP_SYS(umask),
		SCMP_SYS(getrandom), SCMP_SYS(prlimit64), SCMP_SYS(rseq),
		SCMP_SYS(getrusage), SCMP_SYS(poll), SCMP_SYS(ppoll),
		SCMP_SYS(set_robust_list), SCMP_SYS(arch_prctl), SCMP_SYS(uname),
		SCMP_SYS(getpid), SCMP_SYS(getppid), SCMP_SYS(gettid), SCMP_SYS(getuid),
		SCMP_SYS(geteuid), SCMP_SYS(getgid), SCMP_SYS(getegid), SCMP_SYS(getgroups), SCMP_SYS(tgkill),
		SCMP_SYS(tkill), SCMP_SYS(capget), SCMP_SYS(sendto), SCMP_SYS(recvfrom),
		SCMP_SYS(sendmsg), SCMP_SYS(recvmsg), SCMP_SYS(getsockname),
		SCMP_SYS(getpeername), SCMP_SYS(seccomp), SCMP_SYS(execve),
		SCMP_SYS(exit), SCMP_SYS(exit_group), SCMP_SYS(restart_syscall),
	};
	for (size_t index = 0; index < sizeof(allowed) / sizeof(allowed[0]); index++) {
		allow_syscall(filter, allowed[index]);
	}
#ifdef __NR_futex_waitv
	allow_syscall(filter, SCMP_SYS(futex_waitv));
#endif
	/* Go's amd64 runtime adds CLONE_SETTLS to its exact OS-thread flags. */
	if (seccomp_rule_add(filter, SCMP_ACT_ALLOW, SCMP_SYS(clone), 1,
				 SCMP_A0(SCMP_CMP_EQ, 0xd0f00)) < 0) {
		errno = EINVAL;
		fail("add Go thread clone rule");
	}
	if (seccomp_rule_add(filter, SCMP_ACT_ERRNO(ENOSYS), SCMP_SYS(clone3), 0) < 0) {
		errno = EINVAL;
		fail("add clone3 fallback rule");
	}
	if (seccomp_rule_add(filter, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 1,
				 SCMP_A0(SCMP_CMP_EQ, PR_GET_NO_NEW_PRIVS)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 1,
				 SCMP_A0(SCMP_CMP_EQ, PR_GET_SECCOMP)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 1,
				 SCMP_A0(SCMP_CMP_EQ, PR_GET_SECUREBITS)) < 0 ||
		seccomp_rule_add(filter, SCMP_ACT_ALLOW, SCMP_SYS(prctl), 1,
				 SCMP_A0(SCMP_CMP_EQ, PR_CAPBSET_READ)) < 0) {
		errno = EINVAL;
		fail("add read-only prctl rules");
	}
	if (seccomp_attr_set(filter, SCMP_FLTATR_CTL_NNP, 0) < 0 || seccomp_load(filter) < 0) {
		errno = EINVAL;
		fail("load seccomp filter");
	}
	seccomp_release(filter);
}

static void close_unneeded_descriptors(int preserved) {
	struct rlimit limit;
	if (getrlimit(RLIMIT_NOFILE, &limit) == -1) {
		fail("get descriptor limit");
	}
	for (int fd = 3; fd < (int)limit.rlim_cur; fd++) {
		if (fd != preserved) {
			close(fd);
		}
	}
	if (preserved != 3 && dup2(preserved, 3) == -1) {
		fail("move private channel");
	}
	if (preserved != 3) {
		close(preserved);
	}
	int flags = fcntl(3, F_GETFD);
	if (flags == -1 || fcntl(3, F_SETFD, flags & ~FD_CLOEXEC) == -1) {
		fail("inherit private channel");
	}
}

static void close_unneeded_benchmark_descriptors(int data, int control) {
	struct rlimit limit;
	if (getrlimit(RLIMIT_NOFILE, &limit) == -1) fail("get benchmark descriptor limit");
	for (int fd = 3; fd < (int)limit.rlim_cur; fd++) {
		if (fd != data && fd != control) close(fd);
	}
	if (data != 3 && dup2(data, 3) == -1) fail("move benchmark data channel");
	if (control != 4 && dup2(control, 4) == -1) fail("move benchmark control channel");
	if (data != 3 && data != 4) close(data);
	if (control != 3 && control != 4) close(control);
	for (int fd = 3; fd <= 4; fd++) {
		int flags = fcntl(fd, F_GETFD);
		if (flags == -1 || fcntl(fd, F_SETFD, flags & ~FD_CLOEXEC) == -1) fail("inherit benchmark channel");
	}
}

static void run_restricted_child(const char *probe, int channel) {
	prepare_filesystem(probe);
	close_unneeded_descriptors(channel);
	drop_capabilities_and_identity();
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) {
		fail("set no_new_privs");
	}
	int abi = apply_landlock();
	char abi_text[16];
	snprintf(abi_text, sizeof(abi_text), "%d", abi);
	if (setenv("ALMAGEST_LANDLOCK_ABI", abi_text, 1) == -1) {
		fail("set Landlock ABI environment");
	}
	apply_seccomp();
	char *const argv[] = {"/probe", NULL};
	execve(argv[0], argv, environ);
	fail("execute Go probe");
}

static void run_restricted_benchmark_child(const char *probe, int data, int control) {
	prepare_filesystem(probe);
	close_unneeded_benchmark_descriptors(data, control);
	drop_capabilities_and_identity();
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) == -1) fail("set benchmark no_new_privs");
	int abi = apply_landlock();
	char abi_text[16]; snprintf(abi_text, sizeof(abi_text), "%d", abi);
	if (setenv("ALMAGEST_LANDLOCK_ABI", abi_text, 1) == -1) fail("set benchmark Landlock ABI");
	apply_seccomp();
	char *const argv[] = {"/probe", NULL};
	execve(argv[0], argv, environ);
	fail("execute benchmark Go probe");
}

static void send_all(int fd, unsigned char *buffer, size_t length) {
	while (length > 0) {
		ssize_t count = send(fd, buffer, length, MSG_NOSIGNAL);
		if (count <= 0) fail("send native benchmark data");
		buffer += count;
		length -= (size_t)count;
	}
}

static void receive_all(int fd, unsigned char *buffer, size_t length) {
	while (length > 0) {
		ssize_t count = recv(fd, buffer, length, 0);
		if (count <= 0) fail("receive native benchmark data");
		buffer += count;
		length -= (size_t)count;
	}
}

struct native_pair {
	int client;
	int server;
	bool datagram;
	bool connected;
	struct sockaddr_storage client_address;
	struct sockaddr_storage server_address;
	socklen_t client_length;
	socklen_t server_length;
};

static struct native_pair make_native_pair(int family, int type, bool connected) {
	struct native_pair pair = {.client = -1, .server = -1, .datagram = type == SOCK_DGRAM, .connected = connected};
	int listener = socket(family, type | SOCK_CLOEXEC, 0);
	if (listener == -1) fail("create native benchmark listener");
	if (family == AF_INET) {
		struct sockaddr_in address = {.sin_family = AF_INET, .sin_addr.s_addr = htonl(INADDR_LOOPBACK)};
		if (bind(listener, (struct sockaddr *)&address, sizeof(address)) == -1) fail("bind native IPv4 benchmark listener");
		if (type == SOCK_STREAM && listen(listener, 1) == -1) fail("listen on native IPv4 benchmark socket");
		socklen_t length = sizeof(address);
		if (getsockname(listener, (struct sockaddr *)&address, &length) == -1) fail("inspect native IPv4 benchmark listener");
		memcpy(&pair.server_address, &address, length); pair.server_length = length;
		pair.client = socket(family, type | SOCK_CLOEXEC, 0);
		if (pair.client == -1) fail("create native IPv4 benchmark client");
		if (type == SOCK_STREAM || connected) {
			if (connect(pair.client, (struct sockaddr *)&address, length) == -1) fail("connect native IPv4 benchmark client");
		} else {
			struct sockaddr_in client_bind = {.sin_family = AF_INET, .sin_addr.s_addr = htonl(INADDR_LOOPBACK)};
			if (bind(pair.client, (struct sockaddr *)&client_bind, sizeof(client_bind)) == -1) fail("bind native IPv4 benchmark client");
		}
		if (type == SOCK_DGRAM) {
			struct sockaddr_in client_address;
			socklen_t client_length = sizeof(client_address);
			if (getsockname(pair.client, (struct sockaddr *)&client_address, &client_length) == -1) fail("inspect native IPv4 benchmark client");
			memcpy(&pair.client_address, &client_address, client_length); pair.client_length = client_length;
			if (connected && connect(listener, (struct sockaddr *)&client_address, client_length) == -1) fail("connect native IPv4 benchmark server");
		}
	} else {
		struct sockaddr_in6 address = {.sin6_family = AF_INET6, .sin6_addr = IN6ADDR_LOOPBACK_INIT};
		if (bind(listener, (struct sockaddr *)&address, sizeof(address)) == -1) fail("bind native IPv6 benchmark listener");
		if (type == SOCK_STREAM && listen(listener, 1) == -1) fail("listen on native IPv6 benchmark socket");
		socklen_t length = sizeof(address);
		if (getsockname(listener, (struct sockaddr *)&address, &length) == -1) fail("inspect native IPv6 benchmark listener");
		memcpy(&pair.server_address, &address, length); pair.server_length = length;
		pair.client = socket(family, type | SOCK_CLOEXEC, 0);
		if (pair.client == -1) fail("create native IPv6 benchmark client");
		if (type == SOCK_STREAM || connected) {
			if (connect(pair.client, (struct sockaddr *)&address, length) == -1) fail("connect native IPv6 benchmark client");
		} else {
			struct sockaddr_in6 client_bind = {.sin6_family = AF_INET6, .sin6_addr = IN6ADDR_LOOPBACK_INIT};
			if (bind(pair.client, (struct sockaddr *)&client_bind, sizeof(client_bind)) == -1) fail("bind native IPv6 benchmark client");
		}
		if (type == SOCK_DGRAM) {
			struct sockaddr_in6 client_address;
			socklen_t client_length = sizeof(client_address);
			if (getsockname(pair.client, (struct sockaddr *)&client_address, &client_length) == -1) fail("inspect native IPv6 benchmark client");
			memcpy(&pair.client_address, &client_address, client_length); pair.client_length = client_length;
			if (connected && connect(listener, (struct sockaddr *)&client_address, client_length) == -1) fail("connect native IPv6 benchmark server");
		}
	}
	if (type == SOCK_STREAM) {
		pair.server = accept4(listener, NULL, NULL, SOCK_CLOEXEC);
		close(listener);
		if (pair.server == -1) fail("accept native benchmark connection");
	} else {
		pair.server = listener;
	}
	return pair;
}

static void native_roundtrip(struct native_pair *pair, unsigned char *buffer, size_t length) {
	if (pair->datagram) {
		if (!pair->connected) {
			if (sendto(pair->client, buffer, length, 0, (struct sockaddr *)&pair->server_address, pair->server_length) != (ssize_t)length ||
				recv(pair->server, buffer, length, 0) != (ssize_t)length ||
				sendto(pair->server, buffer, length, 0, (struct sockaddr *)&pair->client_address, pair->client_length) != (ssize_t)length ||
				recv(pair->client, buffer, length, 0) != (ssize_t)length) fail("exchange native unconnected datagram benchmark data");
			return;
		}
		if (send(pair->client, buffer, length, 0) != (ssize_t)length ||
			recv(pair->server, buffer, length, 0) != (ssize_t)length ||
			send(pair->server, buffer, length, 0) != (ssize_t)length ||
			recv(pair->client, buffer, length, 0) != (ssize_t)length) fail("exchange native datagram benchmark data");
		return;
	}
	send_all(pair->client, buffer, length);
	receive_all(pair->server, buffer, length);
	send_all(pair->server, buffer, length);
	receive_all(pair->client, buffer, length);
}

static uint32_t read_u32(const unsigned char *value) {
	return ((uint32_t)value[0] << 24) | ((uint32_t)value[1] << 16) | ((uint32_t)value[2] << 8) | (uint32_t)value[3];
}

static void write_u64(unsigned char *value, uint64_t number) {
	for (int index = 7; index >= 0; index--) { value[index] = (unsigned char)(number & 0xff); number >>= 8; }
}

static uint64_t monotonic_nanoseconds(void) {
	struct timespec value;
	if (clock_gettime(CLOCK_MONOTONIC, &value) == -1) fail("read benchmark clock");
	return (uint64_t)value.tv_sec * 1000000000ULL + (uint64_t)value.tv_nsec;
}

static uint64_t timeval_nanoseconds(struct timeval value) {
	return (uint64_t)value.tv_sec * 1000000000ULL + (uint64_t)value.tv_usec * 1000ULL;
}

static void return_broker_resources(int channel, unsigned char *frame) {
	struct rusage usage;
	if (getrusage(RUSAGE_SELF, &usage) == -1) fail("read broker resource usage");
	write_u64(frame + 10, timeval_nanoseconds(usage.ru_utime) + timeval_nanoseconds(usage.ru_stime));
	write_u64(frame + 18, (uint64_t)usage.ru_nvcsw);
	write_u64(frame + 26, (uint64_t)usage.ru_nivcsw);
	write_u64(frame + 34, (uint64_t)usage.ru_maxrss * 1024ULL);
	write_u64(frame + 42, monotonic_nanoseconds());
	if (send(channel, frame, 50, MSG_NOSIGNAL) != 50) fail("return broker resource usage");
}

static void send_descriptors(int channel, unsigned char *frame, size_t frame_length, int *descriptors, size_t descriptor_count) {
	char control[CMSG_SPACE(sizeof(int) * 3)] = {0};
	struct iovec iov = {.iov_base = frame, .iov_len = frame_length};
	struct msghdr message = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = control,
		.msg_controllen = CMSG_SPACE(sizeof(int) * descriptor_count)};
	struct cmsghdr *header = CMSG_FIRSTHDR(&message);
	header->cmsg_level = SOL_SOCKET;
	header->cmsg_type = SCM_RIGHTS;
	header->cmsg_len = CMSG_LEN(sizeof(int) * descriptor_count);
	memcpy(CMSG_DATA(header), descriptors, sizeof(int) * descriptor_count);
	if (sendmsg(channel, &message, MSG_NOSIGNAL) != (ssize_t)frame_length) fail("send benchmark descriptors");
}

static void run_shared_memory_candidate(int channel, struct native_pair *pair, unsigned char *frame, uint32_t iterations) {
	int memory = (int)syscall(SYS_memfd_create, "almagest-data-path", MFD_CLOEXEC);
	if (memory == -1 || ftruncate(memory, 65536) == -1) fail("create shared benchmark memory");
	unsigned char *mapping = mmap(NULL, 65536, PROT_READ | PROT_WRITE, MAP_SHARED, memory, 0);
	if (mapping == MAP_FAILED) fail("map shared benchmark memory");
	int request = eventfd(0, EFD_CLOEXEC);
	int response = eventfd(0, EFD_CLOEXEC);
	if (request == -1 || response == -1) fail("create shared benchmark notification");
	int descriptors[] = {memory, request, response};
	send_descriptors(channel, frame, 10, descriptors, 3);
	for (uint32_t index = 0; index < iterations; index++) {
		uint64_t signal;
		if (read(request, &signal, sizeof(signal)) != sizeof(signal)) fail("wait for shared benchmark request");
		uint32_t length;
		memcpy(&length, mapping, sizeof(length));
		if (length > 65532) { errno = EMSGSIZE; fail("validate shared benchmark length"); }
		native_roundtrip(pair, mapping + 4, length);
		signal = 1;
		if (write(response, &signal, sizeof(signal)) != sizeof(signal)) fail("notify shared benchmark response");
	}
	munmap(mapping, 65536);
	close(memory);
	close(request);
	close(response);
}

static void run_direct_candidate(int channel, struct native_pair *pair, unsigned char *frame, uint32_t payload_length, uint32_t iterations) {
	int descriptor = pair->client;
	send_descriptors(channel, frame, 10, &descriptor, 1);
	unsigned char payload[65536];
	for (uint32_t index = 0; index < iterations; index++) {
		if (pair->datagram) {
			if (recv(pair->server, payload, payload_length, 0) != (ssize_t)payload_length ||
				send(pair->server, payload, payload_length, 0) != (ssize_t)payload_length) fail("service direct datagram benchmark");
		} else {
			receive_all(pair->server, payload, payload_length);
			send_all(pair->server, payload, payload_length);
		}
	}
}

static void run_benchmark_broker(int channel) {
	struct native_pair pairs[] = {
		make_native_pair(AF_INET, SOCK_STREAM, true), make_native_pair(AF_INET6, SOCK_STREAM, true),
		make_native_pair(AF_INET, SOCK_DGRAM, true), make_native_pair(AF_INET6, SOCK_DGRAM, true),
		make_native_pair(AF_INET, SOCK_DGRAM, false), make_native_pair(AF_INET6, SOCK_DGRAM, false),
	};
	unsigned char frame[65536];
	for (;;) {
		ssize_t count = recv(channel, frame, sizeof(frame), 0);
		if (count == 0) break;
		if (count < 10) { errno = EPROTO; fail("receive bounded benchmark frame"); }
		unsigned int operation = frame[0];
		if (operation == 0xff) break;
		if (operation == 0xe0) { return_broker_resources(channel, frame); continue; }
		unsigned int native_index = operation & 0x0f;
		if (native_index < 1 || native_index > 6) { errno = EPROTO; fail("validate benchmark operation"); }
		uint32_t payload_length = read_u32(frame + 2);
		uint32_t iterations = read_u32(frame + 6);
		if (payload_length > (uint32_t)(count - 10) || iterations > 1000000) { errno = EMSGSIZE; fail("validate benchmark frame limits"); }
		if ((operation & 0xf0) == 0x20) {
			run_shared_memory_candidate(channel, &pairs[native_index - 1], frame, iterations);
			continue;
		}
		if ((operation & 0xf0) == 0x30) {
			if (!pairs[native_index - 1].connected) { errno = EPERM; fail("direct unconnected UDP is outside experiment policy"); }
			run_direct_candidate(channel, &pairs[native_index - 1], frame, payload_length, iterations);
			continue;
		}
		if ((operation & 0xf0) == 0x10) {
			uint64_t started = monotonic_nanoseconds();
			for (uint32_t index = 0; index < iterations; index++) native_roundtrip(&pairs[native_index - 1], frame + 10, payload_length);
			write_u64(frame + 10, monotonic_nanoseconds() - started);
			if (send(channel, frame, 18, MSG_NOSIGNAL) != 18) fail("return direct benchmark result");
			continue;
		}
		native_roundtrip(&pairs[native_index - 1], frame + 10, payload_length);
		if (send(channel, frame, (size_t)count, MSG_NOSIGNAL) != count) fail("return relay benchmark frame");
	}
	for (size_t index = 0; index < sizeof(pairs) / sizeof(pairs[0]); index++) { close(pairs[index].client); close(pairs[index].server); }
}

static int run_benchmark(const char *probe) {
	int channels[2];
	if (socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, channels) == -1) fail("create benchmark channel");
	int controls[2];
	if (socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, controls) == -1) fail("create benchmark control channel");
	pid_t child = fork();
	if (child == -1) fail("start benchmark runtime");
	if (child == 0) {
		close(channels[0]);
		close(controls[0]);
		if (unshare(CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWIPC | CLONE_NEWUTS | CLONE_NEWPID) == -1) fail("create benchmark runtime namespaces");
		if (sethostname("almagest-runtime", strlen("almagest-runtime")) == -1) fail("set benchmark runtime hostname");
		pid_t namespace_child = fork();
		if (namespace_child == -1) fail("enter benchmark PID namespace");
		if (namespace_child == 0) run_restricted_benchmark_child(probe, channels[1], controls[1]);
		close(channels[1]);
		close(controls[1]);
		int namespace_status;
		if (waitpid(namespace_child, &namespace_status, 0) == -1) fail("wait for benchmark runtime namespace");
		_exit(WIFEXITED(namespace_status) ? WEXITSTATUS(namespace_status) : 1);
	}
	close(channels[1]);
	close(controls[1]);
	pid_t control_broker = fork();
	if (control_broker == -1) fail("start benchmark control broker");
	if (control_broker == 0) {
		close(channels[0]);
		unsigned char message[256];
		for (;;) {
			ssize_t count = recv(controls[0], message, sizeof(message), 0);
			if (count == 0) _exit(0);
			if (count < 0 || send(controls[0], message, (size_t)count, MSG_NOSIGNAL) != count) _exit(1);
		}
	}
	close(controls[0]);
	run_benchmark_broker(channels[0]);
	close(channels[0]);
	int status;
	if (waitpid(child, &status, 0) == -1) fail("wait for benchmark runtime");
	int control_status;
	if (waitpid(control_broker, &control_status, 0) == -1) fail("wait for benchmark control broker");
	return WIFEXITED(status) && WEXITSTATUS(status) == 0 && WIFEXITED(control_status) && WEXITSTATUS(control_status) == 0 ? 0 : 1;
}

static int run_go_broker(const char *broker, const char *probe) {
	int channels[2];
	if (socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, channels) == -1) fail("create matcher benchmark channel");
	pid_t child = fork();
	if (child == -1) fail("start matcher benchmark runtime");
	if (child == 0) {
		close(channels[0]);
		if (unshare(CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWIPC | CLONE_NEWUTS | CLONE_NEWPID) == -1) fail("create matcher runtime namespaces");
		if (sethostname("almagest-runtime", strlen("almagest-runtime")) == -1) fail("set matcher runtime hostname");
		pid_t namespace_child = fork();
		if (namespace_child == -1) fail("enter matcher PID namespace");
		if (namespace_child == 0) run_restricted_child(probe, channels[1]);
		close(channels[1]);
		int namespace_status;
		if (waitpid(namespace_child, &namespace_status, 0) == -1) fail("wait for matcher runtime namespace");
		_exit(WIFEXITED(namespace_status) ? WEXITSTATUS(namespace_status) : 1);
	}
	close(channels[1]);
	if (dup2(channels[0], 3) == -1) fail("map matcher broker channel");
	if (channels[0] != 3) close(channels[0]);
	int flags = fcntl(3, F_GETFD);
	if (flags == -1 || fcntl(3, F_SETFD, flags & ~FD_CLOEXEC) == -1) fail("inherit matcher broker channel");
	char child_text[32];
	snprintf(child_text, sizeof(child_text), "%d", child);
	if (setenv("ALMAGEST_RESTRICTED_CHILD_PID", child_text, 1) == -1) fail("set matcher child PID");
	execl(broker, broker, NULL);
	fail("execute matcher benchmark broker");
	return 1;
}

int main(int argc, char **argv) {
	if (argc == 4 && strcmp(argv[1], "--broker") == 0) {
		if (geteuid() != 0) { fprintf(stderr, "linux-isolation launcher: bootstrap requires container root\n"); return 1; }
		return run_go_broker(argv[2], argv[3]);
	}
	if (argc == 3 && strcmp(argv[1], "--benchmark") == 0) {
		if (geteuid() != 0) { fprintf(stderr, "linux-isolation launcher: bootstrap requires container root\n"); return 1; }
		return run_benchmark(argv[2]);
	}
	if (argc != 2) {
		fprintf(stderr, "usage: %s [--benchmark PROBE | --broker BROKER PROBE]\n", argv[0]);
		return 2;
	}
	if (geteuid() != 0) {
		fprintf(stderr, "linux-isolation launcher: bootstrap requires container root\n");
		return 1;
	}
	if (mount(NULL, "/", NULL, MS_REC | MS_PRIVATE, NULL) == -1) {
		fail("make mount propagation private");
	}
	if (unshare(CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWIPC | CLONE_NEWUTS | CLONE_NEWPID) == -1) {
		fail("create namespaces");
	}
	if (sethostname("almagest-runtime", strlen("almagest-runtime")) == -1) {
		fail("set private hostname");
	}
	int channels[2];
	if (socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, channels) == -1) {
		fail("create private channel");
	}
	pid_t child = fork();
	if (child == -1) {
		fail("enter PID namespace");
	}
	if (child == 0) {
		close(channels[0]);
		run_restricted_child(argv[1], channels[1]);
	}
	close(channels[1]);
	const char token[] = "broker-channel-token";
	if (send(channels[0], token, sizeof(token), MSG_NOSIGNAL) != (ssize_t)sizeof(token)) {
		fail("send private channel token");
	}
	close(channels[0]);
	int status;
	if (waitpid(child, &status, 0) == -1) {
		fail("wait for probe");
	}
	if (WIFEXITED(status)) {
		return WEXITSTATUS(status);
	}
	fprintf(stderr, "linux-isolation launcher: probe terminated by signal %d\n", WTERMSIG(status));
	return 1;
}
