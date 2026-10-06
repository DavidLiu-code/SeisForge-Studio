# 构建与验证

## 环境要求

- Windows x64。
- Go 1.23 或与 `go.mod` 兼容的更高版本。
- Windows PowerShell。

项目当前仅依赖 Go 标准库，不需要 CGO、Python、OpenGL 或额外包管理器。

## 测试

在仓库根目录运行：

```powershell
go test ./... -count=1
```

测试覆盖 SEG-Y 解码、几何与工作区路由、二维/三维渲染回归、弯线项目、伪三维裁剪与缓存、导出以及文件/单道分析。少数真实测区验收测试由环境变量显式启用，默认测试不会依赖私有数据路径。

如需在本机运行外部数据验收，可在当前 PowerShell 会话中设置：

```powershell
$env:SEISFORGE_TEST_CROOKED_PROJECT_DIR = '<crooked-project-directory>'
$env:SEISFORGE_TEST_VOLUME_ZERO_CORNER_FILE = '<volume-segy-file>'
go test ./... -count=1
```

这些变量只指向开发者自行准备且有权使用的数据；仓库和 CI 不包含真实测区文件。

如需检查格式但不重写文件：

```powershell
$goFiles = Get-ChildItem .\cmd, .\internal -Recurse -Filter *.go
gofmt -d $goFiles.FullName
```

## Windows 发布构建

推荐使用仓库脚本：

```powershell
.\build_windows_release.cmd -Output SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe
```

也可以直接运行 PowerShell 脚本：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File .\build_windows_release.ps1 `
  -Output SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe
```

脚本会：

1. 运行 `go test ./... -count=1`。
2. 使用 `-trimpath` 和 `-ldflags="-s -w -H=windowsgui"` 构建 `./cmd/limage`。
3. 读取 PE Optional Header，确认 Subsystem 为 Windows GUI (`2`)。
4. 输出文件大小和 SHA-256。

等价的手工构建命令为：

```powershell
go build -trimpath `
  -ldflags="-s -w -H=windowsgui" `
  -o SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe `
  .\cmd\limage
```

不要省略 `-H=windowsgui`，否则双击程序时可能同时出现控制台窗口。

## 发布前检查

- `go test ./... -count=1` 通过。
- EXE 的 PE Subsystem 为 Windows GUI (`2`)。
- 版本号、主窗口、各工作区、About 和激活窗口显示 `SeisForge Studio 1.10.3`。
- 首页二维、三维和弯线入口分别进入正确工作区。
- 普通二维、弯线、真三维、伪三维和 SEG-Y 分析完成一次烟雾测试。
- SHA-256 与实际 Release 附件重新计算并一同发布。
- Release 仅包含当前程序、校验文件和必要说明；旧 EXE、测试输出、性能 profile、备份及私有 SEG-Y 不提交到源码仓库。

生成校验值：

```powershell
Get-FileHash .\SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe -Algorithm SHA256
```

## 非 Windows 平台

核心包中有不依赖 Win32 的计算代码，但桌面入口是 Windows 原生界面。当前项目不提供 Linux 或 macOS GUI 发布版。
