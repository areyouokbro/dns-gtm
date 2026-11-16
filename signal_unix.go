//go:build !windows

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// setupReloadSignal 注册 SIGHUP：收到后重新加载配置（systemd reload / kill -HUP）
func setupReloadSignal(m *Manager) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
			if err := m.reload(); err != nil {
				log.Printf("配置重载失败: %v", err)
			} else {
				log.Println("收到 SIGHUP，配置已重载")
			}
		}
	}()
}
