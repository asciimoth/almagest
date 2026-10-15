#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/landlock.h>
#include <linux/seccomp.h>
#include <sched.h>
#include <signal.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/ptrace.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/uio.h>
#include <sys/wait.h>
#include <unistd.h>

#ifndef LANDLOCK_ACCESS_FS_REFER
#define LANDLOCK_ACCESS_FS_REFER (1ULL << 13)
#endif
#ifndef LANDLOCK_ACCESS_FS_TRUNCATE
#define LANDLOCK_ACCESS_FS_TRUNCATE (1ULL << 14)
#endif
#ifndef SYS_fchmodat2
#define SYS_fchmodat2 452
#endif

static void result(const char *group, const char *name, long value, int error) {
	printf("RESULT group=%s test=%s value=%ld errno=%d(%s)\n",
		group, name, value, error, error ? strerror(error) : "none");
}

static void status_lines(const char *prefix) {
	FILE *file = fopen("/proc/self/status", "re");
	if (!file) {
		result("identity", "open_status", -1, errno);
		return;
	}
	char line[512];
	while (fgets(line, sizeof(line), file)) {
		if (!strncmp(line, "Uid:", 4) || !strncmp(line, "Gid:", 4) ||
			!strncmp(line, "Cap", 3) || !strncmp(line, "NoNewPrivs:", 11) ||
			!strncmp(line, "Seccomp:", 8) || !strncmp(line, "Seccomp_filters:", 16)) {
			line[strcspn(line, "\n")] = '\0';
			printf("STATUS phase=%s %s\n", prefix, line);
		}
	}
	fclose(file);
}

static void seccomp_probe(void) {
	status_lines("before_filter");
	struct sock_filter filter[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, arch)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, AUDIT_ARCH_X86_64, 0, 3),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_getppid, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EACCES),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = (unsigned short)(sizeof(filter) / sizeof(filter[0])), .filter = filter};
	errno = 0;
	long nnp = prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0);
	result("seccomp", "set_no_new_privs", nnp, errno);
	errno = 0;
	long installed = prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program);
	result("seccomp", "install_filter", installed, errno);
	errno = 0;
	long denied = syscall(__NR_getppid);
	result("seccomp", "denied_getppid", denied, errno);
	errno = 0;
	long allowed = syscall(__NR_getpid);
	result("seccomp", "allowed_getpid", allowed, errno);
	status_lines("after_filter");
}

static int install_errno_filter(int syscall_number, int error_number) {
	struct sock_filter filter[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, (uint32_t)syscall_number, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (uint32_t)error_number),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = (unsigned short)(sizeof(filter) / sizeof(filter[0])), .filter = filter};
	return prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program);
}

static int write_text(const char *path, const char *text) {
	int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
	if (fd == -1) return -1;
	ssize_t expected = (ssize_t)strlen(text);
	ssize_t n = write(fd, text, (size_t)expected);
	int saved = errno;
	if (close(fd) == -1 && n == expected) return -1;
	errno = saved;
	return n == expected ? 0 : -1;
}

static uint64_t landlock_rights(int abi) {
	uint64_t rights = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_WRITE_FILE |
		LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
		LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
		LANDLOCK_ACCESS_FS_MAKE_CHAR | LANDLOCK_ACCESS_FS_MAKE_DIR |
		LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
		LANDLOCK_ACCESS_FS_MAKE_FIFO | LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		LANDLOCK_ACCESS_FS_MAKE_SYM;
	if (abi >= 2) rights |= LANDLOCK_ACCESS_FS_REFER;
	if (abi >= 3) rights |= LANDLOCK_ACCESS_FS_TRUNCATE;
	return rights;
}

