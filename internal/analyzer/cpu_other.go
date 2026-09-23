//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package analyzer

import "time"

func processCPUTime() time.Duration { return 0 }
