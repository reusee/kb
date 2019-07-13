package main

/*
#include <linux/uinput.h>
#include <sys/timerfd.h>
#include <malloc.h>
#include <fcntl.h>
#include <string.h>
#include <unistd.h>

unsigned int eviocgbit(int a, int b) {
	return EVIOCGBIT(a, b);
}

int setup_uinput(int keyboard_fd) {
  struct uinput_setup usetup;
  int fd = open("/dev/uinput", O_WRONLY | O_NONBLOCK);
  ioctl(fd, UI_SET_EVBIT, EV_KEY);
  int key;
	unsigned long *mask = malloc(
		(KEY_MAX+(sizeof(unsigned long)*8)-1)/(sizeof(unsigned long)*8)
	);
	ioctl(keyboard_fd, EVIOCGBIT(EV_KEY, KEY_MAX), mask);
  for (key = KEY_RESERVED; key <= KEY_MAX; key++) {
    if ((mask[key / (sizeof(unsigned long) * 8)] >> (key % (sizeof(unsigned long) * 8))) & 1) {
      ioctl(fd, UI_SET_KEYBIT, key);
    }
  }
  memset(&usetup, 0, sizeof(usetup));
  usetup.id.bustype = BUS_USB;
  usetup.id.vendor = 0xdead;
  usetup.id.product = 0xbeef;
  strcpy(usetup.name, "keyboard");
  ioctl(fd, UI_DEV_SETUP, &usetup);
  ioctl(fd, UI_DEV_CREATE);
  sleep(1);
  return fd;
}

void close_uinput(int fd) {
  ioctl(fd, UI_DEV_DESTROY);
  close(fd);
}

*/
import "C"

