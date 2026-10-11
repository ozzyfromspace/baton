//go:build windows

package lane

// The tests that use these skip on Windows (needSh).
func running(int) bool { return false }
func pgid(int) int     { return 0 }
