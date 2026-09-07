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

v1 数据面明文（见 README「安全模型」），至少在云防火墙/ufw 收紧入口：

```bash
sudo ufw allow from <你的固定IP> to any port 10101 proto tcp   # 控制端口
sudo ufw allow from <你的固定IP> to any port 10102 proto tcp   # Web 面板
sudo ufw allow 40022 proto tcp                                  # 要公开的隧道端口（仍受白名单二次把关）
```

或更彻底：把控制连接与 Web 面板套在 WireGuard / TLS 之后，只暴露各隧道端口。
