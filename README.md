# Fanout 综合版

以 fanout 为基础的 Ubuntu/Debian 单台 VPS 多出口 SOCKS5 工具。

**先读 [中文使用说明](使用说明.md) 与 [验证报告](验证报告.md)。**

完整包含 Linux amd64/arm64 程序、源码、测试、安装脚本。解压进入目录后运行：

```bash
sudo bash install.sh
```

管理页面默认 HTTPS，首次安装打印地址、随机口令及证书指纹。

主要功能：VPN Gate、多公共来源、OpenVPN/上游代理导入、HTTPS 订阅、批量建出口、自动恢复、S5/CSV/JSON 提取、只读 API 令牌。

公共出口不保证住宅属性；严格住宅筛选需接入结构化检测接口。默认每台 VPS 20 个槽位，可配置 1–254，实际容量由机器与上游决定。

保留原项目 MIT 许可与归属，历史说明见 [README-upstream.md](README-upstream.md)，其中的原版安装命令不适用于综合版。
