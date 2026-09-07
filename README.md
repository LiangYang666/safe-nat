# safe-nat

**安全的 NAT 内网穿透工具（Go）** —— 把内网 TCP 端口暴露到公网，且每个端口可选受 **IP 白名单防火墙** 保护：谁能连、什么时候能连，由你在内置 Web 管理页里说了算。

单静态二进制（前端已 embed），一个命令起服务端、一个命令起客户端。架构参考 [LiangNat](https://github.com/LiangYang666/LiangNat)（自研 Java 版，曾长期使用），协议为全新设计，不兼容旧版。

> 与 frp 的裸端口映射相比，safe-nat 的核心差异：**暴露的端口默认被白名单防火墙保护**——公网连接在 accept 时按来源 IP 校验，命中白名单才放行；不在名单里的访问者连接会被直接拒绝并记录到安全日志。

## 特性

- ✅ **TCP 端口映射**：多隧道并发，一条控制连接多路复用所有转发数据
- ✅ **IP 白名单防火墙**：精确 IP + CIDR（`1.2.3.4` / `10.0.0.0/8`，IPv4/IPv6），每隧道独立开关
- ✅ **Web 管理面板**（Vue3 暗色运维风，登录 / 总览 / 隧道 / 白名单 / 安全日志 + SSE 实时事件）
- ✅ **SOCKS5 代理**：把客户端所在的局域网变成可浏览的网络（出网代理场景）
- ✅ 心跳保活 + 断线指数退避重连（±20% 抖动防重连风暴）
- ✅ 单二进制部署（前端 embed + 纯 Go sqlite，免 cgo，可交叉编译上路由器 / NAS）
- ✅ 登录失败节流、token 常量时间比较、session cookie（HttpOnly + SameSite=Lax）

## 架构

```
                         公网                                内网 (NAT 后)
┌──────────────────────────────────────────┐      ┌───────────────────────────────┐
│  safe-nat server（云服务器）              │      │  safe-nat client（LAN 机器）    │
│                                          │ 控制 │                               │
│  · 控制监听 :10101   ◄══════════════════►│ 连接 │  · 拨号 server:10101           │
│  · 每条隧道绑一个远端端口 :40022          │      │  · 按配置拨号 local_ip:port    │
│     ├─ accept 时校验来源 IP（白名单）     │ 帧   │  · SOCKS5 目标由 server 解析后  │
│     ├─ 命中 → Open 帧让 client 拨号       │ 多路 │    通知本端拨号                │
│     └─ 拒绝 → 计数 + 安全日志事件         │ 复用 │                               │
│  · Web 管理 :10102（embed SPA）           │      │                               │
└──────────────────────────────────────────┘      └───────────────────────────────┘
       数据面与信令复用同一条控制 TCP（帧头 connID 区分），client 只出一条出站连接。
```

用户访问：`curl server:40022` ≈ 直接访问内网机器的 `127.0.0.1:22`。是否放行取决于来源 IP 是否在白名单（且隧道开了 `firewall`）。

## 快速开始

### 1. 服务端（云服务器）

```bash
safenat server -c config_server.yaml
```

`config_server.yaml`：

```yaml
bind_port: 10101   # 控制端口：客户端连这里
token: "change-me"          # 客户端接入凭证，务必修改

web:                       # 存在即启用管理面板 + 白名单防火墙
  bind_port: 10102
  username: admin
  password: "change-me"    # 务必修改
  db_path: data/safenat.db # sqlite 白名单库，自动创建
```

浏览器打开 `http://<server>:10102` 登录。**先把你自己当前的公网 IP 加进白名单**（否则后面所有受保护端口都会拒绝你）。

### 2. 客户端（内网机器）

```bash
safenat client -c config_client.yaml
```

`config_client.yaml`：

```yaml
name: "home-server"        # 可选：Web 面板里显示的标签
server_addr: 1.2.3.4       # 云服务器
server_port: 10101
token: "change-me"         # 与服务端一致

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
| server | `bind_port` | 控制端口 | 10101 |
| server | `token` | 客户端接入凭证 | `123456`（有警告） |
| server.web | `bind_port` / `username` / `password` / `db_path` | 管理面板；**配置了 web 段才启用防火墙** | admin / 123456 / data/safenat.db |
| client | `name` | Web 面板显示名 | 来源 IP |
| client | `server_addr` / `server_port` / `token` | 服务端地址与凭证 | - |
| client.tunnels.\<name> | `type` | `tcp` | tcp |
| | `local_ip` / `local_port` | 内网服务 | 127.0.0.1 / - |
| | `remote_port` | 服务器公网端口（须未被占用） | - |
| | `firewall` | 是否受白名单防火墙保护 | true |
| client.socks5 | `remote_port` / `firewall` | 服务器上的 SOCKS5 代理端口 | - / true |

## Web 管理 API

登录后（cookie session）：

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/login` | 登录（失败 5 次/IP/分钟 锁定） |
| POST | `/api/logout` · GET `/api/session` | 登出 / 会话探测 |
| GET | `/api/tunnels` · `/api/sessions` | 隧道 / 在线客户端状态 |
| GET | `/api/stats` | 汇总：客户端、连接、累计拦截、运行时长 |
| GET/POST | `/api/whitelist` · DELETE `/api/whitelist/{id}` | 白名单 CRUD |
| GET | `/api/events` | SSE 实时事件（conn_open / conn_close / blocked / client_up / client_down） |
| GET | `/` | SPA（embed） |

## 安全模型（诚实版）

- **凭证**：client 接入用 `token`（控制连接登录时校验，常量时间比较）；Web 用 `username/password`。
- **访问控制**：白名单防火墙只决定「谁能连穿透端口」；判定发生在 accept 时，已建立的连接不受中途改名单影响（v1 语义，文档见 protocol.md）。
- **不做传输加密（v1）**：token 防未授权客户端接入，数据面明文。公网使用建议外层套 **TLS / WireGuard**（与 frp 同款取舍）；控制端口（10101）与 Web 端口（10102）建议在防火墙层仅放行你的固定 IP。v2 预留 `tls` 选项（Go 标准库自带，成本低）。
- Web session 为内存态、无 CSRF token；请勿把面板暴露到不受信网络（见上一条）。

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
