//go:build windows

package commands

import "syscall"

// detachedSysProcAttr has no session concept to use on Windows; the child is
// simply started without inherited handles.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
