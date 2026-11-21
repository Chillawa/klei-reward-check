# Klei 点数自动领

[English](README.en.md)

每小时查找 Klei 免费点数链接并自动领取，到账后发送桌面通知。勾选「同时领取神秘礼物」还会尝试领取隐藏奖励。

## 使用方法

1. 在仓库右侧点 **Releases**，打开最新版，在 **Assets** 里下载对应系统的 ZIP 并解压。苹果芯片和其他 ARM 电脑选 `arm64`，Intel 或 AMD 电脑选 `x64`。
2. 按下表打开软件，点「登录并开启」，在 Klei 官网登录。
3. 登录后可以关窗口。电脑开机、已登录系统并联网时，软件会在后台检查。

| 系统 | 怎么打开 |
| --- | --- |
| macOS 13 及以上 | 把 `Klei 点数自动领.app` 拖到「应用程序」，双击打开。 |
| Windows 10 / 11 | 双击 `klei-points-auto-claim.exe`。需要 curl 和 Edge、Chrome 或 Chromium。 |
| Linux 桌面 | 在解压目录运行 `./klei-points-auto-claim`。需要 curl、systemd 用户服务和 Edge、Chrome 或 Chromium。 |

v1.0 用户请手动下载并打开一次新版。从 v1.1 起，打开软件时会自动更新；开着自动领取，关窗口也会在后台更新。请把软件放在你的账户有写入权限的位置。

代码采用 [MIT 许可](LICENSE)。[点数图标](desktop/core/ui/ICON-NOTICE.txt)版权归 Klei Entertainment。
