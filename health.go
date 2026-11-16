package main

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type healthTarget struct {
	id       string
	kind     string // tcp / http / dns
	value    string
	port     int
	url      string
	interval time.Duration // 当前有效探测间隔（失败会退避增大）
	failCnt  int
	riseCnt  int
	last     time.Time
	up       bool
}

// HealthChecker 后台健康检查器。
// 核心设计：探测完全在查询路径之外，独立 goroutine 定时执行；
// 查询只通过 Snapshot() 原子读取健康状态快照，O(1) 且零探测开销。
type HealthChecker struct {
	cfg      ProbeConfig
	mu       sync.Mutex
	targets  map[string]*healthTarget
	states    atomic.Pointer[map[string]bool] // 健康状态快照
	resolver  *dns.Client
	resolvers []string     // 上游 DNS 列表（CNAME 目标解析用）
	rrIdx     uint64       // 上游轮询起始索引
	limiter   *tokenBucket // 仅约束“上游 DNS 解析型探测”的速率
	stopCh    chan struct{}
	doneCh    chan struct{}
}

func NewHealthChecker(cfg ProbeConfig) *HealthChecker {
	initial := map[string]bool{}
	h := &HealthChecker{
		cfg:       cfg,
		targets:   make(map[string]*healthTarget),
		resolver:  &dns.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		resolvers: cfg.UpstreamResolvers,
		limiter:   newTokenBucket(cfg.DNSMaxQPS),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	h.states.Store(&initial)
	return h
}

// Register 在 Start 之前注册探测目标，初始视为存活
func (h *HealthChecker) Register(t *healthTarget) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t.interval = time.Duration(h.cfg.Interval) * time.Second
	t.up = true
	h.targets[t.id] = t

	st := *h.states.Load()
	cp := make(map[string]bool, len(st)+1)
	for k, v := range st {
		cp[k] = v
	}
	cp[t.id] = true
	h.states.Store(&cp)
}

func (h *HealthChecker) Start() {
	tick := time.Duration(h.cfg.Interval) * time.Second
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-h.stopCh:
				close(h.doneCh)
				return
			case <-ticker.C:
				h.sweep()
			}
		}
	}()
}

func (h *HealthChecker) Stop() {
	close(h.stopCh)
	<-h.doneCh
}

// Snapshot 返回当前健康状态快照（只读使用）
func (h *HealthChecker) Snapshot() map[string]bool {
	return *h.states.Load()
}

// sweep 每轮收集到期目标，用带并发上限的 worker 池执行探测
func (h *HealthChecker) sweep() {
	now := time.Now()

	h.mu.Lock()
	due := make([]*healthTarget, 0)
	for _, t := range h.targets {
		if now.Sub(t.last) < t.interval {
			continue
		}
		// 上游 DNS 解析型探测做全局限速，超限则本轮跳过，防止打爆上游被 ban
		if t.kind == "dns" && !h.limiter.Allow() {
			continue
		}
		t.last = now // 标记已调度，避免探测慢时重复调度
		due = append(due, t)
	}
	h.mu.Unlock()

	if len(due) == 0 {
		return
	}

	sem := make(chan struct{}, h.cfg.MaxConcurrent)
	var wg sync.WaitGroup
	for _, t := range due {
		wg.Add(1)
		go func(t *healthTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.probeOne(t)
		}(t)
	}
	wg.Wait()
}

func (h *HealthChecker) probeOne(t *healthTarget) {
	ok := h.doProbe(t)

	min := time.Duration(h.cfg.DNSBackoffMin) * time.Second
	max := time.Duration(h.cfg.DNSBackoffMax) * time.Second

	h.mu.Lock()
	t.last = time.Now()
	switch {
	case ok:
		t.failCnt = 0
		if !t.up {
			t.riseCnt++
			if t.riseCnt >= h.cfg.RiseThreshold {
				t.up = true
				t.riseCnt = 0
				t.interval = time.Duration(h.cfg.Interval) * time.Second // 恢复后退避复位
			}
		}
	case !ok:
		t.riseCnt = 0
		if t.up {
			t.failCnt++
			if t.failCnt >= h.cfg.FailThreshold {
				t.up = false
				t.failCnt = 0
			}
		}
		if !t.up {
			// 故障后端持续退避，探测越来越稀，直到封顶
			t.interval = t.interval * 2
			if t.interval < min {
				t.interval = min
			}
			if t.interval > max {
				t.interval = max
			}
		}
	}
	up := t.up
	h.mu.Unlock()

	h.setUp(t.id, up)
}

func (h *HealthChecker) doProbe(t *healthTarget) bool {
	timeout := time.Duration(h.cfg.Timeout) * time.Second
	switch t.kind {
	case "tcp":
		addr := net.JoinHostPort(t.value, strconv.Itoa(t.port))
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true

	case "http":
		client := &http.Client{Timeout: timeout}
		resp, err := client.Get(t.url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 400

	case "dns":
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(t.value), dns.TypeA)
		n := len(h.resolvers)
		if n == 0 {
			return false
		}
		// 多上游：轮询起始，某上游不可达则故障转移到下一个
		start := int(atomic.AddUint64(&h.rrIdx, 1)-1) % n
		for i := 0; i < n; i++ {
			resolver := h.resolvers[(start+i)%n]
			resp, _, err := h.resolver.Exchange(m, resolver)
			if err != nil {
				continue // 该上游不可达，试下一个
			}
			// 上游有明确响应：以响应判定该目标是否健康
			if resp.Rcode != dns.RcodeSuccess {
				return false
			}
			for _, rr := range resp.Answer {
				switch rr.(type) {
				case *dns.A, *dns.AAAA, *dns.CNAME:
					return true
				}
			}
			return false
		}
		return false // 所有上游都不可达
	}
	return false
}

func (h *HealthChecker) setUp(id string, up bool) {
	st := *h.states.Load()
	if st[id] == up {
		return
	}
	cp := make(map[string]bool, len(st))
	for k, v := range st {
		cp[k] = v
	}
	cp[id] = up
	h.states.Store(&cp)
	log.Printf("健康状态变化 %s up=%v", id, up)
}

// tokenBucket 简单令牌桶，用于限制上游 DNS 解析速率
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	last   time.Time
}

func newTokenBucket(rate float64) *tokenBucket {
	return &tokenBucket{rate: rate, tokens: 1, last: time.Now()}
}

func (b *tokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > 1 {
		b.tokens = 1
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