static void landlock_probe(void) {
	char base[128];
	snprintf(base, sizeof(base), "/tmp/almagest-rootless-%ld", (long)getpid());
	char allowed[160], denied[160], allow_file[192], deny_file[192], new_file[192];
	snprintf(allowed, sizeof(allowed), "%s/allowed", base);
	snprintf(denied, sizeof(denied), "%s/denied", base);
	snprintf(allow_file, sizeof(allow_file), "%s/readable", allowed);
	snprintf(deny_file, sizeof(deny_file), "%s/secret", denied);
	snprintf(new_file, sizeof(new_file), "%s/new", denied);
	if (mkdir(base, 0700) || mkdir(allowed, 0700) || mkdir(denied, 0700) ||
		write_text(allow_file, "allowed")) {
		result("landlock", "fixture_setup", -1, errno);
		return;
	}
	pid_t broker = fork();
	if (broker == 0) _exit(write_text(deny_file, "peer-owned-metadata") == 0 ? 0 : 1);
	int broker_status = 0;
	waitpid(broker, &broker_status, 0);
	result("landlock", "same_uid_broker_file_setup",
		WIFEXITED(broker_status) ? WEXITSTATUS(broker_status) : -1, 0);
	int held_file = open(deny_file, O_RDONLY | O_CLOEXEC);
	int held_dir = open(denied, O_PATH | O_DIRECTORY | O_CLOEXEC);
	errno = 0;
	int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
	result("landlock", "abi", abi, errno);
	if (abi < 1) return;
	uint64_t rights = landlock_rights(abi);
	struct landlock_ruleset_attr ruleset_attr = {.handled_access_fs = rights};
	errno = 0;
	int ruleset = (int)syscall(SYS_landlock_create_ruleset, &ruleset_attr, sizeof(ruleset_attr), 0);
	result("landlock", "create_ruleset", ruleset, errno);
	if (ruleset == -1) return;
	int allow_fd = open(allowed, O_PATH | O_CLOEXEC);
	struct landlock_path_beneath_attr rule = {.allowed_access = rights, .parent_fd = allow_fd};
	errno = 0;
	long added = syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &rule, 0);
	result("landlock", "add_allowed_rule", added, errno);
	close(allow_fd);
	errno = 0;
	long restricted = syscall(SYS_landlock_restrict_self, ruleset, 0);
	result("landlock", "restrict_self", restricted, errno);
	close(ruleset);

	errno = 0;
	int fd = open(allow_file, O_RDONLY | O_CLOEXEC);
	result("landlock", "allowed_open", fd >= 0 ? 0 : -1, errno);
	if (fd >= 0) close(fd);
	errno = 0;
	fd = open(deny_file, O_RDONLY | O_CLOEXEC);
	result("landlock", "denied_open", fd >= 0 ? 0 : -1, errno);
	if (fd >= 0) close(fd);
	errno = 0;
	fd = openat(held_dir, "secret", O_RDONLY | O_CLOEXEC);
	result("landlock", "denied_openat_held_dir", fd >= 0 ? 0 : -1, errno);
	if (fd >= 0) close(fd);
	char byte = 0;
	errno = 0;
	ssize_t n = read(held_file, &byte, 1);
	result("landlock", "held_file_read", n, errno);
	errno = 0;
	int duplicate = fcntl(held_file, F_DUPFD_CLOEXEC, 20);
	result("landlock", "held_file_fcntl_duplicate", duplicate >= 0 ? 0 : -1, errno);
	if (duplicate >= 0) close(duplicate);
	struct stat st;
	errno = 0;
	long stat_result = stat(deny_file, &st);
	result("landlock", "denied_path_stat_metadata", stat_result, errno);
	errno = 0;
	long chmod_result = syscall(SYS_fchmodat, AT_FDCWD, deny_file, 0400);
	result("landlock", "unrestricted_same_uid_broker_fchmodat", chmod_result, errno);
	errno = 0;
	long chmod_filter = install_errno_filter(SYS_fchmodat, EPERM);
	result("seccomp", "install_fchmodat_filter", chmod_filter, errno);
	errno = 0;
	chmod_result = syscall(SYS_fchmodat, AT_FDCWD, deny_file, 0600);
	result("seccomp", "denied_fchmodat_after_landlock", chmod_result, errno);
	errno = 0;
	fd = open(new_file, O_WRONLY | O_CREAT | O_CLOEXEC, 0600);
	result("landlock", "denied_create", fd >= 0 ? 0 : -1, errno);
	if (fd >= 0) close(fd);
	close(held_file);
	close(held_dir);
}

static int copy_file(const char *source_path, const char *target) {
	int source = open(source_path, O_RDONLY | O_CLOEXEC);
	int output = open(target, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0700);
	if (source == -1 || output == -1) return -1;
	char buffer[65536];
	for (;;) {
		ssize_t n = read(source, buffer, sizeof(buffer));
		if (n == 0) break;
		if (n < 0) return -1;
		for (ssize_t offset = 0; offset < n;) {
			ssize_t written = write(output, buffer + offset, (size_t)(n - offset));
			if (written <= 0) return -1;
			offset += written;
		}
	}
	return close(source) || close(output) ? -1 : 0;
}

static int copy_self(const char *target) {
	return copy_file("/proc/self/exe", target);
}

