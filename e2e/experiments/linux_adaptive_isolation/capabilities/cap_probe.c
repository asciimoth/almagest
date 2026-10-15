#include <errno.h>
#include <fcntl.h>
#include <linux/capability.h>
#include <linux/if.h>
#include <linux/if_tun.h>
#include <sched.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#define CAP_WORD(capability) ((capability) / 32)
#define CAP_MASK(capability) (1U << ((capability) % 32))

static const char *self_path;

static void die(const char *operation)
{
	perror(operation);
	exit(1);
}

static void print_status(const char *stage)
{
	FILE *status;
	char *line = NULL;
	size_t size = 0;

	printf("STAGE %s pid=%ld ppid=%ld\n", stage, (long)getpid(), (long)getppid());
	status = fopen("/proc/self/status", "re");
	if (status == NULL)
		die("open /proc/self/status");
	while (getline(&line, &size, status) >= 0) {
		if (strncmp(line, "Uid:", 4) == 0 || strncmp(line, "Gid:", 4) == 0 ||
		    strncmp(line, "Cap", 3) == 0 ||
		    strncmp(line, "NoNewPrivs:", 11) == 0 ||
		    strncmp(line, "Seccomp:", 8) == 0)
			fputs(line, stdout);
	}
	free(line);
	if (fclose(status) != 0)
		die("close /proc/self/status");
	fflush(stdout);
}

static void set_caps(unsigned int mask)
{
	struct __user_cap_header_struct header = {
		.version = _LINUX_CAPABILITY_VERSION_3,
		.pid = 0,
	};
	struct __user_cap_data_struct data[2] = {{0}};

	data[CAP_WORD(CAP_NET_ADMIN)].inheritable = mask;
	data[CAP_WORD(CAP_NET_ADMIN)].permitted = mask;
	data[CAP_WORD(CAP_NET_ADMIN)].effective = mask;
	if (syscall(SYS_capset, &header, data) != 0)
		die("capset");
}

static int run_ip(const char *const arguments[])
{
	pid_t child = fork();
	int status;

	if (child < 0)
		die("fork ip");
	if (child == 0) {
		execv("/usr/sbin/ip", (char *const *)arguments);
		perror("exec ip");
		_exit(127);
	}
	if (waitpid(child, &status, 0) != child)
		die("waitpid ip");
	if (!WIFEXITED(status))
		return 128;
	return WEXITSTATUS(status);
}

static int create_tun(const char *name)
{
	struct ifreq request = {0};
	int descriptor;
	int saved_errno;

	descriptor = open("/dev/net/tun", O_RDWR | O_CLOEXEC);
	if (descriptor < 0)
		return errno;
	request.ifr_flags = IFF_TUN | IFF_NO_PI;
	strncpy(request.ifr_name, name, IFNAMSIZ - 1);
	if (ioctl(descriptor, TUNSETIFF, &request) != 0) {
		saved_errno = errno;
		close(descriptor);
		return saved_errno;
	}
	if (close(descriptor) != 0)
		die("close tun");
	return 0;
}

static int check_dns_write(void)
{
	int descriptor = open("/etc/resolv.conf", O_WRONLY | O_APPEND | O_CLOEXEC);

	if (descriptor < 0)
		return errno;
	close(descriptor);
	return 0;
}

static int network_checks(bool has_capability, bool strip_after)
{
	const char *const add_dummy[] = {"ip", "link", "add", "capdummy0", "type", "dummy", NULL};
	const char *const up_dummy[] = {"ip", "link", "set", "capdummy0", "up", NULL};
	const char *const add_route[] = {"ip", "route", "add", "198.18.0.0/24", "dev", "capdummy0", NULL};
	const char *const denied_dummy[] = {"ip", "link", "add", "capdenied0", "type", "dummy", NULL};
	const char *expect_tun = getenv("EXPECT_TUN");
	const char *expect_ip = getenv("EXPECT_IP");
	int tun_error = create_tun("captun0");
	int dummy_status = run_ip(add_dummy);
	int up_status = dummy_status == 0 ? run_ip(up_dummy) : 125;
	int route_status = up_status == 0 ? run_ip(add_route) : 125;
	int dns_error = check_dns_write();
	bool expect_tun_success = expect_tun == NULL || strcmp(expect_tun, "success") == 0;
	bool expect_ip_success = expect_ip == NULL || strcmp(expect_ip, "success") == 0;

	printf("RESULT privileged tun_errno=%d dummy_exit=%d up_exit=%d route_exit=%d dns_write_errno=%d\n",
	       tun_error, dummy_status, up_status, route_status, dns_error);
	fflush(stdout);
	if (expect_ip_success && (dummy_status != 0 || up_status != 0 || route_status != 0))
		return 1;
	if (!expect_ip_success && dummy_status == 0)
		return 1;
	if (expect_tun_success != (tun_error == 0))
		return 1;
	if (dns_error == 0)
		return 1;
	if (!has_capability) {
		errno = 0;
		if (prctl(PR_CAPBSET_DROP, CAP_NET_ADMIN, 0, 0, 0) == 0) {
			printf("RESULT bounding_drop status=unexpected-success errno=0\n");
			return 1;
		}
		printf("RESULT bounding_drop status=denied errno=%d\n", errno);
		fflush(stdout);
		return 0;
	}
	if (!strip_after)
		return 0;

	set_caps(0);
	if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) != 0)
		die("clear ambient capabilities");
	print_status("runtime.stripped");
	tun_error = create_tun("capdeniedtun0");
	dummy_status = run_ip(denied_dummy);
	printf("RESULT stripped tun_errno=%d dummy_exit=%d\n", tun_error, dummy_status);
	fflush(stdout);
	return tun_error == 0 || dummy_status == 0;
}

