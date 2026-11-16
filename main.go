package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

func main() {
	configPath := flag.String("c", "config.yaml", "配置文件路径")
	reloadInterval := flag.Duration("reload", 2*time.Second, "配置热加载检测间隔（0 关闭）")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	mgr, err := NewManager(cfg, *configPath)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	// 配置热加载：自动检测文件变化（mtime）
	if *reloadInterval > 0 {
		mgr.watchReload(*reloadInterval)
	}
	// SIGHUP 手动重载（Unix；Windows 为空实现）
	setupReloadSignal(mgr)

	udpSrv := &dns.Server{
		Addr:    cfg.Listen,
		Net:     "udp",
		Handler: dns.HandlerFunc(mgr.Handle),
	}

	// 优雅退出
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("收到退出信号，关闭中...")
		mgr.current().health.Stop()
		os.Exit(0)
	}()

	log.Printf("dns-gtm 监听 %s (udp)", cfg.Listen)
	if err := udpSrv.ListenAndServe(); err != nil {
		log.Fatalf("udp 监听失败: %v", err)
	}
}