static int executable_child(const char *path, int inherited_fd) {
	errno = 0;
	int readable = open(path, O_RDONLY | O_CLOEXEC);
	result("landlock_exec", "postexec_read_open_denied", readable >= 0 ? 0 : -1, errno);
	if (readable >= 0) close(readable);
	errno = 0;
	int path_fd = open(path, O_PATH | O_CLOEXEC);
	result("landlock_exec", "postexec_opath_allowed", path_fd >= 0 ? 0 : -1, errno);
	errno = 0;
	long changed = path_fd >= 0 ? fchmod(path_fd, 0600) : -1;
	result("landlock_exec", "postexec_fchmod_opath", changed, errno);
	if (path_fd >= 0) close(path_fd);
	errno = 0;
	long inherited = fcntl(inherited_fd, F_GETFD);
	result("landlock_exec", "inherited_execute_fd_closed", inherited, errno);
	return 0;
}

static int executable_control_child(int inherited_fd) {
	errno = 0;
	long inherited = fcntl(inherited_fd, F_GETFD);
	result("landlock_exec", "control_native_exec_succeeded", 0, 0);
	result("landlock_exec", "control_inherited_execute_fd_closed", inherited, errno);
	return 0;
}

static void landlock_execute_only_probe(void) {
	char directory[160], path[192];
	snprintf(directory, sizeof(directory), "/tmp/almagest-execute-only-%ld", (long)getpid());
	snprintf(path, sizeof(path), "%s/role", directory);
	if (mkdir(directory, 0700) == -1 || copy_self(path) == -1 || chmod(directory, 0500) == -1) {
		result("landlock_exec", "copy_static_executable", -1, errno);
		return;
	}
	pid_t control = fork();
	if (control == 0) {
		int execute_fd = open(path, O_RDONLY | O_CLOEXEC);
		char fd_text[32];
		snprintf(fd_text, sizeof(fd_text), "%d", execute_fd);
		char *arguments[] = {path, "--exec-control-child", fd_text, NULL};
		char *environment[] = {NULL};
		syscall(SYS_execveat, execute_fd, "", arguments, environment, AT_EMPTY_PATH);
		_exit(3);
	}
	int control_status = 0;
	waitpid(control, &control_status, 0);
	result("landlock_exec", "control_child_status",
		WIFEXITED(control_status) ? WEXITSTATUS(control_status) : -1, 0);
	pid_t child = fork();
	if (child == 0) {
		int execute_fd = open(path, O_RDONLY | O_CLOEXEC);
		int directory_fd = open(directory, O_PATH | O_CLOEXEC);
		struct landlock_ruleset_attr ruleset_attr = {
			.handled_access_fs = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_READ_FILE,
		};
		int ruleset = (int)syscall(SYS_landlock_create_ruleset, &ruleset_attr, sizeof(ruleset_attr), 0);
		struct landlock_path_beneath_attr rule = {
			.allowed_access = LANDLOCK_ACCESS_FS_EXECUTE,
			.parent_fd = directory_fd,
		};
		long added = syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &rule, 0);
		long restricted = syscall(SYS_landlock_restrict_self, ruleset, 0);
		result("landlock_exec", "execute_only_rule", added || restricted ? -1 : 0,
			added == -1 || restricted == -1 ? errno : 0);
		close(ruleset);
		close(directory_fd);
		char fd_text[32];
		snprintf(fd_text, sizeof(fd_text), "%d", execute_fd);
		char *arguments[] = {path, "--executable-child", path, fd_text, NULL};
		char *environment[] = {NULL};
		syscall(SYS_execveat, execute_fd, "", arguments, environment, AT_EMPTY_PATH);
		result("landlock_exec", "execute_only_exec", -1, errno);
		_exit(2);
	}
	int status = 0;
	waitpid(child, &status, 0);
	result("landlock_exec", "execute_only_child_status",
		WIFEXITED(status) ? WEXITSTATUS(status) : -1, 0);
	chmod(directory, 0700);
	unlink(path);
	rmdir(directory);
}

static int install_metadata_path_filter(void) {
	struct sock_filter filter[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_fchmodat, 1, 0),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_fchmodat2, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = (unsigned short)(sizeof(filter) / sizeof(filter[0])), .filter = filter};
	return prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program);
}

