/* Static fixed command used only by the Docker qualification. */
#include <unistd.h>
int main(void) {
	const char message[] = "fixed command\n";
	return write(1, message, sizeof(message) - 1) == sizeof(message) - 1 ? 0 : 1;
}
