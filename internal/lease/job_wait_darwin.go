//go:build darwin

package lease

import (
	"errors"

	ownerpkg "github.com/Dematom1/berth/internal/owner"
	"golang.org/x/sys/unix"
)

func childExited(pid int) (bool, error) {
	_, state := ownerpkg.ProcessIdentity(pid)
	if state == ownerpkg.Unknown {
		return false, errors.New("cannot inspect child process")
	}
	return state == ownerpkg.Dead, nil
}

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