static int executable_copy_child(const char *copy_path, const char *source_path, int inherited_fd) {
	errno = 0;
	int copy = open(copy_path, O_RDONLY | O_CLOEXEC);
	result("landlock_copy", "copy_read_allowed", copy >= 0 ? 0 : -1, errno);
	errno = 0;
	long descriptor_chmod = copy >= 0 ? fchmod(copy, 0600) : -1;
	result("landlock_copy", "copy_descriptor_fchmod_allowed", descriptor_chmod, errno);
	if (copy >= 0) close(copy);
	errno = 0;
	int writable = open(copy_path, O_WRONLY | O_CLOEXEC);
	result("landlock_copy", "copy_content_write_denied", writable >= 0 ? 0 : -1, errno);
	if (writable >= 0) close(writable);
	errno = 0;
	long path_chmod = syscall(SYS_fchmodat, AT_FDCWD, copy_path, 0700);
	result("landlock_copy", "copy_path_fchmodat_denied", path_chmod, errno);
	errno = 0;
	int copy_path_fd = open(copy_path, O_PATH | O_CLOEXEC);
	long empty_path_chmod = copy_path_fd >= 0 ?
		syscall(SYS_fchmodat2, copy_path_fd, "", 0700, AT_EMPTY_PATH) : -1;
	result("landlock_copy", "copy_empty_path_fchmodat2_denied", empty_path_chmod, errno);
	if (copy_path_fd >= 0) close(copy_path_fd);
	errno = 0;
	int source = open(source_path, O_RDONLY | O_CLOEXEC);
	result("landlock_copy", "source_read_denied", source >= 0 ? 0 : -1, errno);
	if (source >= 0) close(source);
	errno = 0;
	int source_write = open(source_path, O_WRONLY | O_CLOEXEC);
	result("landlock_copy", "source_write_denied", source_write >= 0 ? 0 : -1, errno);
	if (source_write >= 0) close(source_write);
	errno = 0;
	long source_chmod = syscall(SYS_fchmodat, AT_FDCWD, source_path, 0600);
	result("landlock_copy", "source_path_fchmodat_denied", source_chmod, errno);
	char parent[192];
	strncpy(parent, copy_path, sizeof(parent) - 1);
	parent[sizeof(parent) - 1] = '\0';
	char *slash = strrchr(parent, '/');
	if (slash) *slash = '\0';
	errno = 0;
	int parent_fd = open(parent, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
	result("landlock_copy", "code_parent_read_dir_denied", parent_fd >= 0 ? 0 : -1, errno);
	if (parent_fd >= 0) close(parent_fd);
	errno = 0;
	long inherited = fcntl(inherited_fd, F_GETFD);
	result("landlock_copy", "inherited_execute_fd_closed", inherited, errno);
	return 0;
}

static void landlock_finite_copy_probe(void) {
	char source_directory[160], code_directory[160], state_directory[160];
	char source[192], copy[192], state_file[192], link_path[192];
	snprintf(source_directory, sizeof(source_directory), "/tmp/almagest-source-%ld", (long)getpid());
	snprintf(code_directory, sizeof(code_directory), "/tmp/almagest-code-%ld", (long)getpid());
	snprintf(state_directory, sizeof(state_directory), "/tmp/almagest-state-%ld", (long)getpid());
	snprintf(source, sizeof(source), "%s/installed", source_directory);
	snprintf(copy, sizeof(copy), "%s/role", code_directory);
	snprintf(state_file, sizeof(state_file), "%s/ordinary", state_directory);
	if (mkdir(source_directory, 0700) == -1 || mkdir(code_directory, 0700) == -1 ||
		mkdir(state_directory, 0700) == -1 ||
		copy_self(source) == -1 || copy_file(source, copy) == -1 || chmod(code_directory, 0500) == -1) {
		result("landlock_copy", "fixture_setup", -1, errno);
		return;
	}
	struct stat source_before;
	stat(source, &source_before);
	pid_t child = fork();
	if (child == 0) {
		int execute_fd = open(copy, O_RDONLY | O_CLOEXEC);
		int code_rule_fd = open(copy, O_PATH | O_CLOEXEC);
		int state_rule_fd = open(state_directory, O_PATH | O_CLOEXEC);
		uint64_t abi3_rights = landlock_rights(3);
		struct landlock_ruleset_attr ruleset_attr = {
			.handled_access_fs = abi3_rights,
		};
		int ruleset = (int)syscall(SYS_landlock_create_ruleset, &ruleset_attr, sizeof(ruleset_attr), 0);
		struct landlock_path_beneath_attr code_rule = {
			.allowed_access = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_READ_FILE,
			.parent_fd = code_rule_fd,
		};
		struct landlock_path_beneath_attr state_rule = {
			.allowed_access = abi3_rights,
			.parent_fd = state_rule_fd,
		};
		long added_code = syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &code_rule, 0);
		long added_state = syscall(SYS_landlock_add_rule, ruleset, LANDLOCK_RULE_PATH_BENEATH, &state_rule, 0);
		long restricted = syscall(SYS_landlock_restrict_self, ruleset, 0);
		errno = 0;
		long descriptor_chmod = fchmod(execute_fd, 0777);
		result("landlock_copy", "preexec_copy_descriptor_fchmod_0777", descriptor_chmod, errno);
		errno = 0;
		int preexec_write = open(copy, O_WRONLY | O_CLOEXEC);
		result("landlock_copy", "preexec_copy_write_denied", preexec_write >= 0 ? 0 : -1, errno);
		if (preexec_write >= 0) close(preexec_write);
		errno = 0;
		int ordinary = open(state_file, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
		int ordinary_open_errno = errno;
		ssize_t ordinary_written = ordinary >= 0 ? write(ordinary, "state", 5) : -1;
		result("landlock_copy", "state_create_allowed", ordinary >= 0 ? 0 : -1, ordinary_open_errno);
		result("landlock_copy", "state_write_allowed", ordinary_written, ordinary_written == -1 ? errno : 0);
		if (ordinary >= 0) close(ordinary);

		snprintf(link_path, sizeof(link_path), "%s/path-link", state_directory);
		errno = 0;
		long linked = link(copy, link_path);
		result("landlock_copy", "code_path_link_denied", linked, errno);
		if (linked == 0) unlink(link_path);
		snprintf(link_path, sizeof(link_path), "%s/linkat-link", state_directory);
		errno = 0;
		linked = syscall(SYS_linkat, AT_FDCWD, copy, AT_FDCWD, link_path, 0);
		result("landlock_copy", "code_path_linkat_denied", linked, errno);
		if (linked == 0) unlink(link_path);
		snprintf(link_path, sizeof(link_path), "%s/empty-path-link", state_directory);
		errno = 0;
		linked = syscall(SYS_linkat, execute_fd, "", AT_FDCWD, link_path, AT_EMPTY_PATH);
		result("landlock_copy", "code_empty_path_linkat_denied", linked, errno);
		if (linked == 0) unlink(link_path);
		char proc_path[64];
		snprintf(proc_path, sizeof(proc_path), "/proc/self/fd/%d", execute_fd);
		snprintf(link_path, sizeof(link_path), "%s/proc-fd-link", state_directory);
		errno = 0;
		linked = syscall(SYS_linkat, AT_FDCWD, proc_path, AT_FDCWD, link_path, AT_SYMLINK_FOLLOW);
		result("landlock_copy", "code_proc_fd_linkat_denied", linked, errno);
		if (linked == 0) unlink(link_path);
		struct stat copy_stat;
		errno = 0;
		long inspected = fstat(execute_fd, &copy_stat);
		result("landlock_copy", "code_fstat_after_link_attacks", inspected, errno);
		result("landlock_copy", "code_nlink_after_link_attacks",
			inspected == 0 ? (long)copy_stat.st_nlink : -1, 0);
		long filtered = install_metadata_path_filter();
		result("landlock_copy", "finite_copy_rules",
			added_code || added_state || restricted || filtered ? -1 : 0,
			added_code == -1 || added_state == -1 || restricted == -1 || filtered == -1 ? errno : 0);
		close(ruleset); close(code_rule_fd); close(state_rule_fd);
		char fd_text[32];
		snprintf(fd_text, sizeof(fd_text), "%d", execute_fd);
		char *arguments[] = {copy, "--executable-copy-child", copy, source, fd_text, NULL};
		char *environment[] = {NULL};
		syscall(SYS_execveat, execute_fd, "", arguments, environment, AT_EMPTY_PATH);
		result("landlock_copy", "finite_copy_exec", -1, errno);
		_exit(2);
	}
	int status = 0;
	waitpid(child, &status, 0);
	result("landlock_copy", "finite_copy_child_status",
		WIFEXITED(status) ? WEXITSTATUS(status) : -1, 0);
	struct stat source_after;
	stat(source, &source_after);
	bool unchanged = source_before.st_mode == source_after.st_mode &&
		source_before.st_size == source_after.st_size &&
		source_before.st_mtim.tv_sec == source_after.st_mtim.tv_sec &&
		source_before.st_mtim.tv_nsec == source_after.st_mtim.tv_nsec;
	result("landlock_copy", "source_metadata_unchanged", unchanged ? 0 : -1, 0);
	chmod(code_directory, 0700);
	unlink(state_file); unlink(copy); unlink(source);
	rmdir(state_directory); rmdir(code_directory); rmdir(source_directory);
}

static void namespace_attempt(const char *name, int flags) {
	pid_t child = fork();
	if (child == 0) {
		errno = 0;
		long rc = unshare(flags);
		result("namespace", name, rc, errno);
		fflush(stdout);
		_exit(0);
	}
	int status = 0;
	waitpid(child, &status, 0);
}

static void namespace_probes(void) {
	namespace_attempt("newuser", CLONE_NEWUSER);
	namespace_attempt("newmount", CLONE_NEWNS);
	namespace_attempt("newpid", CLONE_NEWPID);
	namespace_attempt("newnet", CLONE_NEWNET);
	namespace_attempt("newipc", CLONE_NEWIPC);
	namespace_attempt("newuts", CLONE_NEWUTS);
	namespace_attempt("newuser_newmount", CLONE_NEWUSER | CLONE_NEWNS);
}

static volatile sig_atomic_t got_signal;
static char broker_memory[] = "broker-memory-secret";

static void signal_handler(int signal_number) {
	if (signal_number == SIGUSR1) got_signal |= 1;
	if (signal_number == SIGUSR2) got_signal |= 2;
}

struct broker_info {
	pid_t pid;
	uintptr_t address;
	int fd;
};

static ssize_t read_small(const char *path) {
	char buffer[128];
	int fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd == -1) return -1;
	ssize_t n = read(fd, buffer, sizeof(buffer));
	int saved = errno;
	close(fd);
	errno = saved;
	return n;
}

