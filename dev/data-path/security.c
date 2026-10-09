#define _GNU_SOURCE

/* Development-only hostile handle audit. Run through security.sh in Docker.
 * The existing launcher applies the checkpoint 10 profile before this probe.
 * This is not a data-channel implementation or an acceptance benchmark. */
#include <errno.h>
#include <fcntl.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/if_tun.h>
#include <linux/seccomp.h>
#include <net/if.h>
#include <netinet/in.h>
#include <poll.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

enum { TCP, LISTENER, UDP_CONNECTED, UDP_UNCONNECTED, TUN, MEMORY, EVENT, FD_COUNT };
static bool first = true;

static void require(bool ok, const char *what) {
	if (!ok) {
		fprintf(stderr, "data-path security: %s (errno=%d: %s)\n", what, errno, strerror(errno));
		exit(1);
	}
}

static void record(const char *name, bool ok) {
	require(ok, name);
	printf("%s\"%s\":true", first ? "" : ",", name);
	first = false;
}

static void readable(int fd) {
	struct pollfd item = {.fd = fd, .events = POLLIN};
	require(poll(&item, 1, 5000) == 1 && (item.revents & POLLIN), "bounded receive");
}

static void send_token(int fd, char token) {
	require(send(fd, &token, 1, MSG_NOSIGNAL) == 1, "send token");
}

static void expect_token(int fd, char expected) {
	readable(fd);
	char token = 0;
	require(recv(fd, &token, 1, 0) == 1 && token == expected, "receive token");
}

static bool denied(int result) { return result == -1 && errno == EPERM; }

static void final_filter(void) {
	struct sock_filter code[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, 4),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, AUDIT_ARCH_X86_64, 1, 0),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_KILL_PROCESS),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS, 0),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_execve, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_execveat, 0, 1),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog program = {.len = sizeof(code) / sizeof(code[0]), .filter = code};
	require(syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_TSYNC, &program) == 0,
		"apply final execution filter");
}

static void probe(void) {
	final_filter();
	require(getuid() == 65534 && getgid() == 65534 && getpid() == 1 &&
		prctl(PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) == 1 && prctl(PR_GET_SECCOMP, 0, 0, 0, 0) == 2,
		"restricted identity");
	require(denied(socket(AF_INET, SOCK_DGRAM, 0)), "native socket denied");
	send_token(3, 'R');

	struct sockaddr_in destination;
	char ancillary[CMSG_SPACE(sizeof(int) * FD_COUNT)] = {0};
	struct iovec vector = {.iov_base = &destination, .iov_len = sizeof(destination)};
	struct msghdr message = {.msg_iov = &vector, .msg_iovlen = 1,
		.msg_control = ancillary, .msg_controllen = sizeof(ancillary)};
	readable(3);
	require(recvmsg(3, &message, 0) == sizeof(destination) && !(message.msg_flags & (MSG_TRUNC | MSG_CTRUNC)),
		"receive exact handles");
	struct cmsghdr *header = CMSG_FIRSTHDR(&message);
	require(header && header->cmsg_level == SOL_SOCKET && header->cmsg_type == SCM_RIGHTS &&
		header->cmsg_len == CMSG_LEN(sizeof(int) * FD_COUNT) && CMSG_NXTHDR(&message, header) == NULL,
		"validate exact handles");
	int fds[FD_COUNT];
	memcpy(fds, CMSG_DATA(header), sizeof(fds));

	/* This token is sent only after N closes all transferred network copies. */
	expect_token(3, 'C');
	int duplicate = dup(fds[TCP]);
	require(duplicate >= 0 && close(fds[TCP]) == 0, "duplicate runtime TCP handle");
	send_token(duplicate, 'T');
	send_token(3, 'T');
	expect_token(3, 'T');
	int option = 1;
	require(denied(setsockopt(duplicate, SOL_SOCKET, SO_REUSEADDR, &option, sizeof(option))), "socket option denied");
	require(denied(connect(duplicate, (struct sockaddr *)&destination, sizeof(destination))), "TCP reconnect denied");
	require(denied(connect(fds[UDP_CONNECTED], (struct sockaddr *)&destination, sizeof(destination))), "UDP reconnect denied");
	struct sockaddr disconnect = {.sa_family = AF_UNSPEC};
	require(denied(connect(fds[UDP_CONNECTED], &disconnect, sizeof(disconnect))), "UDP disconnect denied");
	require(denied(bind(fds[UDP_UNCONNECTED], (struct sockaddr *)&destination, sizeof(destination))), "rebind denied");
	require(denied(listen(fds[LISTENER], 8)), "listen change denied");
	require(denied(accept(fds[LISTENER], NULL, NULL)), "accept denied");
	require(denied(shutdown(duplicate, SHUT_RDWR)), "runtime shutdown denied");
	struct ifreq interface = {0};
	require(denied(ioctl(fds[TUN], TUNGETIFF, &interface)), "TUN query ioctl denied");
	require(denied(ioctl(fds[TUN], TUNSETPERSIST, 1)), "TUN persistence ioctl denied");
	require(denied(ioctl(fds[TUN], TUNSETQUEUE, &interface)), "TUN queue ioctl denied");
	send_token(3, 'D');
	send_token(fds[UDP_CONNECTED], 'N');
	send_token(3, 'N');
	expect_token(3, 'N');

	/* connect(2) denial does not prevent sendto(2) overriding a UDP peer. */
	require(sendto(fds[UDP_CONNECTED], "U", 1, 0, (struct sockaddr *)&destination, sizeof(destination)) == 1,
		"connected UDP destination override");
	send_token(3, 'U');
	expect_token(3, 'U');
	require(sendto(fds[UDP_UNCONNECTED], "V", 1, 0, (struct sockaddr *)&destination, sizeof(destination)) == 1,
		"unconnected UDP destination");
	send_token(3, 'V');
	expect_token(3, 'V');

	/* F_SETFL changes the shared open-file description, even in N. */
	int flags = fcntl(fds[MEMORY], F_GETFL);
	require(flags >= 0 && fcntl(fds[MEMORY], F_SETFL, flags | O_NONBLOCK) == 0, "change shared file flags");
	unsigned char *mapping = mmap(NULL, 65536, PROT_READ | PROT_WRITE, MAP_SHARED, fds[MEMORY], 0);
	require(mapping != MAP_FAILED, "map shared memory");
	uint32_t length = 1;
	memcpy(mapping, &length, sizeof(length));
	mapping[4] = 'A';
	send_token(3, 'M');
	expect_token(3, 'M'); /* N has validated the length and payload. */
	length = UINT32_MAX;
	memcpy(mapping, &length, sizeof(length));
	mapping[4] = 'B';
	send_token(3, 'X');
	expect_token(3, 'X');
	uint64_t saturation = UINT64_MAX - 1;
	require(write(fds[EVENT], &saturation, sizeof(saturation)) == sizeof(saturation), "saturate response notification");
	send_token(3, 'E');
	expect_token(3, 'E');
	require(ftruncate(fds[MEMORY], 0) == 0, "truncate broker mapping");
	send_token(3, 'S');
	expect_token(3, 'S');

	/* Hold TUN and listener until exit. The broker checks their lifetime. */
	send_token(3, 'Q');
	expect_token(3, 'Q');
	_exit(0);
}

