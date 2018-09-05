package main

/*
#include <linux/uinput.h>
#include <stdlib.h>
#include <unistd.h>
#include <stdio.h>
#include <string.h>
#include <fcntl.h>

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

*/
import "C"

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"
)

const (
	KeyboardPath = "/dev/input/by-id/usb-CATEX_TECH._84EC-S_CA2017090002-event-kbd"
)

var (
	pt = fmt.Printf
)

func main() {

	keyboardFD, err := syscall.Open(KeyboardPath, syscall.O_RDONLY, 0644)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(keyboardFD)

	mask := C.get_mask(C.ulong(keyboardFD))

	uinputFD, err := syscall.Open("/dev/uinput", syscall.O_WRONLY|syscall.O_NONBLOCK, 0644)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(uinputFD)

	ctl := func(fd int, a1, a2 uintptr) {
		_, _, errno := syscall.Syscall(
			syscall.SYS_IOCTL,
			uintptr(fd),
			a1,
			a2,
		)
		if errno != 0 {
			panic("syscall")
		}
	}

	ctl(
		uinputFD,
		C.UI_SET_EVBIT,
		C.EV_KEY,
	)
	C.set_mask(C.ulong(uinputFD), mask)
	var usetup C.struct_uinput_setup
	usetup.id.bustype = C.BUS_USB
	usetup.id.vendor = 0xdead
	usetup.id.product = 0xbeef
	C.strcpy(
		&usetup.name[0],
		C.CString("foo"),
	)
	ctl(
		uinputFD,
		C.UI_DEV_SETUP,
		uintptr(unsafe.Pointer(&usetup)),
	)
	ctl(
		uinputFD,
		C.UI_DEV_CREATE,
		0,
	)
	defer func() {
		ctl(
			uinputFD,
			C.UI_DEV_DESTROY,
			0,
		)
	}()

	ctl(
		keyboardFD,
		C.EVIOCGRAB,
		1,
	)
	defer func() {
		ctl(
			keyboardFD,
			C.EVIOCGRAB,
			0,
		)
	}()

	interval := time.Millisecond * 800

	go func() {
		ctrlPress := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
		ev := (*C.struct_input_event)(unsafe.Pointer(&ctrlPress[0]))
		ev._type = C.EV_KEY
		ev.code = C.KEY_LEFTCTRL
		ev.value = 1
		ctrlRelease := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
		ev = (*C.struct_input_event)(unsafe.Pointer(&ctrlRelease[0]))
		ev._type = C.EV_KEY
		ev.code = C.KEY_LEFTCTRL
		ev.value = 0

		type state func(ev *C.struct_input_event, raw []byte) bool

		doubleShiftToCtrl := func() state {
			state := 0
			var t time.Time
			var code C.ushort
			return func(ev *C.struct_input_event, raw []byte) bool {
				if ev._type != C.EV_KEY {
					return false
				}
				switch state {
				case 0:
					if ev.code == C.KEY_LEFTSHIFT && ev.value == 1 ||
						ev.code == C.KEY_RIGHTSHIFT && ev.value == 1 {
						state = 1
						t = time.Now()
						code = ev.code
					}
				case 1:
					if ev.code == code && ev.value == 0 && time.Since(t) < interval {
						state = 2
						t = time.Now()
					} else {
						state = 0
					}
				case 2:
					if ev.code == code && ev.value == 1 && time.Since(t) < interval {
						state = 3
						t = time.Now()
						return true // ignore shift press
					} else {
						state = 0
					}
				case 3:
					if ev.code == code && ev.value == 0 && time.Since(t) < interval {
						state = 3
						t = time.Now()
					} else {
						state = 0
						if time.Since(t) < time.Second {
							if _, err := syscall.Write(uinputFD, ctrlPress); err != nil {
								panic(err)
							}
							if _, err := syscall.Write(uinputFD, raw); err != nil {
								panic(err)
							}
							if _, err := syscall.Write(uinputFD, ctrlRelease); err != nil {
								panic(err)
							}
							return true
						}
					}
				}
				return false
			}
		}()

		raw := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
		for {
			if _, err := syscall.Read(keyboardFD, raw); err != nil {
				panic(err)
			}
			ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))
			pt("%+v\n", ev)
			if doubleShiftToCtrl(ev, raw) {
				continue
			}
			if _, err := syscall.Write(uinputFD, raw); err != nil {
				panic(err)
			}
		}
	}()

	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGKILL)
	<-sigs
}
