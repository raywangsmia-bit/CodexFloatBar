# 资源来源与继承说明

## 项目沿革

CodexFloatBar 是原 CodexFloatingBar 项目的 Go/Win32 原生重写。新项目重新建立 Git
仓库，但保留对原项目来源、作者贡献和许可证的说明。原项目地址：

<https://github.com/liuguoqiang0730-svg/CodexFloatingBar>

原 WPF/C# 源文件、XAML、项目文件、旧截图和旧构建产物不包含在当前源码中。当前 Go
实现根据冻结的行为契约重新实现数据读取和桌面交互，并保留只读的旧设置迁移能力。

## 应用与托盘图标

2026-08-19 起，正式资源 [`resources/app-icon.ico`](../resources/app-icon.ico) 使用本项目
新生成并由维护者选定的“守望机器人”IP。宽面罩呼应浮条的横向状态面板，薄荷绿呼应
正常额度提示，深海军蓝延续现有深色界面。图像由 OpenAI 图像生成工具生成，源图原样
保存在 [`resources/app-icon-source.png`](../resources/app-icon-source.png)，ICO 只执行标准
多尺寸缩放和容器封装，没有改变构图。

- 源 PNG：1254 × 1254、完全不透明
- ICO 尺寸：16、20、24、32、40、48、64、128、256 像素
- 源 PNG SHA-256：`12e4b7645fd909b2344da11d1814876b01af0a6e7e7eeb7400a99179307efc48`
- ICO SHA-256：`f2c6748b06cf268c229ab355678490cfaa0e6c7eccd8bdda65bac363bb0d4a28`

### 旧版继承图标

此前的 `resources/app-icon.ico` 来自原项目：

- 原路径：`src/CodexFloatingBar/Assets/app-icon.ico`
- 加入提交：`2d1c91486682d11602310b83196a23fa1fa72615`
- 提交作者：`liyuejiong <yuejiong.li@ehang.com>`
- SHA-256：`400194ff1fe659694b53fa2e5c9821082145c8a3d63bc25b2bdaa3ebc6029cc7`
- 许可：随原 CodexFloatingBar 项目按 MIT License 分发

Git 历史没有记录该图标更早的设计来源或独立许可证，因此不得将其描述为新项目原创
资源。该文件已被新版图标替换，但上述记录继续保留用于历史追溯。

### 新版候选品牌资产

`resources/codex-floating-bar-logo.svg` 及由它导出的 PNG 是 2026-08-19
为本项目新设计的“守望机器人”候选品牌资产。其宽面罩呼应浮条的横向状态面板，薄荷绿
呼应正常额度提示，深海军蓝延续现有深色界面。设计过程使用 OpenAI 图像生成工具探索
IP 方向，最终 SVG 由项目内重新绘制的基础几何图形构成，不复用旧图标或第三方商标。
维护者最终选择了另一张原生生成稿作为正式图标，因此这些文件仅作为候选方案保留。

- 设计方向：Codex 指导下的项目定制“守望机器人”IP
- 概念稿：`logo-candidate-a-manta.png`、`logo-candidate-b-sentinel.png`、
  `logo-candidate-c-cloud.png`（OpenAI 图像生成工具原生输出）
- SVG SHA-256：`3c705eb6eebbe977c510a1d817c55992e705b2d621c0c9c4b1381173c826e30c`
- PNG SHA-256：`5c9cf3767292a25acb067e1a9534a99a9c2432bc7a4e5d96d435d58d4f29c111`

## 行为契约

当前项目使用原 WPF 服务生成的冻结对照结果，验证账户、配置、额度、Session 和 Token
统计语义。对照文件只包含脱敏 fixture、输出结果和来源指纹，不包含原 C# 源文件。

相关文件：

- `internal/codexdata/csharp_contract_test.go`
- `internal/codexdata/testdata/golden/csharp-oracle.json`
- `docs/native-p2-report.md`

## 第三方组件

第三方组件及完整许可文本见根目录
[`THIRD_PARTY_NOTICES.txt`](../THIRD_PARTY_NOTICES.txt)。正式便携包和安装包必须同时携带
`LICENSE` 与 `THIRD_PARTY_NOTICES.txt`。
