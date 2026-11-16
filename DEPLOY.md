# dns-gtm 部署文档

极轻量 DNS GTM（权威响应器）在 Linux 服务器本地的完整部署与运维说明。

## 1. 环境要求

| 项 | 要求 |
|---|---|
| 运行机 | Linux（amd64 / arm64），无需安装 Go |
| 编译机 | Go 1.21+（仅编译时需要） |
| 端口 | 自定义 UDP 端口（默认 5353），需防火墙放行 |
| 权限 | 普通用户即可跑；监听 53 特权端口用 systemd 能力授权，无需 root |

## 2. 编译

### 方式 A：服务器上直接编译（服务器有 Go 时）

```bash
cd /path/to/newgtmdns
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o dns-gtm .
```

### 方式 B：本地交叉编译后上传（推荐，服务器无需装 Go）

在任意有 Go 的机器上：

```bash
# Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dns-gtm .

# Linux arm64（树莓派/ARM 服务器）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dns-gtm .
```

产物是单个静态二进制，无任何运行时依赖。

## 3. 安装

```bash
# 1. 上传二进制与配置
scp dns-gtm        user@server:/tmp/
scp config.yaml    user@server:/tmp/

# 2. 服务器上放置到标准位置
sudo install -m 0755 /tmp/dns-gtm /usr/local/bin/dns-gtm
sudo mkdir -p /etc/dns-gtm
sudo install -m 0644 /tmp/config.yaml /etc/dns-gtm/config.yaml

# 3. 先手动前台跑一次，确认配置正确（Ctrl+C 退出）
/usr/local/bin/dns-gtm -c /etc/dns-gtm/config.yaml
```

## 4. systemd 单元

创建 `/etc/systemd/system/dns-gtm.service`：

```ini
[Unit]
Description=DNS GTM
After=network.target

[Service]
ExecStart=/usr/local/bin/dns-gtm -c /etc/dns-gtm/config.yaml
# 若监听 53 特权端口，取消注释下面这行（免 root 绑定）
# AmbientCapabilities=CAP_NET_BIND_SERVICE
# 用独立低权限用户运行（可选）
# User=dnsgtm
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dns-gtm
sudo systemctl status dns-gtm
```

## 5. 防火墙放行

按实际监听端口放行 UDP（示例 5353）：

```bash
# ufw
sudo ufw allow 5353/udp

# firewalld
sudo firewall-cmd --permanent --add-port=5353/udp
sudo firewall-cmd --reload

# 仅内网使用可不放行公网，只绑内网网卡：
#   config.yaml 里 listen: "192.168.1.10:5353"
```

## 6. 验证

```bash
# A 记录（多次查询观察权重轮询）
dig @127.0.0.1 -p 5353 api.example.com A +short

# CNAME 记录
dig @127.0.0.1 -p 5353 www.example.com CNAME +short

# AAAA 记录
dig @127.0.0.1 -p 5353 api6.example.com AAAA +short

# 从另一台机器验证（走网络）
dig @<服务器IP> -p 5353 api.example.com A +short
```

## 7. 配置与热加载

- 配置格式见 [config.yaml](config.yaml)，运行期改记录/权重/后端/健康参数后**自动生效**（默认 2 秒检测 mtime），无需重启。
- 手动触发：`sudo systemctl reload dns-gtm` 或 `kill -HUP <pid>`。
- **`listen` 端口改动无法热加载**，需重启进程。
- 关闭自动热加载：`ExecStart` 加 `-reload 0`。

## 8. 日常运维

```bash
# 查看日志（含健康状态变化、热加载记录）
journalctl -u dns-gtm -f

# 只查健康状态变化
journalctl -u dns-gtm | grep 健康状态

# 重启 / 重载 / 停止
sudo systemctl restart dns-gtm
sudo systemctl reload dns-gtm     # 等价 kill -HUP，重读配置
sudo systemctl stop dns-gtm
```

## 9. 故障排查

| 现象 | 排查 |
|---|---|
| 启动报 `bind: address already in use` | 端口被占：`ss -ulnp | grep <port>`，改配置端口或停掉占用进程 |
| 启动报 `加载配置失败` | 检查 YAML 缩进/字段名，`config.yaml` 语法错误 |
| 查询返回 `NXDOMAIN` | 域名不在 `records` 里，或查询的 qname 大小写/结尾点问题 |
| 查询返回 `SERVFAIL` | 该域名所有后端被判 down 且未配 `fallback` |
| 改了配置不生效 | 看日志有无「热加载失败」；确认不是改的 `listen` 段 |
| 后端挂了还在返回它 | 健康检查 `fail_threshold` 未到、`interval` 太长；看日志健康状态 |

## 10. 常用命令速查

```bash
# 编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dns-gtm .

# 前台调试（打印日志到终端）
/usr/local/bin/dns-gtm -c /etc/dns-gtm/config.yaml -reload 1s

# 管理
sudo systemctl status|restart|reload|stop dns-gtm

# 探测
dig @127.0.0.1 -p 5353 <域名> A +short
```
