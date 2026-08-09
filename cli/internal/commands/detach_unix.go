//go:build !windows

package commands

import "syscall"

// detachedSysProcAttr puts the child in its own session so it is reparented
// to init and survives the hook process exiting.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
