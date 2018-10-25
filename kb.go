package main

/*
#include <linux/uinput.h>
#include <string.h>
#include "kb.h"
*/
import "C"

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	pt = fmt.Printf
)

func main() {

	var KeyboardPath string
	names, err := filepath.Glob("/dev/input/by-id/*")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		if !strings.Contains(name, "event") {
			continue
		}
		fd, err := syscall.Open(name, syscall.O_RDONLY, 0644)
		if err != nil {
			continue
		}
		bits := make([]byte, C.EV_MAX)
		ctl(
			fd,
			uintptr(C.eviocgbit(0, C.EV_MAX)),
			uintptr(unsafe.Pointer(&bits[0])),
		)
		syscall.Close(fd)
		if testBit(C.EV_REP, bits) {
			KeyboardPath = name
			break
		}
	}
	if KeyboardPath == "" {
		panic("no keyboard")
	}
	pt("%s\n", KeyboardPath)

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

	interval := time.Millisecond * 400

	go func() {
		ctrlPress := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 1)
		ctrlRelease := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 0)
		metaPress := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 1)
		metaRelease := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 0)

		type stateFunc func(
			ev *C.struct_input_event,
			raw []byte,
		) (
			next stateFunc,
			full bool,
		)

		var doubleShiftToCtrl stateFunc
		doubleShiftToCtrl = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool) {
			if ev._type != C.EV_KEY {
				return nil, false
			}
			if ev.value != 1 {
				return nil, false
			}
			if ev.code != C.KEY_LEFTSHIFT && ev.code != C.KEY_RIGHTSHIFT {
				return nil, false
			}
			t := time.Now()
			code := ev.code
			var waitNextShift stateFunc
			waitNextShift = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool) {
				if ev._type != C.EV_KEY {
					return waitNextShift, false
				}
				if ev.value != 1 {
					/*
						key press -> shift press -> shift release -> key release
						这个序列会导致 key release 事件被延迟，会产生 key repeat
						所以在 key release 时，中止匹配
					*/
					if ev.code != code {
						return nil, false
					}
					return waitNextShift, false
				}
				if time.Since(t) > interval {
					return nil, false
				}
				if ev.code != code {
					return nil, false
				}
				t := time.Now()
				var waitNextKey stateFunc
				waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool) {
					if ev._type != C.EV_KEY {
						return waitNextKey, false
					}
					if ev.value != 1 {
						return waitNextKey, false
					}
					if time.Since(t) > interval {
						return nil, false
					}
					if ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT {
						return nil, false
					}
					writeEv(ctrlPress)
					writeEv(raw)
					writeEv(ctrlRelease)
					return nil, true
				}
				return waitNextKey, false
			}
			return waitNextShift, false
		}

		var capslockToMeta stateFunc
		capslockToMeta = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool) {
			if ev._type != C.EV_KEY {
				return nil, false
			}
			if ev.value != 1 {
				return nil, false
			}
			if ev.code != C.KEY_CAPSLOCK {
				return nil, false
			}
			t := time.Now()
			var waitNextKey stateFunc
			waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool) {
				if ev._type != C.EV_KEY {
					return waitNextKey, false
				}
				if ev.value != 1 {
					/*
						key press -> capslock press -> capslock release -> key release
						这个序列会导致 key release 事件被延迟，产生 key repeat
						所以在 key release 时，写入该 release 事件，并返回匹配成功
					*/
					if ev.code != C.KEY_CAPSLOCK {
						writeEv(raw)
						return nil, true
					}
					return waitNextKey, false
				}
				if time.Since(t) > interval {
					return nil, true
				}
				if ev.code == C.KEY_CAPSLOCK {
					return nil, true
				}
				writeEv(metaPress)
				writeEv(raw)
				writeEv(metaRelease)
				return nil, true
			}
			return waitNextKey, false
		}

		fns := []stateFunc{
			doubleShiftToCtrl,
			capslockToMeta,
		}
		var states []stateFunc
		var delayed [][]byte

		for {
			raw := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
			if _, err := syscall.Read(keyboardFD, raw); err != nil {
				panic(err)
			}
			ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))

			var newStates []stateFunc
			hasPartialMatch := false
			hasFullMatch := false
			for _, fn := range fns {
				state, full := fn(ev, raw)
				if full {
					hasFullMatch = true
				} else if state != nil {
					hasPartialMatch = true
					newStates = append(newStates, state)
				}
			}
			for _, fn := range states {
				state, full := fn(ev, raw)
				if full {
					hasFullMatch = true
				} else if state != nil {
					hasPartialMatch = true
					newStates = append(newStates, state)
				}
			}
			states = newStates
			if hasPartialMatch {
				// delay key
				delayed = append(delayed, raw)
			} else if hasFullMatch {
				// clear delayed key
				delayed = delayed[0:0:cap(delayed)]
			} else {
				// pop keys
				for _, r := range delayed {
					writeEv(r)
				}
				delayed = delayed[0:0:cap(delayed)]
				writeEv(raw)
			}

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

func ctl(fd int, a1, a2 uintptr) {
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		a1,
		a2,
	)
	if errno != 0 {
		C.pe()
		panic("syscall")
	}
}

func testBit(n uint, bits []byte) bool {
	return bits[n/8]&(1<<(n%8)) > 1
}