static int bound_socket(int type, struct sockaddr_in *address) {
	int fd = socket(AF_INET, type | SOCK_CLOEXEC, 0);
	require(fd >= 0, "create socket");
	*address = (struct sockaddr_in){.sin_family = AF_INET, .sin_addr.s_addr = htonl(INADDR_LOOPBACK)};
	require(bind(fd, (struct sockaddr *)address, sizeof(*address)) == 0, "bind socket");
	socklen_t size = sizeof(*address);
	require(getsockname(fd, (struct sockaddr *)address, &size) == 0, "socket address");
	return fd;
}

static void broker(void) {
	/* Bring up loopback in the disposable container only. */
	int setup = socket(AF_INET, SOCK_DGRAM, 0);
	struct ifreq loopback = {0};
	strcpy(loopback.ifr_name, "lo");
	require(setup >= 0 && ioctl(setup, SIOCGIFFLAGS, &loopback) == 0, "get loopback flags");
	loopback.ifr_flags |= IFF_UP;
	require(ioctl(setup, SIOCSIFFLAGS, &loopback) == 0 && close(setup) == 0, "enable container loopback");
	int fds[FD_COUNT];
	struct sockaddr_in tcp_address, expected_address, other_address, listener_address, unused;
	int tcp_listener = bound_socket(SOCK_STREAM, &tcp_address);
	require(listen(tcp_listener, 1) == 0, "TCP listen");
	fds[TCP] = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
	require(fds[TCP] >= 0 && connect(fds[TCP], (struct sockaddr *)&tcp_address, sizeof(tcp_address)) == 0, "TCP connect");
	int tcp_peer = accept(tcp_listener, NULL, NULL);
	require(tcp_peer >= 0 && close(tcp_listener) == 0, "TCP accept");
	fds[LISTENER] = bound_socket(SOCK_STREAM, &listener_address);
	require(listen(fds[LISTENER], 1) == 0, "transfer listener");
	int expected_peer = bound_socket(SOCK_DGRAM, &expected_address);
	int other_peer = bound_socket(SOCK_DGRAM, &other_address);
	fds[UDP_CONNECTED] = bound_socket(SOCK_DGRAM, &unused);
	require(connect(fds[UDP_CONNECTED], (struct sockaddr *)&expected_address, sizeof(expected_address)) == 0, "connect UDP");
	fds[UDP_UNCONNECTED] = bound_socket(SOCK_DGRAM, &unused);
	fds[TUN] = open("/dev/net/tun", O_RDWR | O_CLOEXEC);
	struct ifreq interface = {.ifr_flags = IFF_TUN | IFF_NO_PI};
	strcpy(interface.ifr_name, "alm-audit");
	require(fds[TUN] >= 0 && ioctl(fds[TUN], TUNSETIFF, &interface) == 0, "create nonpersistent TUN");
	fds[MEMORY] = (int)syscall(SYS_memfd_create, "alm-audit", MFD_CLOEXEC);
	require(fds[MEMORY] >= 0 && ftruncate(fds[MEMORY], 65536) == 0, "create candidate memfd");
	unsigned char *mapping = mmap(NULL, 65536, PROT_READ | PROT_WRITE, MAP_SHARED, fds[MEMORY], 0);
	require(mapping != MAP_FAILED, "broker mapping");
	/* NONBLOCK lets this audit detect saturation without hanging the broker. */
	fds[EVENT] = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
	require(fds[EVENT] >= 0, "create event");
	expect_token(3, 'R');
	printf("{\"schema_version\":1,\"profile\":\"linux-runtime-r-v1\",\"checks\":{");
	record("restricted_runtime", true);
	char ancillary[CMSG_SPACE(sizeof(fds))] = {0};
	struct iovec vector = {.iov_base = &other_address, .iov_len = sizeof(other_address)};
	struct msghdr message = {.msg_iov = &vector, .msg_iovlen = 1,
		.msg_control = ancillary, .msg_controllen = sizeof(ancillary)};
	struct cmsghdr *header = CMSG_FIRSTHDR(&message);
	header->cmsg_level = SOL_SOCKET;
	header->cmsg_type = SCM_RIGHTS;
	header->cmsg_len = CMSG_LEN(sizeof(fds));
	memcpy(CMSG_DATA(header), fds, sizeof(fds));
	require(sendmsg(3, &message, MSG_NOSIGNAL) == sizeof(other_address), "transfer handles");
	for (int index = TCP; index <= TUN; index++) require(close(fds[index]) == 0, "close broker network copy");
	send_token(3, 'C');
	expect_token(3, 'T');
	expect_token(tcp_peer, 'T');
	record("tcp_duplicate_survives_broker_close", true);
	send_token(3, 'T');
	expect_token(3, 'D');
	record("socket_control_denied", true);
	record("tun_ioctls_denied", true);
	record("listener_accept_denied", true);
	expect_token(3, 'N');
	expect_token(expected_peer, 'N');
	record("connected_udp_survives_broker_close", true);
	send_token(3, 'N');
	expect_token(3, 'U');
	expect_token(other_peer, 'U');
	record("connected_udp_destination_override", true);
	send_token(3, 'U');
	expect_token(3, 'V');
	expect_token(other_peer, 'V');
	record("unconnected_udp_survives_broker_close", true);
	send_token(3, 'V');
	expect_token(3, 'M');
	record("shared_file_flags_mutable", (fcntl(fds[MEMORY], F_GETFL) & O_NONBLOCK) != 0);
	uint32_t snapshot;
	memcpy(&snapshot, mapping, sizeof(snapshot));
	require(snapshot == 1 && mapping[4] == 'A', "initial shared record");
	send_token(3, 'M');
	expect_token(3, 'X');
	uint32_t changed;
	memcpy(&changed, mapping, sizeof(changed));
	record("shared_metadata_and_payload_mutable_after_validation", snapshot == 1 && changed == UINT32_MAX && mapping[4] == 'B');
	send_token(3, 'X');
	expect_token(3, 'E');
	uint64_t notification = 1;
	record("shared_notification_saturated", write(fds[EVENT], &notification, sizeof(notification)) == -1 && errno == EAGAIN);
	send_token(3, 'E');
	expect_token(3, 'S');
	struct stat status;
	record("shared_backing_truncated", fstat(fds[MEMORY], &status) == 0 && status.st_size == 0);
	/* Do not access the invalidated mapping: that would SIGBUS the broker. */
	send_token(3, 'S');
	expect_token(3, 'Q');
	record("tun_survives_broker_close", if_nametoindex(interface.ifr_name) != 0);
	int contender = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
	require(contender >= 0, "listener contender");
	record("listener_survives_broker_close", bind(contender, (struct sockaddr *)&listener_address, sizeof(listener_address)) == -1 && errno == EADDRINUSE);
	send_token(3, 'Q');
	int child_status;
	pid_t child = (pid_t)strtol(getenv("ALMAGEST_RESTRICTED_CHILD_PID"), NULL, 10);
	require(child > 0 && waitpid(child, &child_status, 0) == child && WIFEXITED(child_status) && WEXITSTATUS(child_status) == 0, "restricted child exit");
	/* Linux device unregister may finish after the final file close. */
	for (int attempt = 0; attempt < 100 && if_nametoindex(interface.ifr_name); attempt++) usleep(10000);
	record("tun_removed_after_runtime_exit", if_nametoindex(interface.ifr_name) == 0);
	record("listener_released_after_runtime_exit", bind(contender, (struct sockaddr *)&listener_address, sizeof(listener_address)) == 0);
	readable(tcp_peer);
	char value;
	record("tcp_eof_after_runtime_exit", recv(tcp_peer, &value, 1, 0) == 0);
	printf("}}\n");
}

int main(void) {
	if (getenv("ALMAGEST_RESTRICTED_CHILD_PID")) broker();
	else probe();
	return 0;
}
