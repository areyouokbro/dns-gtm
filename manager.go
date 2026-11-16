package main

import (
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type runtimeRecord struct {
	rec       Record
	pool      *wrrPool
	targetIDs []string // 与 rec.Targets 对齐，健康状态快照的 key
}

type wrrPool struct {
	mu      sync.Mutex
	current []int // 平滑加权轮询的累计值，与 targets 对齐
}

// state 是热加载的最小切换单元：一次 reload 原子替换整个 state。
// 查询路径通过 atomic.Load 读取，与 reload 互不阻塞。
type state struct {
	cfg     *Config
	records map[string]*runtimeRecord
	health  *HealthChecker
}

type Manager struct {
	configPath string
	state      atomic.Pointer[state]
	reloadMu   sync.Mutex // 串行化 reload
}

func NewManager(cfg *Config, configPath string) (*Manager, error) {
	m := &Manager{configPath: configPath}
	st, err := buildState(cfg)
	if err != nil {
		return nil, err
	}
	st.health.Start()
	m.state.Store(st)
	return m, nil
}

// buildState 依据配置构建一个完整的新 state（不启动 health）
func buildState(cfg *Config) (*state, error) {
	st := &state{
		cfg:     cfg,
		records: make(map[string]*runtimeRecord),
	}
	st.health = NewHealthChecker(cfg.Probe)

	for name, rec := range cfg.Records {
		key := strings.ToLower(dns.Fqdn(name))
		rr := &runtimeRecord{
			rec:       rec,
			pool:      &wrrPool{current: make([]int, len(rec.Targets))},
			targetIDs: make([]string, 0, len(rec.Targets)),
		}
		for _, t := range rec.Targets {
			kind := defaultKind(rec.Type, t.Health)
			port := t.Health.Port
			if port == 0 && kind == "tcp" {
				port = 80
			}
			id := targetID(name, t.Value, kind, port, t.Health.URL)
			rr.targetIDs = append(rr.targetIDs, id)
			st.health.Register(&healthTarget{
				id:    id,
				kind:  kind,
				value: t.Value,
				port:  port,
				url:   t.Health.URL,
			})
		}
		st.records[key] = rr
	}
	return st, nil
}

func (m *Manager) current() *state {
	return m.state.Load()
}

// reload 重新读取配置文件并原子替换 state
func (m *Manager) reload() error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return err
	}
	newSt, err := buildState(cfg)
	if err != nil {
		return err
	}
	newSt.health.Start()

	old := m.state.Load()
	if old != nil && old.cfg.Listen != cfg.Listen {
		log.Printf("警告: listen 地址变化(%s -> %s)需重启进程才能生效", old.cfg.Listen, cfg.Listen)
	}
	m.state.Store(newSt)

	if old != nil {
		old.health.Stop()
	}
	return nil
}

// watchReload 定时检查配置文件 mtime，变化则自动重载
func (m *Manager) watchReload(interval time.Duration) {
	var lastMod time.Time
	if fi, err := os.Stat(m.configPath); err == nil {
		lastMod = fi.ModTime()
	}
	go func() {
		for {
			time.Sleep(interval)
			fi, err := os.Stat(m.configPath)
			if err != nil {
				continue
			}
			if !fi.ModTime().After(lastMod) {
				continue
			}
			lastMod = fi.ModTime()
			if err := m.reload(); err != nil {
				log.Printf("配置热加载失败: %v", err)
			} else {
				log.Println("配置已热加载")
			}
		}
	}()
}

// pick 在存活后端中按权重选一个
func (m *Manager) pick(rr *runtimeRecord, alive []int) string {
	p := rr.pool
	p.mu.Lock()
	defer p.mu.Unlock()

	ts := rr.rec.Targets

	if rr.rec.LB == "weighted-random" {
		total := 0
		for _, i := range alive {
			total += ts[i].Weight
		}
		r := rand.Intn(total)
		for _, i := range alive {
			r -= ts[i].Weight
			if r < 0 {
				return ts[i].Value
			}
		}
		return ts[alive[len(alive)-1]].Value
	}

	// 平滑加权轮询（nginx 同款）
	total := 0
	for _, i := range alive {
		p.current[i] += ts[i].Weight
		total += ts[i].Weight
	}
	best := alive[0]
	for _, i := range alive[1:] {
		if p.current[i] > p.current[best] {
			best = i
		}
	}
	p.current[best] -= total
	return ts[best].Value
}

// resolve 过滤存活后端并选出目标值，返回构造好的 RR
func (m *Manager) resolve(st *state, rr *runtimeRecord, q dns.Question) ([]dns.RR, bool) {
	up := st.health.Snapshot()

	alive := make([]int, 0, len(rr.rec.Targets))
	for i := range rr.rec.Targets {
		if up[rr.targetIDs[i]] {
			alive = append(alive, i)
		}
	}

	var value string
	switch {
	case len(alive) > 0:
		value = m.pick(rr, alive)
	case rr.rec.Fallback != "":
		value = rr.rec.Fallback
	default:
		return nil, false // 全挂且无兜底 → SERVFAIL
	}

	return buildRRs(q.Name, q.Qtype, rr.rec.Type, value, st.cfg.TTL), true
}
