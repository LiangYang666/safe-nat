# safe-nat 线协议 v1

单条 TCP 控制连接承载所有信令与转发数据（client 只需一条出站连接，天然穿透 NAT）。

## 帧格式

```
frame  := header + payload
header := type(1B) | connID(4B BE) | length(4B BE)      // 9 字节定长
```

- `connID == 0`：控制消息，payload 为 JSON。
- `connID > 0`：某条穿透连接的数据或生命周期消息；数据帧 payload 为裸字节。
- 单帧最大 8 MiB（防恶意长度字段）。

### 帧类型

| type | 值 | 方向 | 语义 |
|---|---|---|---|
| Login | 0x01 | c→s | 登录：token + 隧道清单 |
| LoginResp | 0x02 | s→c | 登录结果，逐隧道回报绑定成败 |
| Open | 0x03 | s→c | 公网连接到达：让 client 拨号 |
| Close | 0x04 | 双向 | 拆除某条 connID 的穿透连接 |
| Data | 0x05 | 双向 | connID 对应连接的裸数据 |
| Heartbeat | 0x06 | 双向 | 保活，空 payload |

## 控制消息

```jsonc
// Login（c→s，连上后立即发）
{ "token": "...", "name": "home-server",          // name 可选：面板标签
  "tunnels": [
    { "name": "ssh", "type": "tcp",               // tcp | socks5
      "remote_port": 40022, "firewall": true }
  ]}

// LoginResp（s→c）
{ "ok": true,
  "results": [ { "name": "ssh", "remote_port": 40022, "ok": true } ] }
// 失败时 ok=false, message 说明；results 逐隧道给 error。
// 整批全部绑定失败 → 会话终止。token 错误 → 直接断开。

// Open（s→c，帧头 connID 为新连接编号）
//   TCP 隧道：
{ "remote_port": 40022 }
//   SOCKS5 隧道（remote_port=0，动态目标）：
{ "target_host": "intranet.example", "target_port": 8080 }

// Close（双向，拆 connID；带原因便于日志）
{ "reason": "wan closed" }
```

## 消息流

1. **Login**：client 拨通后立即发送；server 校验 token（常量时间比较）→ 为每个 remote_port 绑定监听 → LoginResp 逐条回报。client 显示每条隧道 ready/失败。
2. **Open**：公网连上 remote_port 且通过白名单 → server 分配 connID、发 Open；client 拨号本地服务（TCP 隧道按配置 local_ip:local_port；SOCKS5 按 target 拨号）。
3. **Data**：双向 `io.Copy` 泵，读到即写帧。
4. **Close**：任一侧数据连接 EOF/出错/dial 失败 → 发 Close → 对端关闭 socket、清 map。
5. **Heartbeat**：双向每 10s（空帧，connID=0）。**读超时判活**：读超时随每个到达帧刷新，数据流量自动顶替心跳。
   - client：35s 无任何帧 → 认为 server 死亡 → 断开重连（退避 1s×2 封顶 30s；连接存活 ≥30s 视为稳定，下次失败从 1s 重新起步；每次重试 ±20% 抖动）。
   - server：60s 无任何帧 → 清理会话、释放端口、推送 client_down 事件。
6. 端口占用等绑定失败**不致命**：LoginResp 逐条上报，其余隧道照常服务；全部失败才终止会话。

## SOCKS5 隧道（type=socks5）

- client.yaml 顶层 `socks5:` 段在 Login 时注册为一条 `type=socks5` 的隧道。
- 公网连入该端口：server 先完成 RFC1928 握手（**仅 no-auth + CONNECT**；方法选择回复严格 2 字节 `[5, method]`，CONNECT 回复为 10 字节标准结构），随后发携带 `target_host/target_port` 的 Open；client 拨号目标后与 TCP 隧道共用 Data/Close 生命周期。
- 握手失败或拨号失败表现为连接被重置（server 无法在拨号前得知 LAN 侧结果，v1 乐观回成功；见 design.md §3.7）。

## 防火墙判定

- 生效条件：server.yaml 配了 `web:` 段（白名单库存在），且该隧道 `firewall: true`。
- 判定时机：公网 **accept 时**按来源 IP 查白名单（精确 IP 或 CIDR，IPv4/IPv6）。拒绝 → 立即 close + 计数 + `blocked` 事件；放行 → Open。
- 已建立的连接不受白名单变更影响；隧道 `firewall` 标志来自 client.yaml，改后需重连客户端生效。