static void report_read(const char *group, const char *name, const char *path) {
	errno = 0;
	ssize_t n = read_small(path);
	int saved = errno;
	result(group, name, n, saved);
}

static void attacker(pid_t broker, uintptr_t address, int broker_fd) {
	char path[128];
	errno = 0;
	result("same_uid", "signal_zero", kill(broker, 0), errno);
	errno = 0;
	result("same_uid", "signal_usr1", kill(broker, SIGUSR1), errno);
	errno = 0;
	result("same_uid", "tgkill_zero", syscall(SYS_tgkill, broker, broker, 0), errno);
	int async_pipe[2];
	if (pipe2(async_pipe, O_CLOEXEC) == 0) {
		errno = 0;
		long owner = fcntl(async_pipe[0], F_SETOWN, broker);
		result("same_uid", "fcntl_setown_peer", owner, errno);
		errno = 0;
		long signal_number = fcntl(async_pipe[0], F_SETSIG, SIGUSR2);
		result("same_uid", "fcntl_setsig_peer", signal_number, errno);
		errno = 0;
		long async = fcntl(async_pipe[0], F_SETFL, O_ASYNC | O_NONBLOCK);
		result("same_uid", "fcntl_enable_async", async, errno);
		ssize_t triggered = write(async_pipe[1], "s", 1);
		result("same_uid", "fcntl_sigio_trigger_write", triggered, triggered == -1 ? errno : 0);
		close(async_pipe[0]); close(async_pipe[1]);
	}
	struct rlimit peer_limit;
	errno = 0;
	long peer_query = syscall(SYS_prlimit64, broker, RLIMIT_NOFILE, NULL, &peer_limit);
	result("same_uid", "prlimit64_query", peer_query, errno);
	if (peer_query == 0) {
		struct rlimit changed = peer_limit;
		if (changed.rlim_cur > 32) changed.rlim_cur = 32;
		errno = 0;
		result("same_uid", "prlimit64_lower_nofile",
			syscall(SYS_prlimit64, broker, RLIMIT_NOFILE, &changed, NULL), errno);
	}
	snprintf(path, sizeof(path), "/proc/%ld/status", (long)broker);
	report_read("same_uid", "proc_status_read", path);
	snprintf(path, sizeof(path), "/proc/%ld/cmdline", (long)broker);
	report_read("same_uid", "proc_cmdline_read", path);
	snprintf(path, sizeof(path), "/proc/%ld/environ", (long)broker);
	report_read("same_uid", "proc_environ_read", path);
	snprintf(path, sizeof(path), "/proc/%ld/fd/%d", (long)broker, broker_fd);
	report_read("same_uid", "proc_fd_open", path);
	char memory[sizeof(broker_memory)] = {0};
	struct iovec local = {.iov_base = memory, .iov_len = sizeof(memory)};
	struct iovec remote = {.iov_base = (void *)address, .iov_len = sizeof(memory)};
	errno = 0;
	ssize_t n = process_vm_readv(broker, &local, 1, &remote, 1, 0);
	result("same_uid", "process_vm_readv", n, errno);
	errno = 0;
	long attached = ptrace(PTRACE_ATTACH, broker, NULL, NULL);
	result("same_uid", "ptrace_attach", attached, errno);
	if (attached == 0) {
		waitpid(broker, NULL, 0);
		ptrace(PTRACE_DETACH, broker, NULL, NULL);
	}
}

