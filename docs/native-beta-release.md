# CodexFloatingBar Next Beta 发布与回滚

## 发布身份

Beta 继续与 WPF 正式版并行，不占用 WPF 的应用身份、启动项或安装目录。发布身份由
`resources/release-metadata.psd1` 单点维护：

| 字段 | 默认值 |
| --- | --- |
| 渠道 | `Beta` |
| 展示版本 | `0.1.0-beta.4` |
| Windows 数字版本 | `0.1.0.3` |
| 应用 ID | `CodexFloatingBar.Next` |
| 可执行文件 | `CodexFloatingBar.Next.exe` |
| 安装目录 | `%LOCALAPPDATA%\Programs\CodexFloatingBar.Next` |
| 启动项 | `CodexFloatingBar.Next` |
| 卸载键 | `CodexFloatingBar.Next` |
| 窗口类 / 标题 | `CodexFloatingBar.Next.Window` / `CodexFloatingBar Next Beta` |

`resources/app.manifest.in` 和 `resources/app.rc.in` 是模板。构建脚本使用同一份元数据
生成 manifest 与 Windows `VERSIONINFO`，再把完全相同的值传给 NSIS。EXE 资源
使用本项目生成的“守望机器人” `app-icon.ico`，主窗口任务栏和托盘均从资源 ID `101`
加载该图标；来源、源图和哈希记录在 `docs/asset-provenance.md`。
统计窗口与额度提醒窗口继续保持工具窗口，不单独占用任务栏入口。
构建前还会核对 `internal/appidentity` 的运行时应用 ID 和产品名，身份漂移时直接失败。

## 构建与校验

日常发布只需在 `resources/release-metadata.psd1` 更新版本后执行：

```powershell
.\scripts\publish-next.ps1
```

该命令依次执行静态门槛、测试与构建、ZIP/清单/SHA-256 独立验证，并只在前述步骤
全部成功后清理旧候选。候选输出到 `release/next-beta-{version}/`；默认保留最近 2 个
`next-beta-*` 目录，同时清理明确命名的发布验证缓存。可用以下命令先预览清理范围：

```powershell
.\scripts\cleanup-releases.ps1 -KeepReleases 2 -WhatIf
```

`cleanup-releases.ps1` 不处理 `bin` 回滚程序、当前 UI bundle、源码、用户数据、验证报告
或非 `next-beta-*` 目录。签名发布可向一键命令传入
`-SigningCertificateThumbprint` 和 `-TimestampUrl`。

先进行不启动应用、不安装程序的静态校验：

```powershell
.\scripts\test-release-static.ps1
```

该脚本完成 PowerShell 语法检查、manifest/`VERSIONINFO` 展开、`windres` 编译和
NSIS 完整编译，所有输出只进入 `.cache/release-static-validation`。

构建正式候选产物：

```powershell
.\scripts\build-release.ps1
```

若发布新版本，显示版本和四段 Windows 数字版本必须在同一次构建中明确提供：

```powershell
.\scripts\build-release.ps1 `
  -Version 0.1.0-beta.4 `
  -VersionQuad 0.1.0.3
```

取得当前用户证书存储中的代码签名证书后，可在同一次构建中先签独立 EXE，再生成
包含已签名 EXE 的 ZIP，最后签 NSIS 安装器并计算最终校验值：

```powershell
.\scripts\build-release.ps1 `
  -SigningCertificateThumbprint <40位证书指纹> `
  -TimestampUrl http://timestamp.digicert.com
```

签名构建会用 Windows SDK x64 `signtool.exe` 执行 SHA-256 签名和 RFC 3161 时间戳，
并在生成 `SHA256SUMS.txt` 前验证签名状态与时间戳；任何一步失败都会中止发布。

默认产物为：

- `CodexFloatingBar.Next\CodexFloatingBar.Next.exe`
- `CodexFloatingBar.Next-0.1.0-beta.4-win-x64.zip`
- `CodexFloatingBar.Next-0.1.0-beta.4-Setup.exe`
- `SHA256SUMS.txt`

