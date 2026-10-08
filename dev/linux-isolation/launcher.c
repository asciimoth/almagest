#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/landlock.h>
#include <linux/securebits.h>
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
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
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

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: %s PROBE\n", argv[0]);
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