static void same_uid_round(bool dumpable) {
	int info_pipe[2], done_pipe[2];
	if (pipe2(info_pipe, O_CLOEXEC) || pipe2(done_pipe, O_CLOEXEC)) return;
	pid_t broker = fork();
	if (broker == 0) {
		close(info_pipe[0]); close(done_pipe[1]);
		char path[128];
		snprintf(path, sizeof(path), "/tmp/almagest-broker-%ld", (long)getpid());
		int fd = open(path, O_RDWR | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
		if (fd >= 0) {
			ssize_t ignored_write = write(fd, "held-fd-secret", 14);
			(void)ignored_write;
			lseek(fd, 0, SEEK_SET);
			unlink(path);
		}
		signal(SIGUSR1, signal_handler);
		signal(SIGUSR2, signal_handler);
		prctl(PR_SET_DUMPABLE, dumpable ? 1 : 0, 0, 0, 0);
		struct broker_info info = {.pid = getpid(), .address = (uintptr_t)broker_memory, .fd = fd};
		ssize_t info_written = write(info_pipe[1], &info, sizeof(info));
		if (info_written != sizeof(info)) _exit(4);
		char byte;
		ssize_t done_read = read(done_pipe[0], &byte, 1);
		if (done_read != 1) _exit(5);
		_exit(got_signal);
	}
	close(info_pipe[1]); close(done_pipe[0]);
	struct broker_info info;
	if (read(info_pipe[0], &info, sizeof(info)) != sizeof(info)) return;
	printf("ROUND same_uid dumpable=%d broker=%ld\n", dumpable ? 1 : 0, (long)broker);
	pid_t attack = fork();
	if (attack == 0) {
		attacker(broker, info.address, info.fd);
		fflush(stdout);
		_exit(0);
	}
	waitpid(attack, NULL, 0);
	ssize_t done_written = write(done_pipe[1], "x", 1);
	if (done_written != 1) kill(broker, SIGTERM);
	int broker_exit = 0;
	waitpid(broker, &broker_exit, 0);
	result("same_uid", dumpable ? "broker_signal_bits_dumpable" : "broker_signal_bits_nondumpable",
		WIFEXITED(broker_exit) ? WEXITSTATUS(broker_exit) : -1, 0);
	close(info_pipe[0]); close(done_pipe[1]);
}

static int install_peer_filter(pid_t peer) {
	uint32_t argument_offset = (uint32_t)offsetof(struct seccomp_data, args[0]);
	struct sock_filter filter[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_tgkill, 0, 4),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, argument_offset),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, (uint32_t)peer, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_prlimit64, 0, 4),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, argument_offset),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, (uint32_t)peer, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = (unsigned short)(sizeof(filter) / sizeof(filter[0])), .filter = filter};
	return prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program);
}

