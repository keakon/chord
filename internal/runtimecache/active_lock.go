package runtimecache

import "os"

type sessionActiveLock struct {
	file *os.File
}
