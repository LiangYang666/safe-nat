# safe-nat

安全的 NAT 内网穿透工具（Go）。把内网 TCP 端口暴露到公网，且每个端口可选受 **IP 白名单防火墙**保护——谁能连由你控制，通过内置 Web 管理页维护。

架构参考自 [LiangNat](https://github.com/LiangYang666/LiangNat)（Java 版，曾长期使用的同类工具），协议全新设计。设计文档见工作区 `tasks/20260906-go-liangnat/design.md`（M0 阶段）。

## 特性（规划）

- [ ] TCP 端口映射，多隧道并发
- [ ] 每隧道独立防火墙开关（`firewall_protect`）
- [ ] IP 白名单：精确 IP + CIDR，Web 页管理（sqlite 存储）
- [ ] 内置 Web 管理后台（前端 embed 单二进制）
- [ ] SOCKS5 代理（出网场景）
- [ ] 心跳保活 + 指数退避重连
- [ ] 单静态二进制，交叉编译支持路由器/NAS

## 使用（M1 后可跑通）

```bash
safenat server -c config_server.yaml   # 云端
safenat client -c config_client.yaml   # 内网
```

## 开发状态

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 仓库骨架 / CLI / 设计文档 | ✅ |
| M1 | 控制连接 + 心跳 + token + TCP 映射闭环 | ✅ |
| M2 | Web 管理：登录 + 白名单 + 隧道状态 | ⬜ |
| M3 | SOCKS5 + 重连加固 | ⬜ |
| M4 | Web UI 完善美化（Vue3 面板 + 实时） | ⬜ |
| M5 | 开源发布：README / LICENSE / CI / release | ⬜ |

## 开发

```bash
go build ./cmd/safenat
go vet ./...
```
