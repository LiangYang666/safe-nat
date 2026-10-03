# safe-nat

**安全的 NAT 内网穿透工具（Go）** —— 把内网 TCP 端口暴露到公网，且每个端口可选受 **IP 白名单防火墙** 保护：谁能连、什么时候能连，由你在内置 Web 管理页里说了算。

单静态二进制（前端已 embed），一个命令起服务端、一个命令起客户端。架构参考 [LiangNat](https://github.com/LiangYang666/LiangNat)（自研 Java 版，曾长期使用），协议为全新设计，不兼容旧版。

> 与 frp 的裸端口映射相比，safe-nat 的核心差异：**暴露的端口默认被白名单防火墙保护**——公网连接在 accept 时按来源 IP 校验，命中白名单才放行；不在名单里的访问者连接会被直接拒绝并记录到安全日志。

## 安装

三种方式：

```bash
# 1. 下载发布版（推荐）：https://github.com/LiangYang666/safe-nat/releases
#    选平台二进制：safenat-linux-amd64 / -linux-arm64 / -linux-arm-arm7 /
#    -darwin-amd64 / -darwin-arm64 / -windows-amd64.exe（各带 .sha256 校验）
chmod +x safenat-linux-arm64 && sudo mv safenat-linux-arm64 /usr/local/bin/safenat

# 2. 源码安装（需要 Go 1.24+）
go install github.com/LiangYang666/safe-nat/cmd/safenat@latest

# 3. 交叉编译（客户端跑路由器/NAS），见「部署」节

safenat version    # 确认装好
```

## 特性

- ✅ **TCP 端口映射**：多隧道并发，一条控制连接多路复用所有转发数据
- ✅ **IP 白名单防火墙**：精确 IP + CIDR（`1.2.3.4` / `10.0.0.0/8`，IPv4/IPv6），每隧道独立开关
- ✅ **Web 管理面板**（Vue3 暗色运维风，登录 / 总览 / 隧道 / 流量 / 白名单 / 安全日志 + SSE 实时事件）
- ✅ **SOCKS5 代理**：把客户端所在的局域网变成可浏览的网络（出网代理场景）
- ✅ **流量统计（v0.8）**：每隧道独立实时吞吐（1s 采样）+ 分钟级曲线（SQLite，自动保留 7 天）+ 天级历史持久化，面板「流量」页曲线/柱状图悬停查看逐点数据
- ✅ **IP 归属地标注（v0.8）**：白名单规则自动标注 ip2region 地域（离线库 embed）；面板自动识别当前访问者 IP，一键把自己加进白名单
- ✅ 心跳保活 + 断线指数退避重连（±20% 抖动防重连风暴）
- ✅ **自动传输加密（v0.7）**：server↔client 默认 TLS（自签 + 指纹校验，SSH known_hosts 式），零配置
- ✅ **公网 TLS 终结（v0.9）**：`public_tls.ports` 列出的隧道端口由服务端终结访客 TLS——浏览器用 `https://` 访问公网端口，内层服务保持 HTTP 不用改一行代码；按端口 opt-in，VNC/SOCKS5 这类不认 TLS 的客户端不受影响
- ✅ 单二进制部署（前端 embed + 纯 Go sqlite，免 cgo，可交叉编译上路由器 / NAS）
- ✅ **防爆破**：Web 登录与控制口接入按 IP 递增锁定（5 错 → 1m/5m/15m），失败尝试实时上安全日志
- ✅ 本地运维：`safenat init`（配置生成到用户目录）· `safenat status`（已监听端口/会话/统计）· `safenat logs`

## 架构

```
                         公网                                内网 (NAT 后)
┌──────────────────────────────────────────┐      ┌───────────────────────────────┐
│  safe-nat server（云服务器）              │      │  safe-nat client（LAN 机器）    │
│                                          │ 控制 │                               │
│  · 控制监听 :10010   ◄══════════════════►│ 连接 │  · 拨号 server:10010           │
│  · 每条隧道绑一个远端端口 :40022          │      │  · 按配置拨号 local_ip:port    │
│     ├─ accept 时校验来源 IP（白名单）     │ 帧   │  · SOCKS5 目标由 server 解析后  │
│     ├─ 命中 → Open 帧让 client 拨号       │ 多路 │    通知本端拨号                │
│     └─ 拒绝 → 计数 + 安全日志事件         │ 复用 │                               │
│  · Web 管理 :10086（embed SPA）           │      │                               │
└──────────────────────────────────────────┘      └───────────────────────────────┘
       数据面与信令复用同一条控制 TCP（帧头 connID 区分），client 只出一条出站连接。
```

用户访问：`curl server:40022` ≈ 直接访问内网机器的 `127.0.0.1:22`。是否放行取决于来源 IP 是否在白名单（且隧道开了 `firewall`）。

## 快速开始

两种方式二选一：

```bash
# A. 本地/个人使用：配置写到用户目录（Linux ~/.config/safenat/，
#    macOS ~/Library/Application Support/safenat/），不用 -c
safenat init server    # 生成并告诉你路径，改好再跑
safenat init client
safenat server         # 自动找到用户目录配置；没有则提示 init
safenat client

# B. 云服务器部署（systemd）：-c 显式指定，见 docs/deploy.md
safenat server -c config_server.yaml
```

### 1. 服务端（云服务器）

```bash
safenat server -c config_server.yaml
```

`config_server.yaml`：

```yaml
bind_port: 10010   # 控制端口：客户端连这里
token: "change-me"          # 客户端接入凭证，务必修改

# tls: true                # 默认开：首次启动自动生成自签证书
#                          # data/safenat-server.crt/.key（配置同目录的 data/ 下）。
#                          # 别删——删了客户端指纹校验会全部拒连（见「安全模型」）
web:                       # 存在即启用管理面板 + 白名单防火墙
  bind_port: 10086
  username: admin
  password: "change-me"    # 务必修改
  # db_path: <配置目录>/data/safenat.db   # 白名单库（默认，自动创建）

# public_tls:              # v0.9：给选定的隧道公网端口终结访客 TLS
#   ports: [48080]         # 只有列出的 remote_port 走 https；内层服务保持 HTTP 无需改
#   # cert: /  key:         # 默认 data/public-tls.crt/.key（首次启动自签）；
#   #                        指向你自己的域名证书可消除浏览器告警
```

浏览器打开 `http://<server>:10086` 登录。**先把你自己当前的公网 IP 加进白名单**（否则后面所有受保护端口都会拒绝你）。

### 2. 客户端（内网机器）

```bash
safenat client -c config_client.yaml
```

`config_client.yaml`：

```yaml
name: "home-server"        # 可选：Web 面板里显示的标签
server_addr: 1.2.3.4       # 云服务器
server_port: 10010
token: "change-me"         # 与服务端一致

# tls: true                # 默认开：首次连接自动信任服务器指纹并写入
#                          # data/known_servers.txt（配置同目录的 data/ 下）；
#                          # 之后服务器换了证书会被拒连（防冒充），
#                          # 确认真服务器后删除该文件重连即可

tunnels:
  ssh:
    local_ip: 127.0.0.1    # 要暴露的内网服务
    local_port: 22
    remote_port: 40022     # 云服务器上的公网端口
    # firewall: true       # 默认 true：受白名单保护
  # api:
  #   local_ip: 192.168.1.202
  #   local_port: 8011
  #   remote_port: 8011
  #   firewall: false      # 开放给所有人——仅当应用层自带鉴权时用

# 可选：SOCKS5 代理（服务器上绑 7999，把浏览器指向 server:7999 即可
# “回到”客户端的局域网浏览，如校外访问校内资源）
# socks5:
#   remote_port: 7999
#   firewall: true
```

### 3. 验证

```bash
curl http://<server>:40022   # 白名单内 → 正常响应
# 白名单外的来源 → 连接被拒（reset），并在面板安全日志出现 blocked 事件
curl --socks5-hostname <server>:7999 http://intranet.example/   # SOCKS5 代理
```

## 配置参考

| 段 | 字段 | 说明 | 默认 |
|---|---|---|---|
| server | `bind_port` | 控制端口 | 10010 |
| server | `token` | 客户端接入凭证 | `123456`（有警告） |
| server | `tls` / `tls_cert` / `tls_key` | 自动加密开关；证书路径（自动生成） | true / <配置目录>/data/safenat-server.crt·key |
| server.web | `bind_port` / `username` / `password` / `db_path` | 管理面板；**配置了 web 段才启用防火墙** | 10086 / admin / 123456 / <配置目录>/data/safenat.db |
| server.public_tls | `cert` / `key` / `ports` | v0.9：访客侧 TLS 终结；`ports` 列出的 remote_port 走 https，其余端口不受影响 | <配置目录>/data/public-tls.crt·key / 无（不启用） |
| client | `name` | Web 面板显示名 | 来源 IP |
| client | `server_addr` / `server_port` / `token` | 服务端地址与凭证 | - |
| client | `tls` / `tls_fingerprints` | 加密开关；服务器指纹存储 | true / <配置目录>/data/known_servers.txt |
| client.tunnels.\<name> | `type` | `tcp` | tcp |
| | `local_ip` / `local_port` | 内网服务 | 127.0.0.1 / - |
| | `remote_port` | 服务器公网端口（须未被占用） | - |
| | `firewall` | 是否受白名单防火墙保护 | true |
| client.socks5 | `remote_port` / `firewall` | 服务器上的 SOCKS5 代理端口 | - / true |

## 目录约定

配置与数据分开（XDG/Unix 惯例；systemd 部署见 docs/deploy.md：/etc + /var/lib）：

```
<用户配置目录>/safenat/          Linux ~/.config/safenat/ · macOS ~/Library/Application Support/safenat/
├── server.yaml  client.yaml      ← 纯配置，你编辑的对象（safenat init 生成）
└── data/                         ← 会变的状态，程序自动创建，别手动删
    ├── safenat-server.crt/.key      自动生成的自签证书/私钥（server）
    ├── safenat.db                   白名单库 + 流量统计（日/分钟序列，server）
    └── known_servers.txt            已信任的服务器指纹（client）
```

规则：**配置文件放配置目录，证书/私钥/数据库/指纹这些运行状态默认放旁边的 `data/`**，全部可用 yaml 字段覆盖（`tls_cert`/`tls_key`/`tls_fingerprints`/`db_path`）。备份 = 拷 `data/`（证书/指纹别丢，丢了要重新信任）；`safenat init` 不会覆盖已有配置。

## 本地运维（v0.7）

每个运行中的 server/client 都会在本机开一个 **unix socket 管理端点**（`<TMPDIR>/safenat-<server|client>-<uid>.sock`，权限 0600，仅本进程所属用户可读——本地文件权限即鉴权，不需要密码）：

```bash
safenat status           # 默认查 server：uptime / 已监听端口 / 客户端会话 / 隧道 / 统计 / TLS
safenat status client    # 查 client：连接状态 / 重连次数 / 最近错误
safenat logs             # server 最近日志（内存环形 2000 行）
safenat logs client 50   # client 最近 50 行
```

进程退出自动清理 socket；残留的陈旧 socket 下次启动会覆盖。日志同时照常写 stderr（systemd/journald 不受影响）。

## Web 管理 API

登录后（cookie session）：

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/login` | 登录（失败 5 次 → 按 IP 递增锁定：1m → 5m → 15m，成功即清零） |
| POST | `/api/logout` · GET `/api/session` | 登出 / 会话探测 |
| GET | `/api/tunnels` · `/api/sessions` | 隧道 / 在线客户端状态 |
| GET | `/api/stats` | 汇总：客户端、连接、累计拦截、运行时长 |
| GET/POST | `/api/whitelist` · DELETE `/api/whitelist/{id}` | 白名单 CRUD（添加时自动标注地域） |
| GET | `/api/whitelist/me` | 当前访问者 IP + 地域（面板「一键加自己」） |
| GET | `/api/traffic/live` | 各隧道实时速率与累计（1s 采样） |
| GET | `/api/traffic/daily?days=&tunnel=` | 天级历史（SQLite，可过滤隧道，days 1–90） |
| GET | `/api/traffic/series?days=&bucket=&tunnel=` | 分钟级曲线（days 1–7，bucket `m`/`h`，分钟数据自动保留 7 天） |
| GET | `/api/events` | SSE 实时事件（conn_open / conn_close / blocked / client_up / client_down / auth_fail / login_fail） |
| GET | `/` | SPA（embed） |

## 安全模型（诚实版）

- **凭证**：client 接入用 `token`（控制连接登录时校验，常量时间比较）；Web 用 `username/password`。
- **防爆破**：Web 登录与控制口 client 接入都受**按来源 IP 的递增阶梯锁**保护——同一 IP 连续 5 次凭据错误 → 锁 1 分钟，再犯升级至 5m → 15m 封顶，且锁过（stage>0）的 IP 不会被冷清理重置（等待锁到期不会换来新的 1 分钟起点），只有成功凭据清零；锁定期内 Web 返回 `429 + Retry-After`，控制口连接在读帧前即被断开。所有被拒尝试以 `auth_fail` / `login_fail` 事件实时流入安全日志页。锁是内存态：重启进程即清零。
- **传输加密（v0.7 默认开）**：server↔client 全链路 TLS——server 首次启动自动生成自签 ECDSA 证书（10 年）并持久化；client 首次连接自动信任其 SHA-256 指纹（写入 known_servers.txt），之后每次校验，**换证书即拒连**（防冒充/防重装误连）。诚实边界：无 CA、无域名，防的是被动嗅探（token 与数据不再明文过线）；**首次连接**的主动中间人无法完全排除（与 SSH 首次连接同理，可用 `safenat logs` 核对 server 启动时打印的 fingerprint）。面板（Web 管理）不在此 TLS 内——它是浏览器直连的 HTTP（v1 取舍，见下条）。
- **Web 面板为 HTTP（v1 取舍）**：无 HTTPS（需要域名/证书，用户环境无备案）；无 CSRF token（SameSite=Lax 缓解）。面板默认随服务公网可达，防线 = 强口令 + 阶梯锁 + 失败审计；更强的隔离（反代 HTTPS、仅 VPN 可达）仍是推荐做法。

## 部署

- **systemd 自启模板**：见 [`docs/deploy.md`](docs/deploy.md)（服务端 + 客户端两个 unit，含数据目录与日志配置）。
- **交叉编译**（客户端可跑在路由器 / NAS）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o safenat-linux-arm64 ./cmd/safenat
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o safenat-linux-armv7 ./cmd/safenat
```

发布流程见 [`.github/workflows/release.yml`](.github/workflows/release.yml)（tag `v*` 触发，矩阵交叉编译 + 校验和）。

## 开发

```
cmd/safenat/            CLI 入口（server / client / version）
internal/protocol/      帧协议：9B 头(type|connID|len) + JSON 控制 / 裸数据
internal/config/        yaml 解析与校验
internal/server/        控制面、隧道监听、白名单判定、事件 hub、统计
internal/client/        拨号、登录、目标拨号、退避重连
internal/whitelist/     sqlite 存储 + netip 缓存匹配
internal/webapi/        HTTP API + session + SSE（static/ 由 web/ 构建产物 embed）
web/                    Vue3 + Vite + TS + Tailwind 面板源码（构建产物输出到 internal/webapi/static）
docs/                   协议与部署文档
```

```bash
make build          # 编译 ./safenat
make test           # go test ./...
make web            # 前端依赖 + 构建（产物进 internal/webapi/static）
make fmt-check      # gofmt / go vet
```

依赖克制：Go 侧仅 `gopkg.in/yaml.v3` + `modernc.org/sqlite`（其余全标准库）；CLI 不用 cobra。

## License

[MIT](LICENSE)