static int install_fcntl_owner_filter(void) {
	uint32_t command_offset = (uint32_t)offsetof(struct seccomp_data, args[1]);
	struct sock_filter filter[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_fcntl, 0, 7),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, command_offset),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, F_SETOWN, 4, 0),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, F_SETOWN_EX, 3, 0),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, F_SETSIG, 2, 0),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = (unsigned short)(sizeof(filter) / sizeof(filter[0])), .filter = filter};
	return prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program);
}

static void targeted_peer_filter_probe(void) {
	int done_pipe[2];
	if (pipe2(done_pipe, O_CLOEXEC)) return;
	pid_t broker = fork();
	if (broker == 0) {
		close(done_pipe[1]);
		char byte;
		ssize_t n = read(done_pipe[0], &byte, 1);
		_exit(n == 1 ? 0 : 1);
	}
	close(done_pipe[0]);
	errno = 0;
	long value = install_peer_filter(broker);
	int saved = errno;
	result("target_filter", "install_peer_argument_filter", value, saved);
	errno = 0;
	value = syscall(SYS_tgkill, broker, broker, 0);
	saved = errno;
	result("target_filter", "tgkill_peer_denied", value, saved);
	errno = 0;
	value = syscall(SYS_tgkill, getpid(), getpid(), 0);
	saved = errno;
	result("target_filter", "tgkill_self_allowed", value, saved);
	struct rlimit limit;
	errno = 0;
	value = syscall(SYS_prlimit64, broker, RLIMIT_NOFILE, NULL, &limit);
	saved = errno;
	result("target_filter", "prlimit_peer_denied", value, saved);
	errno = 0;
	value = syscall(SYS_prlimit64, getpid(), RLIMIT_NOFILE, NULL, &limit);
	saved = errno;
	result("target_filter", "prlimit_self_allowed", value, saved);
	int descriptor = open("/dev/null", O_RDONLY | O_CLOEXEC);
	errno = 0;
	value = install_fcntl_owner_filter();
	saved = errno;
	result("target_filter", "install_fcntl_owner_filter", value, saved);
	errno = 0;
	value = fcntl(descriptor, F_GETFL);
	saved = errno;
	result("target_filter", "fcntl_getfl_allowed", value, saved);
	errno = 0;
	value = fcntl(descriptor, F_SETOWN, broker);
	saved = errno;
	result("target_filter", "fcntl_setown_peer_denied", value, saved);
	close(descriptor);
	ssize_t n = write(done_pipe[1], "x", 1);
	if (n != 1) kill(broker, SIGTERM);
	waitpid(broker, NULL, 0);
	close(done_pipe[1]);
}