import (
	"fmt"
	"math"
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

	uinputFD := C.setup_uinput(C.int(keyboardFD))
	defer C.close_uinput(uinputFD)

	writeEv := func(raw []byte) {

		// filter
		ev := (*C.struct_input_event)(unsafe.Pointer(&raw[0]))
		if ev.code == C.KEY_CAPSLOCK {
			return
		}

		if _, err := syscall.Write(int(uinputFD), raw); err != nil {
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

	tickDuration := time.Millisecond * 3
	timeoutDuration := time.Millisecond * 200
	timeout := int(math.Floor(float64(timeoutDuration) / float64(tickDuration)))

	go func() {
		ctrlPress := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 1)
		ctrlRelease := rawEvent(C.EV_KEY, C.KEY_LEFTCTRL, 0)
		metaPress := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 1)
		metaRelease := rawEvent(C.EV_KEY, C.KEY_LEFTMETA, 0)

		type timeoutFn func() (
			full bool,
		)
		type stateFunc func(
			ev *C.struct_input_event,
			raw []byte,
		) (
			next stateFunc,
			full bool,
			timeout int,
			timeoutFn timeoutFn,
		)

		timeoutNoMatch := timeoutFn(func() bool {
			return false
		})
		timeoutFullMatch := timeoutFn(func() bool {
			return true
		})

		var doubleShiftToCtrl stateFunc
		doubleShiftToCtrl = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int, timeoutFn) {
			if ev._type != C.EV_KEY {
				return nil, false, 0, nil
			}
			if ev.value != 1 {
				return nil, false, 0, nil
			}
			if ev.code != C.KEY_LEFTSHIFT && ev.code != C.KEY_RIGHTSHIFT {
				return nil, false, 0, nil
			}
			code := ev.code
			var waitNextShift stateFunc
			waitNextShift = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int, timeoutFn) {
				if ev._type != C.EV_KEY {
					return waitNextShift, false, timeout, timeoutNoMatch
				}
				if ev.value != 1 {
					/*
						key press -> shift press -> shift release -> key release
						这个序列会导致 key release 事件被延迟，会产生 key repeat
						所以在 key release 时，中止匹配
					*/
					if ev.code != code {
						return nil, false, 0, nil
					}
					return waitNextShift, false, timeout, timeoutNoMatch
				}
				if ev.code != code {
					return nil, false, 0, nil
				}
				var waitNextKey stateFunc
				waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int, timeoutFn) {
					if ev._type != C.EV_KEY {
						return waitNextKey, false, timeout, timeoutNoMatch
					}
					if ev.value != 1 {
						return waitNextKey, false, timeout, timeoutNoMatch
					}
					if ev.code == C.KEY_LEFTSHIFT || ev.code == C.KEY_RIGHTSHIFT {
						return nil, false, 0, nil
					}
					writeEv(ctrlPress)
					writeEv(raw)
					writeEv(ctrlRelease)
					return nil, true, 0, nil
				}
				return waitNextKey, false, timeout, timeoutNoMatch
			}
			return waitNextShift, false, timeout, timeoutNoMatch
		}

		var capslockToMeta stateFunc
		capslockToMeta = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int, timeoutFn) {
			if ev._type != C.EV_KEY {
				return nil, false, 0, nil
			}
			if ev.value != 1 {
				return nil, false, 0, nil
			}
			if ev.code != C.KEY_CAPSLOCK {
				return nil, false, 0, nil
			}
			var waitNextKey stateFunc
			waitNextKey = func(ev *C.struct_input_event, raw []byte) (stateFunc, bool, int, timeoutFn) {
				if ev._type != C.EV_KEY {
					return waitNextKey, false, timeout, timeoutFullMatch
				}
				if ev.value != 1 {
					/*
						key press -> capslock press -> capslock release -> key release
						这个序列会导致 key release 事件被延迟，产生 key repeat
						所以在 key release 时，写入该 release 事件，并返回匹配成功
					*/
					if ev.code != C.KEY_CAPSLOCK {
						writeEv(raw)
						return nil, true, 0, nil
					}
					return waitNextKey, false, timeout, timeoutFullMatch
				}
				if ev.code == C.KEY_CAPSLOCK {
					return nil, true, 0, nil
				}
				writeEv(metaPress)
				writeEv(raw)
				writeEv(metaRelease)
				return nil, true, 0, nil
			}
			return waitNextKey, false, timeout, timeoutFullMatch
		}

		fns := []stateFunc{
			doubleShiftToCtrl,
			capslockToMeta,
		}
		type State struct {
			fn        stateFunc
			timeout   int
			timeoutFn timeoutFn
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

		ticker := time.NewTicker(tickDuration)

		for {
			//pt("STATES: %+v\n", states)
			//pt("DELAYED: %+v\n", delayed)
			select {

			case ev := <-evCh:
				var newStates []*State
				hasPartialMatch := false
				hasFullMatch := false
				for _, fn := range fns {
					next, full, t, timeoutFn := fn(ev.ev, ev.raw)
					if full {
						hasFullMatch = true
					} else if next != nil {
						hasPartialMatch = true
						newStates = append(newStates, &State{
							fn:        next,
							timeout:   t,
							timeoutFn: timeoutFn,
						})
					}
				}
				for _, state := range states {
					next, full, t, timeoutFn := state.fn(ev.ev, ev.raw)
					if full {
						hasFullMatch = true
					} else if next != nil {
						hasPartialMatch = true
						newStates = append(newStates, &State{
							fn:        next,
							timeout:   t,
							timeoutFn: timeoutFn,
						})
					}
				}
				states = newStates
				if hasPartialMatch {
					// delay key
					delayed = append(delayed, ev.raw)
				}
				if hasFullMatch {
					// clear delayed key
					delayed = delayed[0:0:cap(delayed)]
				}
				if !hasPartialMatch && !hasFullMatch {
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
				hasFullMatch := false
				for i := 0; i < len(states); {
					state := states[i]
					state.timeout--
					if state.timeout == 0 {
						// timeoutFn
						hasFullMatch = hasFullMatch || state.timeoutFn()
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
					if !hasFullMatch {
						// pop
						for _, r := range delayed {
							writeEv(r)
						}
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
		panic(errno.Error())
	}
}

func testBit(n uint, bits []byte) bool {
	return bits[n/8]&(1<<(n%8)) > 1
}