便携包和安装目录同时包含 `release.json`，供人工核对渠道、版本、应用 ID 和架构。
`SHA256SUMS.txt` 对独立 EXE、ZIP 和实际生成的安装器逐一计算 SHA-256；没有找到
NSIS 时不会把不存在或旧的安装器写入校验文件。

## 当前 Beta 候选

本机已在独立目录 `release/next-beta-0.1.0-beta.4/` 生成未签名的 `0.1.0-beta.4`
本地候选。该版本加入增量 session 尾读、单次 inventory、工作台 atlas/事务导出、
额度提醒窗口级动效和隐藏态空闲优化，并把正式应用、托盘、安装器与卸载器统一为本项目
生成的“守望机器人”图标。

| 产物 | 大小 | SHA-256 |
| --- | ---: | --- |
| `CodexFloatingBar.Next.exe` | 4,292,096 B | `bde7c5aa20d620bb805255dbedc4de2752ca8eff4fb6f6fd94769135c1ffe1f5` |
| `CodexFloatingBar.Next-0.1.0-beta.4-win-x64.zip` | 2,272,690 B | `6fd95ef0a41dae96026249d647a144aa9dbefb57c12a3b63402aca02d7b3bda6` |
| `CodexFloatingBar.Next-0.1.0-beta.4-Setup.exe` | 1,919,642 B | `ef8ed8fce44b47ebf609b74716b654fe24739c4db72234a3530d526368853e64` |

内置 UI bundle 为 schema 2，包含 14 个 surface、56 个 PNG；页面元数据为
`更新 2026-08-19`、`BUILD 2026-08-19 15:56:31`、指纹
`codexfloatingbar-d5908c973410`、generation `79078914f9c3e9c8`。便携包只包含
manifest 引用的这一代资源。

beta.4 已通过普通单元测试、工作台资产门槛、发布静态门槛、Windows 版本资源、图标
来源双哈希、NSIS 编译和完整发布验证，并独立复算三份 SHA-256。ZIP 内 61 个文件已
逐一与便携目录比对，manifest 引用的 56 张 PNG 与包内 PNG 集合完全一致。正式 EXE
与安装器 PE Subsystem 均为 `2 (Windows GUI)`，图标资源、安装器图标和卸载器图标均
指向 SHA-256 为 `f2c6748b06cf268c229ab355678490cfaa0e6c7eccd8bdda65bac363bb0d4a28`
的 ICO；源 PNG SHA-256 为
`12e4b7645fd909b2344da11d1814876b01af0a6e7e7eeb7400a99179307efc48`。

本候选从含未提交修改的工作区生成，不能只凭当时的 HEAD `186903b` 逐字节复现。
EXE 与安装器 Authenticode 状态均为 `NotSigned`；未执行真实安装、覆盖升级、卸载、窗口
压力循环或长期空闲性能回归。旧 beta.2、beta.3 均保留，本轮只预览了旧候选清理。

此前的 beta.1 候选已在本机 Windows 10 Home China 22H2 完成便携启动，以及 `0.0.9-beta.1` 到
`0.1.0-beta.1` 的同身份覆盖升级。升级会清除注入的旧 UI generation，并保留、更新
已启用的 Next 启动项。应用运行期间，静默安装器和注册的 `QuietUninstallString` 均
返回 `32`，应用没有被终止；退出应用后该注册命令返回 0，Apps & Features、Next
启动项、应用键、卸载键、
开始菜单和安装目录均清理，WPF 启动项保持不变。安装验证报告位于
`runtime-data/installer-upgrade-verification-20260809-213617-25428/verification.json`。

卸载 Next 后，现有 WPF v0.1.3 便携程序也已完成隔离回滚启动：进程保持运行，创建了
标题为 `CodexFloatingBar` 的有效可见窗口且 PID 匹配；测试结束后只终止本次启动的
测试进程。Next 与 WPF 的既有用户数据均在验证前暂存、结束后原样恢复，脚本生成的
测试数据进入各自报告目录。WPF 报告位于
`runtime-data/wpf-rollback-verification-20260809-212953-8680/verification.json`。

无需开发环境的干净机验收包也已生成：

