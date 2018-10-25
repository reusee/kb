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

	timeout := 40

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
			timeout int,
		)

		var doubleShiftToCtrl stateFunc
		doubleShiftToCtrl = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int) {
			if ev._type != C.EV_KEY {
				return nil, false, 0
			}
			if ev.value != 1 {
				return nil, false, 0
			}
			if ev.code != C.KEY_LEFTSHIFT && ev.code != C.KEY_RIGHTSHIFT {
				return nil, false, 0
			}
			code := ev.code
			var waitNextShift stateFunc
			waitNextShift = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int) {
				if ev._type != C.EV_KEY {
					return waitNextShift, false, timeout
				}
				if ev.value != 1 {
					/*
						key press -> shift press -> shift release -> key release
						这个序列会导致 key release 事件被延迟，会产生 key repeat
						所以在 key release 时，中止匹配
					*/
					if ev.code != code {
						return nil, false, 0
					}
					return waitNextShift, false, timeout
				}
				if ev.code != code {
					return nil, false, 0
				}
				var waitNextKey stateFunc
				waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int) {
					if ev._type != C.EV_KEY {
						return waitNextKey, false, timeout
					}
					if ev.value != 1 {
						return waitNextKey, false, timeout
					}
					if ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT {
						return nil, false, 0
					}
					writeEv(ctrlPress)
					writeEv(raw)
					writeEv(ctrlRelease)
					return nil, true, 0
				}
				return waitNextKey, false, timeout
			}
			return waitNextShift, false, timeout
		}

		var capslockToMeta stateFunc
		capslockToMeta = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int) {
			if ev._type != C.EV_KEY {
				return nil, false, 0
			}
			if ev.value != 1 {
				return nil, false, 0
			}
			if ev.code != C.KEY_CAPSLOCK {
				return nil, false, 0
			}
			var waitNextKey stateFunc
			waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int) {
				if ev._type != C.EV_KEY {
					return waitNextKey, false, timeout
				}
				if ev.value != 1 {
					/*
						key press -> capslock press -> capslock release -> key release
						这个序列会导致 key release 事件被延迟，产生 key repeat
						所以在 key release 时，写入该 release 事件，并返回匹配成功
					*/
					if ev.code != C.KEY_CAPSLOCK {
						writeEv(raw)
						return nil, true, 0
					}
					return waitNextKey, false, timeout
				}
				if ev.code == C.KEY_CAPSLOCK {
					return nil, true, 0
				}
				writeEv(metaPress)
				writeEv(raw)
				writeEv(metaRelease)
				return nil, true, 0
			}
			return waitNextKey, false, timeout
		}

		fns := []stateFunc{
			doubleShiftToCtrl,
			capslockToMeta,
		}
		type State struct {
			fn      stateFunc
			timeout int
		}
		var states []*State
		var delayed [][]byte

		type Event struct {
			ev  *C.struct_input_event
			raw []byte
		}
		evCh := make(chan Event, 128)

		go func() {
			for {
				raw := make([]byte, unsafe.Sizeof(C.struct_input_event{}))
				if _, err := syscall.Read(keyboardFD, raw); err != nil {
					panic(err)
				}
				ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))
				evCh <- Event{
					ev:  ev,
					raw: raw,
				}
			}
		}()

		ticker := time.NewTicker(time.Millisecond * 10)

		for {
			pt("%+v\n", states)
			pt("%+v\n", delayed)
			select {

			case ev := <-evCh:
				var newStates []*State
				hasPartialMatch := false
				hasFullMatch := false
				for _, fn := range fns {
					next, full, t := fn(ev.ev, ev.raw)
					if full {
						hasFullMatch = true
					} else if next != nil {
						hasPartialMatch = true
						newStates = append(newStates, &State{
							fn:      next,
							timeout: t,
						})
					}
				}
				for _, state := range states {
					next, full, t := state.fn(ev.ev, ev.raw)
					if full {
						hasFullMatch = true
					} else if next != nil {
						hasPartialMatch = true
						newStates = append(newStates, &State{
							fn:      next,
							timeout: t,
						})
					}
				}
				states = newStates
				if hasPartialMatch {
					// delay key
					delayed = append(delayed, ev.raw)
				} else if hasFullMatch {
					// clear delayed key
					delayed = delayed[0:0:cap(delayed)]
				} else {
					// pop keys
					for _, r := range delayed {
						writeEv(r)
					}
					delayed = delayed[0:0:cap(delayed)]
					writeEv(ev.raw)
				}

			case <-func() <-chan time.Time {
				if len(states) > 0 || len(delayed) > 0 {
					return ticker.C
				}
				return nil
			}():
				for i := 0; i < len(states); {
					state := states[i]
					state.timeout--
					if state.timeout == 0 {
						// delete
						copy(
							states[i:len(states)-1],
							states[i+1:len(states)],
						)
						states = states[: len(states)-1 : cap(states)]
						continue
					}
					i++
				}
				if len(states) == 0 {
					// pop
					for _, r := range delayed {
						writeEv(r)
					}
					delayed = delayed[0:0:cap(delayed)]
				}

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
