package main

/*
#include <linux/uinput.h>
#include <stdlib.h>
#include <string.h>
#include <fcntl.h>
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
	KeyboardPath = "/dev/input/by-id/usb-CATEX_TECH._87EC-S_CA2015090001-event-kbd"
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

	writeEv := func(raw []byte) {
		if _, err := syscall.Write(uinputFD, raw); err != nil {
			panic(err)
		}
	}

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
		ctrlPress := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 1)
		ctrlRelease := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 0)
		metaPress := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 1)
		metaRelease := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 0)

		type stateFunc func(ev *C.struct_input_event, raw []byte) bool

		doubleShiftToCtrl := func() stateFunc {
			state := 0
			var t time.Time
			var code C.ushort
			return func(ev *C.struct_input_event, raw []byte) bool {
				if ev._type != C.EV_KEY {
					return false
				}
				if ev.value != 1 {
					return false
				}
				switch state {
				case 0:
					if ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT {
						state = 1
						t = time.Now()
						code = ev.code
					}
				case 1:
					if time.Since(t) < interval && ev.code == code {
						state = 2
						t = time.Now()
						return true
					} else {
						state = 0
					}
				case 2:
					state = 0
					if time.Since(t) < interval {
						writeEv(ctrlPress)
						writeEv(raw)
						writeEv(ctrlRelease)
						return true
					}
				}
				return false
			}
		}()

		shiftToMeta := func() stateFunc {
			state := 0
			var t time.Time
			var code C.ushort
			return func(ev *C.struct_input_event, raw []byte) bool {
				if ev._type != C.EV_KEY {
					return false
				}
				if ev.value != 1 {
					return false
				}
				switch state {
				case 0:
					if ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT {
						state = 1
						t = time.Now()
						code = ev.code
					}
				case 1:
					if time.Since(t) < interval && (ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT) && ev.code != code {
						state = 2
						t = time.Now()
						return true // ignore this shift press
					} else {
						state = 0
					}
				case 2:
					state = 0
					if time.Since(t) < interval {
						writeEv(metaPress)
						writeEv(raw)
						writeEv(metaRelease)
						return true
					}
				}
				return false
			}
		}()

		raw := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
	next_key:
		for {
			if _, err := syscall.Read(keyboardFD, raw); err != nil {
				panic(err)
			}
			ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))
			for _, fn := range []stateFunc{
				doubleShiftToCtrl,
				shiftToMeta,
			} {
				if fn(ev, raw) {
					continue next_key
				}
			}
			writeEv(raw)
		}

	}()

	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGKILL)
	<-sigs
}

func rawEvent(_type C.ushort, code C.ushort, value C.int) []byte {
	raw := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
	ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))
	ev._type = _type
	ev.code = code
	ev.value = value
	return raw
}
