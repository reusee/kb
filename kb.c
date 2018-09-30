#include <linux/uinput.h>
#include <stdlib.h>
#include <stdio.h>

unsigned long* get_mask(unsigned long fd) {
	unsigned long *mask = malloc(
		(KEY_MAX+(sizeof(unsigned long)*8)-1)/(sizeof(unsigned long)*8)
	);
	ioctl(fd, EVIOCGBIT(EV_KEY, KEY_MAX), mask);
	return mask;
}

void set_mask(unsigned long fd, unsigned long *mask) {
	int key;
	for (key = KEY_RESERVED; key <= KEY_MAX; key++) {
		if ((mask[key / (sizeof(unsigned long) * 8)] >> (key % (sizeof(unsigned long) * 8))) & 1) {
			ioctl(fd, UI_SET_KEYBIT, key);
		}
	}
}

unsigned int eviocgbit(int a, int b) {
	return EVIOCGBIT(a, b);
}

void pe() {
	perror("error");
}

