package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen  string            `yaml:"listen"`
	TTL     uint32            `yaml:"ttl"`
	Probe   ProbeConfig       `yaml:"probe"`
	Records map[string]Record `yaml:"records"`
}

// ProbeConfig 全局健康检查参数
type ProbeConfig struct {
	Interval         int     `yaml:"interval"`          // 探测周期（秒）
	Timeout          int     `yaml:"timeout"`           // 单次探测超时（秒）
	FailThreshold    int     `yaml:"fail_threshold"`    // 连续失败 N 次判定 down
	RiseThreshold    int     `yaml:"rise_threshold"`    // down 后连续成功 N 次恢复
	MaxConcurrent    int     `yaml:"max_concurrent"`    // 全局并发探测上限
	DNSMaxQPS        float64 `yaml:"dns_max_qps"`       // 对上游 DNS 解析的全局速率上限
	DNSBackoffMin    int     `yaml:"dns_backoff_min"`   // 失败退避下限（秒）
	DNSBackoffMax    int     `yaml:"dns_backoff_max"`   // 失败退避上限（秒）
	UpstreamResolvers []string `yaml:"upstream_resolvers"` // CNAME 目标解析用上游 DNS 列表 (host:port)，轮询+故障转移
}

type Record struct {
	Type     string   `yaml:"type"`     // A / AAAA / CNAME
	LB       string   `yaml:"lb"`       // weighted-round-robin / weighted-random
	Fallback string   `yaml:"fallback"` // 全部后端 down 时的兜底值（可选）
	Targets  []Target `yaml:"targets"`
}

type Target struct {
	Value  string       `yaml:"value"`  // A/AAAA 为 IP，CNAME 为目标域名
	Weight int          `yaml:"weight"` // 权重
	Health HealthConfig `yaml:"health"` // 可选，缺省按记录类型推断
}

type HealthConfig struct {
	Kind string `yaml:"kind"` // tcp / http / dns
	Port int    `yaml:"port"` // tcp 探测端口
	URL  string `yaml:"url"`  // http 探测地址
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "0.0.0.0:5353"
	}
	if c.TTL == 0 {
		c.TTL = 15
	}
	p := &c.Probe
	if p.Interval == 0 {
		p.Interval = 5
	}
	if p.Timeout == 0 {
		p.Timeout = 2
	}
	if p.FailThreshold == 0 {
		p.FailThreshold = 3
	}
	if p.RiseThreshold == 0 {
		p.RiseThreshold = 2
	}
	if p.MaxConcurrent == 0 {
		p.MaxConcurrent = 10
	}
	if p.DNSMaxQPS == 0 {
		p.DNSMaxQPS = 10
	}
	if p.DNSBackoffMin == 0 {
		p.DNSBackoffMin = 5
	}
	if p.DNSBackoffMax == 0 {
		p.DNSBackoffMax = 120
	}
	if len(p.UpstreamResolvers) == 0 {
		p.UpstreamResolvers = []string{"119.29.29.29:53", "223.5.5.5:53"}
	}
}

func (c *Config) validate() error {
	for name, rec := range c.Records {
		rec.Type = strings.ToUpper(rec.Type)
		switch rec.Type {
		case "A", "AAAA", "CNAME":
		default:
			return fmt.Errorf("记录 %s: 不支持的 type %q（仅 A/AAAA/CNAME）", name, rec.Type)
		}
		if rec.LB == "" {
			rec.LB = "weighted-round-robin"
		}
		if rec.LB != "weighted-round-robin" && rec.LB != "weighted-random" {
			return fmt.Errorf("记录 %s: 不支持的 lb %q", name, rec.LB)
		}
		if len(rec.Targets) == 0 {
			return fmt.Errorf("记录 %s: targets 不能为空", name)
		}
		for i := range rec.Targets {
			if rec.Targets[i].Weight <= 0 {
				rec.Targets[i].Weight = 1
			}
		}
		c.Records[name] = rec
	}
	return nil
}

// defaultKind 按记录类型推断健康检查方式
func defaultKind(recType string, h HealthConfig) string {
	if h.Kind != "" {
		return strings.ToLower(h.Kind)
	}
	if recType == "CNAME" {
		return "dns" // CNAME 目标默认做 DNS 解析探测
	}
	if h.URL != "" {
		return "http"
	}
	return "tcp"
}

// targetID 生成探测目标唯一标识
func targetID(qname, value, kind string, port int, url string) string {
	return strings.ToLower(qname) + "|" + value + "|" + kind + "|" + strconv.Itoa(port) + "|" + url
}
