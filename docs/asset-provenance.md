# 资源来源与继承说明

## 项目沿革

CodexFloatBar 是原 CodexFloatingBar 项目的 Go/Win32 原生重写。新项目重新建立 Git
仓库，但保留对原项目来源、作者贡献和许可证的说明。原项目地址：

<https://github.com/liuguoqiang0730-svg/CodexFloatingBar>

原 WPF/C# 源文件、XAML、项目文件、旧截图和旧构建产物不包含在当前源码中。当前 Go
实现根据冻结的行为契约重新实现数据读取和桌面交互，并保留只读的旧设置迁移能力。

## 应用与托盘图标

当前的 [`resources/app-icon.ico`](../resources/app-icon.ico) 来自原项目：

- 原路径：`src/CodexFloatingBar/Assets/app-icon.ico`
- 加入提交：`2d1c91486682d11602310b83196a23fa1fa72615`
- 提交作者：`liyuejiong <yuejiong.li@ehang.com>`
- SHA-256：`400194ff1fe659694b53fa2e5c9821082145c8a3d63bc25b2bdaa3ebc6029cc7`
- 许可：随原 CodexFloatingBar 项目按 MIT License 分发

Git 历史没有记录该图标更早的设计来源或独立许可证，因此不得将其描述为新项目原创
资源。如果未来更换图标，应记录设计者、来源提交、许可和内容摘要。

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
