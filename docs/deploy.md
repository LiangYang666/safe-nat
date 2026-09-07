# 部署：systemd 自启

> 假设：Linux 云服务器（服务端）+ 局域网 Linux/树莓派等（客户端）；二进制放在 `/opt/safenat/`，数据目录 `/var/lib/safenat/`。

## 服务端

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

配置与数据目录（白名单库默认 `data/safenat.db`，把 `db_path` 指到 /var/lib/safenat）：

```bash
sudo mkdir -p /opt/safenat /etc/safenat /var/lib/safenat
sudo cp safenat /opt/safenat/
# config_server.yaml: web.db_path: /var/lib/safenat/safenat.db
sudo cp config_server.yaml /etc/safenat/
sudo systemctl daemon-reload
sudo systemctl enable --now safenat-server
journalctl -u safenat-server -f          # 看日志
```

## 客户端

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

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now safenat-client
```

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

v1 数据面明文：若传输内容敏感，把控制连接与 Web 面板套在 WireGuard / TLS 之后，只暴露各隧道端口——这是比任何口令策略都强的兜底。
