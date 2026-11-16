//go:build windows

package main

// Windows 无 SIGHUP，热加载由 mtime 自动检测覆盖
func setupReloadSignal(m *Manager) {}
