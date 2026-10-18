//go:build windows

package logging

import "golang.org/x/sys/windows"

// enableVirtualTerminal turns on ANSI escape processing for a Windows console.
// It returns false on consoles that predate it (before Windows 10 1511), in
// which case escape codes would be printed literally.
func enableVirtualTerminal(fd uintptr) bool {
	h := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
