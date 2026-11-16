# dns-gtm

极轻量的 DNS GTM（基于 DNS 的流量管理）权威响应器。单二进制部署在 Linux，自定义 UDP 端口，支持 A/AAAA/CNAME 记录的加权负载均衡与健康监测。

## 特性

- 自定义监听 UDP 端口（默认 `0.0.0.0:5353`）
- 加权负载均衡：平滑加权轮询（nginx 同款）/ 加权随机
- 记录类型：`A` / `AAAA` / `CNAME`，CNAME 同样支持权重负载均衡与健康监测
- 健康检查：`tcp` / `http` / `dns`（对 CNAME 目标做解析）
- **健康检查与查询请求完全解耦**：后台独立 goroutine 定时探测，查询只读内存快照，零探测开销
- **上游 DNS 频控**：解析型探测带全局 QPS 限流 + 失败指数退避，防止被上游 DNS ban
- 全后端 down 时可配兜底值（fallback），否则返回 `SERVFAIL`
- **配置热加载**：改动配置文件自动生效（mtime 检测），支持 SIGHUP 手动重载

## 一键安装（推荐）

从 GitHub Releases 下载预编译二进制，自动检测 amd64/arm64 架构，安装并配置 systemd 开机自启，**无需 Go 环境**：

```bash
curl -fsSL https://raw.githubusercontent.com/areyouokbro/dns-gtm/main/install.sh | sudo bash
```

完成后用管理脚本进行日常管理：

```bash
sudo dns-gtm.sh          # 交互式菜单（安装/卸载/启停/重启/重载/编辑配置）
sudo dns-gtm.sh edit     # 编辑配置（改完自动热加载）
```

- 配置在 `/etc/dns-gtm/config.yaml`，改完自动生效。
- 目录受限的容器环境可指定安装目录：`BIN_DIR=/opt/gtm CONF_DIR=/opt/gtm/etc bash install.sh`

## 手动构建（源码编译）

```bash
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o dns-gtm .
```

产物是单个静态二进制 `dns-gtm`。交叉编译其它架构：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dns-gtm-arm64 .
```

## 运行

```bash
./dns-gtm -c config.yaml             # 默认 2s 检测一次配置变化并自动重载
./dns-gtm -c config.yaml -reload 0   # 关闭自动热加载
```

命令行参数：`-c` 配置文件路径；`-reload` 热加载检测间隔（默认 2s，0 关闭）。

systemd 单元：

```ini
[Unit]
Description=DNS GTM
After=network.target

[Service]
ExecStart=/usr/local/bin/dns-gtm -c /etc/dns-gtm/config.yaml
# 若要监听 53 特权端口，取消注释下面这行（免 root）
# AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

## 配置热加载

默认每 2 秒检查配置文件 mtime，变化即自动重载（原子替换整个状态，不阻塞查询）。也可手动触发：

```bash
kill -HUP <pid>
# 或
systemctl reload dns-gtm
```

- 可热加载：`records`（解析记录）、`probe`（健康检查参数）、`ttl`。
- 不可热加载：`listen`（端口已绑定，改动需重启，日志会告警）。
- 热加载会重建健康检查状态（新后端初始视为存活），WRR 计数复位。

## 测试

```bash
# A 记录，多次查询观察权重轮询
dig @127.0.0.1 -p 5353 api.example.com A +short
dig @127.0.0.1 -p 5353 api.example.com A +short
dig @127.0.0.1 -p 5353 api.example.com A +short

# CNAME 记录（按权重返回不同别名）
dig @127.0.0.1 -p 5353 www.example.com CNAME +short
```

## 配置说明

见 [config.yaml](config.yaml)，要点：

- `listen`：自定义 UDP 监听地址/端口。
- `ttl`：建议 10~30s，短 TTL 让故障切换与权重调整更快生效。
- `records.<域名>`：每条记录含 `type`、`lb`、`fallback`、`targets`。
- 每个 target 的 `weight` 决定被选中的概率/比例；`health` 可省略，缺省按记录类型推断（A/AAAA→tcp，CNAME→dns）。

## 健康检查与频控设计（关键）

1. **查询路径零探测**：`Handle` 收到查询后只做「查配置 → 读健康快照 → 权重选值 → 回包」。健康快照是 `atomic.Pointer` 保存的内存 map，原子读、O(1)。
2. **后台探测**：独立 goroutine 每 `probe.interval` 秒扫一轮，对到期目标用带 `max_concurrent` 并发上限的 worker 池执行探测。
3. **失败退避**：后端被判 down 后，探测间隔按 2 倍递增，封顶 `dns_backoff_max`；恢复后退避复位。故障后端不会持续被打。
4. **上游 DNS 限速**：`dns` 型探测（CNAME 目标解析）额外经过全局令牌桶（`dns_max_qps`），超限本轮跳过，绝不对上游造成突发压力。

因此探测开销 = `目标数 / 探测周期`，**与查询 QPS 完全无关**，不会因查询量放大对上游 DNS 的访问。

## 边界

- 这是权威响应器：只回答配置内的域名，其他查询返回 `NXDOMAIN`；不递归转发。
- 只监听 UDP。若客户端强制 TCP 或需要 DNSSEC 大包，需另加 TCP 监听（本实现未包含，保持最轻）。
