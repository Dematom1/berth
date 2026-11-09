//go:build darwin

package main

import "golang.org/x/sys/unix"

func waitChildExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	for {
		_, err = unix.Kevent(kq, []unix.Kevent_t{change}, make([]unix.Kevent_t, 1), nil)
		if err != unix.EINTR {
			return err
		}
	}
}
