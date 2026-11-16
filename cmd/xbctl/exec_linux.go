package main

import "syscall"

// execManagedProcess 替换当前进程，使监督器直接管理主程序。
func execManagedProcess(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}
