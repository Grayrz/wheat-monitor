//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modKernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = modKernel32.NewProc("GetConsoleMode")
	procWriteConsoleW  = modKernel32.NewProc("WriteConsoleW")
)

// setupConsole 让中文输出直接走 Unicode 接口。
// 不再切换代码页：chcp 65001 在部分 Windows 的 conhost 上会把每个汉字显示两次，
// 而保持 GBK(936) 又会让 Go 的 UTF-8 字节变成乱码。WriteConsoleW 绕过代码页，两头都对。
func setupConsole() {
	h := os.Stdout.Fd()
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return // 输出被重定向到文件/管道，保持普通 UTF-8 写入
	}
	consoleWriter = func(s string) bool {
		p, err := syscall.UTF16FromString(s)
		if err != nil || len(p) <= 1 {
			return false
		}
		var n uint32
		r, _, _ := procWriteConsoleW.Call(h,
			uintptr(unsafe.Pointer(&p[0])),
			uintptr(len(p)-1),
			uintptr(unsafe.Pointer(&n)), 0)
		return r != 0
	}
}
