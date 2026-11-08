# 叠前 Gather / Compare：生成绘图代码

此功能把**当前完成绘制的图**保存为可重复执行的绘图配方。其他程序通过
SeisForge Studio 的命令行入口重新读取 SEG-Y 并绘制 PNG，不需要模拟鼠标，
也不需要 Python 或第三方绘图框架。第一版只支持叠前 Gather 与 Compare。

## 在界面中生成

1. 在 Gather 或 Compare 中调整好道集、道序、样点窗口、色标、增益、AGC、
   Image/Wiggle 或 Compare 振幅范围，等待加载与绘图完成。
2. 点击工具栏的 **生成绘图代码**，选择一个尚不存在的 `.figure` 目录名称。
3. 程序冻结当前视图，在后台校验数据、重新绘图并逐像素验证。一致后才发布目录。
   生成过程中按钮变为“取消生成”；窗口关闭或数据集切换也会取消任务。

目录包含：

- `recipe.json`：版本化配方、数据与程序 SHA-256、布局和渲染参数。
- `reference.png`：导出时的基准图，用于人工检查；复绘不会复制这张图代替计算。
- `render.ps1`：调用当前 EXE 的一键复绘脚本。
- `README.txt`：运行方法和注意事项。

只导出图像内容：Gather 包括标题和坐标；Compare 包括两行摘要和 A/B/Δ 面板。
按钮、状态栏、鼠标位置、Ctrl 预览竖线及拖动框不属于图像，不会保存。
窄窗口的工具栏仍可横向滚动，生成按钮不增加工具栏行。

## 他人或其他程序调用

在生成目录中运行：

```powershell
.\render.ps1
```

若系统禁止运行 PowerShell 脚本，请遵循本机策略，不需要也不建议为此修改策略。
可由现有程序直接调用下方 EXE 命令行入口；它不依赖 PowerShell 脚本。

文件移到其他电脑时，用参数指定**同一构建的 EXE**及内容相同的源数据：

```powershell
.\render.ps1 -Tool 'D:\Tools\SeisForgeStudio.exe' `
  -SourceA 'D:\Data\A.sgy' -SourceB 'D:\Data\B.sgy' `
  -Output 'D:\Results\comparison.png'
```

Gather 或未加载 B 的 Compare 不需要 `-SourceB`。执行电脑仍需要有效的
SeisForge Studio 本地许可证；命令行不会绕过授权，也不会弹出激活窗口。
脚本通过隐藏进程启动 Windows GUI EXE，并等待退出；不要只根据启动成功
判断绘图成功。脚本成功退出码为 0，同时输出 PNG 和机器可读的结果 JSON
（默认 `输出文件.png.result.json`）。直接调用 EXE 时结果 JSON 需指定 `--result`。

其他语言可通过创建子进程调用以下参数（应使用参数数组，而非拼接 shell 命令）：

```text
SeisForgeStudio.exe --render-recipe recipe.json --output figure.png
```

可选参数：`--source-a`、`--source-b`、`--result` 和 `--overwrite`。
默认不覆盖已有 PNG；只有明确指定 `--overwrite` 才允许替换目标图像。
输出扩展名必须为 `.png`，结果扩展名必须为 `.json`。
不得把输出路径指向源 SEG-Y、配方、基准图、脚本或 EXE；路径别名和硬链接也会检查。

## “完全一致”的边界

配方固定以下内容，而不是使用对方电脑当前打开的工作区：

- 解析后的真实物理道列表、一级分类、二级排序、逻辑可见范围和 Compare 行对应关系。
- 样点窗口、Trace/Offset 横轴、坐标文字、图像尺寸、各面板矩形及字体描述。
- 色标完整 LUT、增益、AGC、显示模式、Wiggle 减采样及填充、Compare 振幅范围。
- A/B 共享范围、浮点 A-B 和 Δ 零对称范围的原有生产渲染流程。

这是固定像素尺寸的二维复绘，不随运行电脑的窗口大小重新排版。
当前 Gather/Compare 没有三维相机角度；三维、弯线、Geometry 和 QC 不在第一版范围内。
为限制重绘内存，第一版只接受最多 16,777,216 像素的客户区，Compare 每面板栅格
不超过 4,194,304 像素；超大布局会明确拒绝，普通 4K 布局在此范围内。

严格复现要求：源 SEG-Y 内容一致、EXE 的 SHA-256 一致，以及 Windows/GDI
字体渲染环境兼容。仅版本名称相同不够。若字体替换、系统文字栅格化或其他原因
改变了最终像素，程序报告校验失败，**不发布不一致的新 PNG**。不承诺跨系统、
跨字体版本无条件像素相同。

数据文件会在复绘前后做完整 SHA-256 校验。大文件可能需要较长时间；校验流式执行，
不会把完整振幅体复制到内存。样点读取仍限于已有渲染器所需的当前视图。
不要在生成或复绘时修改源文件。哈希阶段可取消；已经进入底层样点渲染时需等待
当前有界渲染结束后释放 Reader，取消后不会发布文件。

配方包含本地路径和道头/匹配元数据，不包含振幅数组，也不会自动打包 SEG-Y。
对外分享前请检查路径和元数据的敏感性；接收方需要自行取得同一数据和授权程序。
配方严格绑定原图；直接修改 JSON 中的色标或窗口不再满足原图像素哈希，应该回到
界面修改显示并生成一个新配方。

## 开发与验证

实现分为 `internal/figurebundle`（格式、哈希、原子文件输出、脚本）和
`cmd/limage/prestack_figure_*_windows.go`（原生渲染、界面导出、CLI）。
实时界面与离屏复绘共用生产绘图函数，不复制另一套显示算法。异步任务仅携带
冻结快照，关闭/替换窗口后旧结果不能更新 UI。原始 SEG-Y、已有缓存与用户参数文件
均不修改。

测试覆盖数据驱动复绘与 GDI 基准像素相等、Image/Wiggle、Trace/Offset、Compare
缺失槽与禁用 Δ、宽窄布局、输入校验、取消、输出保护及脚本路径转义。
GUI 鼠标点击验收与自动化测试必须分开记录，自动化测试不能替代全部 GUI 人工验收。

已在本机激活且构建完成后，可以额外执行真实二进制回归（会生成并自动清理
小型临时 SEG-Y，不控制桌面鼠标，不弹出窗口；未设置环境变量则明确 skipped）：

```powershell
$env:SEISFORGE_FIGURE_TEST_TOOL = (Resolve-Path .\SeisForgeStudio_v1.10.4_figure_recipe_test_x64.exe).Path
go test ./cmd/limage -run '^TestPrestackFigureBuiltBinaryReplay$' -count=1 -v
Remove-Item Env:SEISFORGE_FIGURE_TEST_TOOL
```

仅在现有系统策略允许脚本执行时，另设置 `SEISFORGE_FIGURE_TEST_SCRIPT=1`
可通过生成的 PowerShell 脚本测试同一路径；测试不会更改执行策略。
