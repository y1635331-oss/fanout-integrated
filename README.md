# Fanout 综合版

面向 Ubuntu/Debian 单台 VPS 的多来源 SOCKS5 出口面板。

## 一键安装 / 从旧版更新

在 Ubuntu/Debian VPS 执行（需已安装 curl 和 CA 证书）：

```bash
f=$(mktemp) && curl -fsSL --retry 3 https://raw.githubusercontent.com/y1635331-oss/fanout-integrated/main/bootstrap.sh -o "$f" && sudo bash "$f" && rm -f "$f"
```

已安装的 1.0/1.1 版本也可以执行上面命令更新；它保留现有配置。升级到 1.2.0 后使用：

```bash
sudo fanoutctl update
```

下载完整包、校验 SHA256 成功后才停止旧服务；更新会短暂中断 S5。安装过程会备份配置，失败时尝试恢复旧程序和服务。

## 域名与已有证书

先让域名 A 记录指向 VPS，然后执行交互引导：

```bash
sudo fanoutctl domain
```

或指定参数（请替换为自己的域名和实际文件路径）：

```bash
sudo fanoutctl domain panel.example.com /etc/letsencrypt/live/panel.example.com/fullchain.pem /etc/letsencrypt/live/panel.example.com/privkey.pem
```

默认保留面板端口，例如 https://panel.example.com:8899/原访问路径/。可在命令末尾加空闲端口 443，此时域名可省略端口。如果 443 已被 Nginx/Caddy 使用，保持内部面板端口，执行 `sudo fanoutctl proxy` 查看反向代理配置示例，不会修改现有网站。

证书与域名、私钥和有效期验证通过后才写入配置；失败恢复旧设置。程序自动读取原路径上续期的证书，无需重启代理。证书路径变化时重新执行引导。`sudo fanoutctl cert-check` 检查证书，`sudo fanoutctl info` 查看地址。私钥留在 VPS，不上传或写入网页。

## 下载与安装

从 [Releases](https://github.com/y1635331-oss/fanout-integrated/releases/latest) 下载完整安装包（包含 Linux amd64/arm64 程序），解压后进入 fanout-integrated 目录运行：

```bash
sudo bash install.sh
```

仓库源码不附带预编译程序。从仓库源码安装需要 Go 1.24+；直接部署建议使用 Releases 完整包。

升级同样执行安装脚本，配置保留在 /var/lib/fanout-integrated。升级前请备份该目录。

## 1.2.0 更新

- 增加一键安装、校验更新、配置备份和启动失败恢复。
- 增加域名证书引导、自动加载续期证书及反代示例。
- 保留已有面板端口、最大出口数量、来源和凭据。

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


## v1.3.0 多来源订阅与更新修复

修复 1.2.0 启动检测遗漏登录路径前导斜杠导致误判更新失败的问题，并修正管理地址显示。

升级命令与前文相同。首次使用多协议来源，在 VPS 执行：

```bash
sudo fanoutctl core-install
```

从 SagerNet 官方下载固定版本 sing-box 1.14.1，并校验固定 SHA256。该内核独立运行，按官方 GPL 许可证分发；本安装包不捆绑其二进制。已有 OpenVPN、HTTP、SOCKS5 来源不依赖内核。

面板新增 freesub、Au1rxx、proxy-pool、snakem982、awesome-vpn、freeSub、NoMoreWalls 七个公开订阅入口。各来源最多载入 2000 个候选，跨来源按实际连接参数去重；名称或来源自称“住宅”不作为判断依据。

自动识别 sing-box JSON(outbounds)、Clash YAML(proxies)、Base64 和逐行链接。常见 VLESS、VMess、Trojan、Shadowsocks、Hysteria2、TUIC、AnyTLS 参数受支持；不支持 SSR、WireGuard、Mieru、SS 插件、任意 CSV，以及部分扩展传输。高级特性不保证兼容；启动前再用 sing-box check 检查。要求关闭 TLS 验证的节点会跳过。面板展示导入与跳过数量；这些数量不是在线节点数。

使用顺序：安装内核 → 点击来源 → 先选“不限地区”建立 1—3 个出口 → 查看真实出口 IP、国家/ISP、HTTPS 请求耗时 → 导出可用 S5。候选国家未知不表示没有分类功能；连接后按出口 IP 查询。此版本多协议转换输出支持 TCP；不宣称 UDP 可用。每个运行出口一个核心进程，低内存 VPS 请从少量出口开始。

仅提取连接参数，不采用订阅中的入站、路由、脚本、插件、控制接口或本地证书路径。内核监听本机并使用随机认证，只有经过检查的出口可提取；内核退出不会回退 VPS 直连。来源刷新失败保留上一份有效数据；连接失败有重试冷却，已验证成功的节点和较低 HTTPS 请求耗时优先。请求耗时不等于下载带宽。

来源说明：SSAggregator 已跳转到 V2RayAggregator，不重复接入；submerge 是 mihomo 订阅管理器架构参考，并非额外 IP 来源；3x-ui 是管理面板，不是公开节点源。没有复制上述项目的实现代码。