static void cgroup_probe(void) {
	report_read("cgroup", "self_cgroup_read", "/proc/self/cgroup");
	errno = 0;
	long created = mkdir("/sys/fs/cgroup/almagest-rootless-probe", 0700);
	result("cgroup", "create_child", created, errno);
	if (created == 0) rmdir("/sys/fs/cgroup/almagest-rootless-probe");
	errno = 0;
	int fd = open("/sys/fs/cgroup/cgroup.procs", O_WRONLY | O_CLOEXEC);
	result("cgroup", "open_cgroup_procs_write", fd >= 0 ? 0 : -1, errno);
	if (fd >= 0) close(fd);
}

static void run_probe_child(void (*probe)(void)) {
	pid_t child = fork();
	if (child == 0) {
		probe();
		fflush(stdout);
		_exit(0);
	}
	int status = 0;
	waitpid(child, &status, 0);
	result("harness", "probe_child_status", WIFEXITED(status) ? WEXITSTATUS(status) : -1, 0);
}

static void rlimit_probe(void) {
	struct rlimit old_limit;
	getrlimit(RLIMIT_NOFILE, &old_limit);
	struct rlimit limit = {.rlim_cur = 16, .rlim_max = old_limit.rlim_max};
	errno = 0;
	result("rlimit", "set_nofile_16", setrlimit(RLIMIT_NOFILE, &limit), errno);
	int fds[32], opened = 0, saved = 0;
	for (; opened < 32; opened++) {
		fds[opened] = open("/dev/null", O_RDONLY | O_CLOEXEC);
		if (fds[opened] == -1) { saved = errno; break; }
	}
	result("rlimit", "nofile_open_count", opened, saved);
	for (int i = 0; i < opened; i++) close(fds[i]);
	limit.rlim_cur = old_limit.rlim_cur;
	setrlimit(RLIMIT_NOFILE, &limit);
	struct rlimit as_old;
	getrlimit(RLIMIT_AS, &as_old);
	limit.rlim_cur = 64 * 1024 * 1024;
	limit.rlim_max = as_old.rlim_max;
	errno = 0;
	result("rlimit", "set_address_space_64m", setrlimit(RLIMIT_AS, &limit), errno);
	errno = 0;
	void *mapping = mmap(NULL, 128 * 1024 * 1024, PROT_READ | PROT_WRITE,
		MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	result("rlimit", "address_space_128m_mmap", mapping == MAP_FAILED ? -1 : 0, errno);
	if (mapping != MAP_FAILED) munmap(mapping, 128 * 1024 * 1024);
}

int main(int argc, char **argv) {
	setvbuf(stdout, NULL, _IONBF, 0);
	if (argc == 4 && !strcmp(argv[1], "--executable-child")) {
		return executable_child(argv[2], atoi(argv[3]));
	}
	if (argc == 3 && !strcmp(argv[1], "--exec-control-child")) {
		return executable_control_child(atoi(argv[2]));
	}
	if (argc == 5 && !strcmp(argv[1], "--executable-copy-child")) {
		return executable_copy_child(argv[2], argv[3], atoi(argv[4]));
	}
	printf("PROBE rootless adaptive isolation\n");
	status_lines("startup");
	run_probe_child(seccomp_probe);
	run_probe_child(landlock_probe);
	landlock_execute_only_probe();
	landlock_finite_copy_probe();
	namespace_probes();
	cgroup_probe();
	rlimit_probe();
	same_uid_round(true);
	same_uid_round(false);
	run_probe_child(targeted_peer_filter_probe);
	return 0;
}
