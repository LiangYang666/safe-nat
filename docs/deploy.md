# 部署：systemd 自启

两种方式二选一：

| 方式 | 权限 | 适用 | 配置/数据放哪 |
|---|---|---|---|
| **A. 用户级**（推荐，家庭服务器/NAS） | 免 sudo | 服务器与客户端同机（j 即此形态），或普通用户跑 | `~/.config/safenat/` |
| **B. 系统级**（root） | 需 sudo | 云服务器（root 运维、多用户隔离） | `/etc/safenat/` + `/var/lib/safenat/` |

---

## 方式 A：用户级 systemd（免 sudo，家庭服务器推荐）

适合"服务端在自家常开机器上、同机再跑客户端拨号局域网其它设备"的形态（LiangNat 同款：server + client 同机双 unit）。**配置目录用用户默认发现**（Linux `~/.config/safenat/`），无需 `-c`。

### 1. 生成配置

```bash
safenat init server     # 生成 ~/.config/safenat/server.yaml 并打印路径
safenat init client     # 生成 ~/.config/safenat/client.yaml
# 编辑两个 yaml：token 一致、client.server_addr 指向本机（127.0.0.1）或服务器 IP
```

### 2. 两个 unit

`~/.config/systemd/user/safenat-server.service`：

```ini
[Unit]
Description=safe-nat server (NAT penetration public side)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%h/safenat/safenat server
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

`~/.config/systemd/user/safenat-client.service`：

```ini
[Unit]
Description=safe-nat client (expose LAN services via local server)
After=network-online.target safenat-server.service
Wants=network-online.target
PartOf=safenat-server.service   # server 停/启时跟随停/启

[Service]
Type=simple
ExecStart=%h/safenat/safenat client
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

（`%h` 展开为家目录；二进制放在 `~/safenat/safenat`。如需 `-c` 指定配置：`ExecStart=%h/safenat/safenat server -c %h/.config/safenat/server.yaml`）

### 3. 开机自启（关键：linger）

用户级 unit 在**用户登出后会被回收**——必须开 linger，服务才会随开机自启且 ssh 断开不掉：

```bash
sudo loginctl enable-linger <用户名>      # 只需这一次 sudo
loginctl show-user <用户名> -p Linger     # 验证：Linger=yes
```

### 4. 管理命令

```bash
systemctl --user daemon-reload
systemctl --user enable --now safenat-server safenat-client   # 开机自启 + 立即启动
systemctl --user restart safenat-client      # 改 client.yaml 后重启（v0.8 起优雅退出 <1s）
systemctl --user status safenat-server --no-pager
journalctl --user -u safenat-server -n 100   # 看日志
safenat status client                         # 本机运维端点（连接状态/最近错误）
```

> ⚠️ 早期版本 client 对 SIGTERM 不响应、stop 要等 90s 强杀——**v0.8 已修复**（主动断开控制连接）。若跑旧版，重启 client 用 `systemctl --user kill -s KILL safenat-client` 兜底。

### 5. 本机验证

```bash
ss -tlnp | grep -E '10010|10086'        # server 监听控制口 + 面板
journalctl --user -u safenat-client -n 5  # 应见 "tunnel ready ... remote_port=..."
```

---

## 方式 B：系统级（root，云服务器）

> 假设：云服务器（服务端）+ 局域网 Linux/树莓派（客户端）；二进制 `/opt/safenat/`，数据目录 `/var/lib/safenat/`。

### 服务端

创建 `/etc/systemd/system/safenat-server.service`：

```ini
[Unit]
Description=safe-nat server (NAT penetration public side)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/safenat/safenat server -c /etc/safenat/config_server.yaml
Restart=on-failure
RestartSec=3
# 硬化：只读大部分路径，仅数据目录可写
ProtectSystem=strict
ReadWritePaths=/var/lib/safenat
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo mkdir -p /opt/safenat /etc/safenat /var/lib/safenat
sudo cp safenat /opt/safenat/
sudo cp config_server.yaml /etc/safenat/
# config_server.yaml 关键项（v0.7+ TLS 默认开，证书自动生成，须放在可写目录）：
#   web.db_path: /var/lib/safenat/safenat.db
#   tls_cert: /var/lib/safenat/safenat-server.crt
#   tls_key:  /var/lib/safenat/safenat-server.key
#   ⚠️ 证书生成一次后别删：删了 = 换锁，所有客户端拒连，
#      需逐个删除其 known_servers.txt 重新信任。
sudo systemctl daemon-reload
sudo systemctl enable --now safenat-server
journalctl -u safenat-server -f          # 看日志
```

### 客户端

创建 `/etc/systemd/system/safenat-client.service`：

```ini
[Unit]
Description=safe-nat client (LAN side)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/safenat/safenat client -c /etc/safenat/config_client.yaml
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
# 客户端首次连接要写服务器指纹文件（known_servers.txt）：
ReadWritePaths=/var/lib/safenat

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp config_client.yaml /etc/safenat/
# config_client.yaml：tls_fingerprints: /var/lib/safenat/known_servers.txt
sudo systemctl daemon-reload
sudo systemctl enable --now safenat-client
```

---

## 公网暴露面建议

控制端口（默认 10010）与 Web 面板（默认 10086）默认就是公网可达的——它们内置的防线是**按 IP 递增锁定 + 常量时间校验 + 失败审计**（见 README「安全模型」），所以这两处**务必**：

1. `token` 与 `web.password` 用足够强的随机串（`openssl rand -hex 16` 级别），不要用默认值；
2. 隧道端口（例：40022）交给白名单防火墙把关，保持 `firewall: true`。

云防火墙/ufw 可按需收紧（无固定 IP 时至少限制管理入口的速率，但内置阶梯锁已覆盖口令爆破）：

```bash
sudo ufw allow 10010 proto tcp   # 控制端口（client 接入）
sudo ufw allow 10086 proto tcp   # Web 面板
sudo ufw allow 40022 proto tcp   # 要公开的隧道端口（仍受白名单二次把关）
```

家庭场景（光猫 + 家用路由器双层 NAT）：**路由器端口映射只指向跑 server 的那台内网机**，Mac/其它设备一律通过 client 隧道暴露（继承白名单防火墙），不要裸映射——具体见 nat-dev-workspace 的 `areas/port-mapping` 手册。

v1 数据面明文：若传输内容敏感，把控制连接与 Web 面板套在 WireGuard / TLS 之后，只暴露各隧道端口——这是比任何口令策略都强的兜底。
