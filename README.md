# Fanout 综合版

面向 Ubuntu/Debian 单台 VPS 的多来源 SOCKS5 出口面板。

## 下载与安装

从 [Releases](https://github.com/y1635331-oss/fanout-integrated/releases/latest) 下载完整安装包（包含 Linux amd64/arm64 程序），解压后进入 fanout-integrated 目录运行：

```bash
sudo bash install.sh
```

仓库源码不附带预编译程序。从仓库源码安装需要 Go 1.24+；直接部署建议使用 Releases 完整包。

升级同样执行安装脚本，配置保留在 /var/lib/fanout-integrated。升级前请备份该目录。

## 1.1.0 更新

- 新增在线出口国家与运营商查询、缓存、重新识别按钮；支持关闭查询。
- 区分来源标注、查询国家和住宅检测，不把公共代理误标为住宅。
- 面板内新增操作步骤、来源获取指南、按类型切换的示例、订阅限制与故障排查。
- 移除原版面板和登录页的推广、联系方式及仓库链接；保留 MIT 版权许可。

主要功能：VPN Gate、公共代理列表、OpenVPN/上游代理导入、HTTPS 订阅、批量建出口、自动恢复、S5/CSV/JSON 提取及只读 API。

详见 [中文使用说明](使用说明.md) 与 [验证报告](验证报告.md)。公共出口不保证住宅属性；严格住宅筛选需要单独的检测服务。默认 20 个槽位，上限可配置至 254，实际容量取决于 VPS 和上游。

国家查询使用 [ipwho.is](https://ipwhois.io/documentation)，只发送出口 IP，不发送代理凭据。查询失败不影响普通转发，也不会判定为住宅。

## 许可

保留原项目 MIT 许可与版权署名，见 [LICENSE](LICENSE)。
