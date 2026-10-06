# ProxyForge 文档

首次使用从[项目概览与快速上手](overview.md)开始，后续按需要查阅对应指南。

| 文档 | 适用场景 |
| --- | --- |
| [项目概览与快速上手](overview.md) | 了解完整功能，完成首次安装、配置和客户端导出 |
| [安装、升级与卸载](installation.md) | 选择版本和渠道，配置安装代理，确认脚本信任，卸载或清理残留 |
| [配置与日常使用](configuration.md) | 管理服务端和客户端配置，调整 DNS、SNI 与出站，设置中转和落地 |
| [文件与安全边界](security.md) | 查阅受管文件路径、访问权限、备份和回滚机制 |
| [REALITY SNI 检测指南](reality-sni-check.md) | 使用独立黑盒检测脚本，判读结果并排查回落防护问题 |
| [构建、测试与发布](development.md) | 本地构建、运行集成测试，了解 CI 和 Release 流程 |

## 命令帮助

查看命令列表和子命令参数：

```bash
proxyforge --help
proxyforge config --help
proxyforge service --help
```

除帮助和版本查询外，操作需使用 root 权限。安装、更新和卸载的自动化参数以[安装指南](installation.md)为准。

[返回项目首页](../README.md)
