# 部署与运维

GoDex Web UI 可嵌入 Go 二进制，适合以单服务运行。部署前先决定只在本机监听，还是通过反向代理和 Relay 对外提供能力。

## 部署路线

| 目标 | 文档 |
|---|---|
| 在 Linux/macOS 安装为服务 | [自部署指南](../self-deploy.md) |
| 把另一台机器接入控制面 | [节点接入](../node-onboarding.md) |
| 理解 Registry、Relay 与远程执行 | [Node Mesh 设计](../node-mesh-design.md) |
| 运行 Tauri 桌面壳 | [桌面壳](../desktop-shell.md) |

## 最小生产检查

1. 使用独立系统用户运行服务。
2. 将 `GODEX_HOME` 放在持久磁盘并纳入备份策略。
3. 只监听可信地址，公网访问放在 TLS 反向代理后。
4. Provider 密钥使用环境变量或安全引用，不提交到仓库。
5. 根据环境选择审批模式和工具安全 profile。
6. 定期检查 session、browser cache、artifact 与 subagent 存储占用。
7. 升级前运行 release checks，并备份配置和状态目录。

## 本地启动

```bash
godex serve --addr 127.0.0.1:8080
```

生产服务安装和 systemd 配置见[自部署指南](../self-deploy.md)。

## 排障入口

- 用户配置、Provider、API 和常见错误：[用户指南](../user-guide.md#故障排查)
- 已知工具链问题与规避方式：[工具问题记录](../tools_issues.md)
- 当前功能状态与事实源：[功能实现矩阵](../feature-implementation-matrix.md)

::: danger 暴露到公网前
不要直接把带高权限工具和宽松审批策略的实例暴露到公网。先收紧监听地址、认证、反向代理、工具白名单、scope 与 sandbox。
:::
