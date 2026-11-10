//go:build linux

package lease

import (
	"golang.org/x/sys/unix"
)

func childExited(pid int) (bool, error) {
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil); err != nil {
		return false, err
	}
	return info.Signo != 0, nil
}

func waitChildExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
