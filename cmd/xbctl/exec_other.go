//go:build !linux

package main

import "fmt"

// execManagedProcess 在非 Linux 系统明确拒绝服务启动。
func execManagedProcess(path string, args, env []string) error {
	return fmt.Errorf("服务启动仅支持 Linux")
}
