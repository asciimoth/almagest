/* Static fixed command used only by the Linux isolation E2E test. */
#include <fcntl.h>
#include <unistd.h>
int main(void) {
	const char message[] = "fixed command\n";
	int marker = open("command-ran", O_WRONLY | O_CREAT | O_TRUNC, 0600);
	if (marker == -1 || write(marker, message, sizeof(message) - 1) != sizeof(message) - 1 || close(marker) == -1) return 1;
	return write(1, message, sizeof(message) - 1) == sizeof(message) - 1 ? 0 : 1;
}