static void file_first(const char *next_mode)
{
	print_status("file.pre-exec2");
	execl(self_path, self_path, next_mode, NULL);
	die("exec runtime.exec2");
}

static int bootstrap(void)
{
	pid_t child;
	int status;

	print_status("bootstrap.initial");
	child = fork();
	if (child < 0)
		die("fork broker");
	if (child == 0) {
		if (prctl(PR_SET_KEEPCAPS, 1, 0, 0, 0) != 0)
			die("PR_SET_KEEPCAPS");
		if (prctl(PR_CAPBSET_DROP, CAP_SETUID, 0, 0, 0) != 0 ||
		    prctl(PR_CAPBSET_DROP, CAP_SETGID, 0, 0, 0) != 0 ||
		    prctl(PR_CAPBSET_DROP, CAP_SETPCAP, 0, 0, 0) != 0)
			die("drop bootstrap-only bounding capabilities");
		if (setresgid(65534, 65534, 65534) != 0)
			die("setresgid");
		if (setresuid(65534, 65534, 65534) != 0)
			die("setresuid");
		set_caps(CAP_MASK(CAP_NET_ADMIN));
		if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_RAISE, CAP_NET_ADMIN, 0, 0) != 0)
			die("raise ambient CAP_NET_ADMIN");
		print_status("handoff.pre-exec");
		execl(self_path, self_path, "network-stage1", NULL);
		die("exec network-stage1");
	}
	if (waitpid(child, &status, 0) != child)
		die("waitpid bootstrap");
	print_status("bootstrap.finished");
	if (!WIFEXITED(status))
		return 128;
	return WEXITSTATUS(status);
}

static int network_stage2(void)
{
	pid_t runtime;
	int status;

	print_status("network.exec2");
	if (network_checks(true, false) != 0)
		return 1;
	runtime = fork();
	if (runtime < 0)
		die("fork runtime");
	if (runtime == 0) {
		if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) != 0)
			die("clear runtime ambient capabilities");
		set_caps(0);
		if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0)
			die("set runtime no_new_privs");
		if (setenv("EXPECT_TUN", "denied", 1) != 0 ||
		    setenv("EXPECT_IP", "denied", 1) != 0)
			die("set runtime expectations");
		print_status("runtime.pre-exec");
		execl(self_path, self_path, "runtime-final", NULL);
		die("exec runtime-final");
	}
	if (waitpid(runtime, &status, 0) != runtime)
		die("waitpid runtime");
	print_status("network.retained");
	if (!WIFEXITED(status))
		return 128;
	return WEXITSTATUS(status);
}

int main(int argc, char **argv)
{
	self_path = argv[0];
	if (argc != 2) {
		fprintf(stderr, "usage: %s MODE\n", argv[0]);
		return 2;
	}
	if (strcmp(argv[1], "bootstrap") == 0)
		return bootstrap();
	if (strcmp(argv[1], "network-stage1") == 0) {
		print_status("network.exec1");
		execl(self_path, self_path, "network-stage2", NULL);
		die("exec network-stage2");
		return 1;
	}
	if (strcmp(argv[1], "network-stage2") == 0)
		return network_stage2();
	if (strcmp(argv[1], "runtime-final") == 0) {
		print_status("runtime.final-exec");
		return network_checks(false, false);
	}
	if (strcmp(argv[1], "file-1") == 0) {
		print_status("file.exec1");
		file_first("file-2");
		return 1;
	}
	if (strcmp(argv[1], "file-2") == 0) {
		print_status("file.exec2");
		return network_checks(geteuid() == 65534 && getenv("EXPECT_CAP") != NULL, true);
	}
	if (strcmp(argv[1], "nnp-file-1") == 0) {
		print_status("nnp.before-file-exec");
		errno = 0;
		if (prctl(PR_CAPBSET_DROP, CAP_NET_ADMIN, 0, 0, 0) == 0) {
			printf("RESULT bounding_drop status=unexpected-success errno=0\n");
			return 1;
		}
		printf("RESULT bounding_drop status=denied errno=%d\n", errno);
		fflush(stdout);
		execl("/usr/local/bin/cap-probe-file", "/usr/local/bin/cap-probe-file", "file-2", NULL);
		die("exec file capability under no_new_privs");
	}
	if (strcmp(argv[1], "none") == 0) {
		print_status("no-capability");
		return network_checks(false, false);
	}
	fprintf(stderr, "unknown mode: %s\n", argv[1]);
	return 2;
}
