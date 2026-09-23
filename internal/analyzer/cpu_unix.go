//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package analyzer

import (
	"syscall"
	"time"
)

func processCPUTime() time.Duration {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
