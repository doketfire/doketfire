//go:build !windows

package main

import "time"

func clockNow() int64 {
	return time.Now().UnixNano()
}