- `release/CodexFloatingBar.Next-0.1.0-beta.1-verification-kit.zip`
- 大小：72,060,767 B
- SHA-256：`7b48deeff200c7415ba477d2582372dd51212c938f42e325534ad9244034782d`

ZIP 只包含最终安装器、旧版升级夹具、自包含 WPF v0.1.3、发布元数据、四个验证脚本
和内部 `SHA256SUMS.txt`，不包含运行报告或用户数据。它已在 Windows 10 Build 19045
的临时干净用户状态下直接运行通过：包内校验、干净预检、覆盖升级、占用返回 32、
正常卸载和 WPF 回滚均通过；汇总位于
`release/CodexFloatingBar.Next-0.1.0-beta.1-verification-kit/runtime-data/`
`clean-verification-20260809-214618-11044/summary.json`。

## 安装、升级和卸载策略

- 安装器按窗口类和精确标题检测主窗口，包括隐藏到托盘的实例。
- 交互安装或卸载发现程序仍运行时，会要求从托盘退出后重试；不会强制杀进程。
- 静默安装或卸载发现程序仍运行时直接失败，并设置退出码 `32`。
- 同一 Next 身份采用原目录覆盖升级；复制前只清除生成的 `ui` 目录，避免旧 generation
  残留。若 Next 启动项原先已启用，升级后会把它更新到新安装路径；默认不会主动启用。
- Apps & Features 使用 HKCU 64 位卸载键，写入展示名、版本、发布者、安装目录、
  卸载命令、静默卸载命令、项目网址、预计大小以及 `NoModify`/`NoRepair`。
- 卸载同时清除 32/64 位视图中的 `CodexFloatingBar.Next` 应用键、卸载键和启动项。
  WPF 的启动值名为 `CodexFloatingBar`，不会被读取、覆盖或删除。
- 卸载删除程序文件时会有限次重试瞬时文件锁；仍被占用时改为 Windows 重启删除，
  不会无限等待或强制结束进程。
- 卸载不删除 `%LOCALAPPDATA%\CodexFloatingBar.Next` 中的用户设置和统计缓存，便于
  重新安装；数据清理应作为单独、明确授权的操作。

旧 `CodexFloatingBar.Native.P0` 原型与 Next 使用不同身份，因此 Next 安装器不会把
P0 当成可升级版本，也不会自动卸载它。已安装 P0 的测试者应先从其托盘退出，并按需
单独卸载 P0。

## 回滚到 WPF

1. 从 Next Beta 托盘菜单退出应用。
2. 在 Windows“已安装的应用”中卸载 `CodexFloatingBar Next Beta`。
3. 启动仍保留的 WPF `CodexFloatingBar.exe`；Beta 安装和卸载均不覆盖 WPF 文件。
4. 如需开机启动，在 WPF 托盘菜单重新启用。Next 卸载器不会删除 WPF 的启动项。
5. 若 Beta 安装器因程序仍运行而返回 `32`，先退出 Next，再重试安装、卸载或回滚。

Beta 至少保留一个发布周期的 WPF 下载、构建产物与说明。正式切换前不得删除 WPF
源码或构建产物。

## 尚未闭环

- 当前没有代码签名证书；安装器和 EXE 仍会受到 SmartScreen 信誉提示影响。
- 当前 EXE 和安装器的 Authenticode 状态均为 `NotSigned`。
- 当前运行时窗口类、标题、互斥体、设置/缓存目录和启动项已统一为
  `CodexFloatingBar.Next`；正式打包前仍须保留静态身份核对门槛。
- 当前宿主已是 Windows 10 x64 22H2，并通过干净用户状态验收，但不是独立系统镜像；
  Windows 10 Home 也未提供 Windows Sandbox，仍需在独立机器/VM 复核，并在签名后
  完成 SmartScreen 信誉验证。
- 用户已明确取消最终窗口压力循环和 5 分钟空闲性能回归，早期 P0 数据不得表述为
  当前发布候选的性能验收结果。
- Beta 验收后切换正式应用 ID、文件名和启动项属于独立变更，不在 Beta 包中提前做。
