//go:build !linux

package stats

func newWindowsHostSource() windowsHostSource { return nil }
