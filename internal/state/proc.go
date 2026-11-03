// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"os"
	"strconv"
	"strings"
)

// clockTicks is USER_HZ, the unit of utime/stime in /proc/<pid>/stat (100 on every Linux we run on).
const clockTicks = 100

// ReadProc returns this process's resident memory (KB) and the CPU time it used (seconds).
func ReadProc() (rssKB int64, cpuSec float64) {
	return readProc("/proc/self/status", "/proc/self/stat")
}

func readProc(status, stat string) (rssKB int64, cpuSec float64) {
	if b, err := os.ReadFile(status); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
				f := strings.Fields(v)
				if len(f) > 0 {
					rssKB, _ = strconv.ParseInt(f[0], 10, 64)
				}
			}
		}
	}
	if b, err := os.ReadFile(stat); err == nil {
		// the name in field 2 may hold spaces and brackets: count from the last ")"
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i >= 0 {
			f := strings.Fields(s[i+1:])
			if len(f) > 13 { // state is f[0] = field 3; utime and stime are fields 14 and 15
				u, _ := strconv.ParseInt(f[11], 10, 64)
				k, _ := strconv.ParseInt(f[12], 10, 64)
				cpuSec = float64(u+k) / clockTicks
			}
		}
	}
	return
}
