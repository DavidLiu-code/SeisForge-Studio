# 为 SeisForge Studio 贡献

感谢你帮助改进 **SeisForge Studio — Seismic Visualization, Reconstruction & Enhancement（震铸地震数据处理平台）**。

## 开始之前

- Bug、界面建议和小型功能可以直接提交 Issue。
- 架构调整、新文件格式或大规模界面改动，请先提交 Feature Request 并说明使用场景。
- 安全问题不要提交公开 Issue，请遵循 [SECURITY.md](SECURITY.md)。
- 不要上传客户数据、生产 SEG-Y、许可证、机器码、访问令牌或其他敏感信息。需要复现数据时，请使用可公开分发的最小合成数据。

## 开发环境

- Windows 10/11 x64
- Go 1.23 或更高兼容版本
- PowerShell 5.1 或更高版本

在仓库根目录运行测试：

```powershell
go test ./... -count=1
```

构建 Windows GUI 程序：

```powershell
powershell -ExecutionPolicy Bypass -File .\build_windows_release.ps1 -Output SeisForgeStudio_dev_windows_x64.exe
```

发布构建必须保持 PE Subsystem 为 Windows GUI，不能出现额外控制台窗口。

## 代码与兼容性要求

- 使用 `gofmt` 格式化修改过的 Go 文件。
- 保持 SEG-Y 读取只读；任何导出功能必须写入新文件并安全处理中断与临时文件。
- 不得无意改变既有色标顺序、gain/clip 定义、二维或三维归一化、相机操作、配置文件和索引缓存兼容性。
- 异步任务必须拒绝过期结果并及时释放 Reader、文件映射、GDI 对象和缓存资源。
- 新行为应有单元测试；涉及绘制、拾取或数据映射时，应增加回归测试或清晰的人工验收步骤。
- 文档、界面文字和测试名称应描述用户可见行为，避免记录真实项目坐标、绝对路径或地震振幅内容。

## Pull Request

一个 Pull Request 应尽量只解决一个主题，并包含：

1. 问题与解决方案摘要。
2. 用户可见变化和兼容性影响。
3. 执行过的测试及结果。
4. 界面变化的截图或短视频；请先移除敏感数据。
5. 对应 Issue（如有）。

提交贡献即表示你有权提供相关内容，并同意该贡献按本仓库的 [MIT License](LICENSE) 发布。
