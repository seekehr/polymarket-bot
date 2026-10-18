//go:build !windows

package logging

// enableVirtualTerminal is a no-op outside Windows: Unix terminals interpret
// ANSI codes natively.
func enableVirtualTerminal(fd uintptr) bool { return true }
