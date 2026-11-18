# SeisForge Studio QC 图表伪影诊断报告

日期：2026-10-03  
源码基线：`9c706b8`  
范围：叠前工作区 → QC 页签；只检查道头统计、Win32 绘制和窗口重绘，不读取或修改振幅数据。

## 结论

截图中的顶部点状摘要和底部蓝橙水平残带，与当前源码已经修正的布局不一致，最可能的原因是运行了旧的 `SeisForgeStudio_v1.10.3_prestack_qc_x64.exe` 或旧的 `layoutfix` 构建。两个旧程序的窗口标题都只显示 `v1.10.3`，标题不能区分构建版本。

当前源码还存在一个需要继续加固的窗口重绘风险：QC 页签在调整大小、显示/隐藏完成标签或移动进度控件时只调用 `InvalidateRect + UpdateWindow`，而父窗口使用了 `WS_CLIPCHILDREN` 且 `WM_ERASEBKGND` 返回 1。某些 Windows 重绘顺序下，原先被子控件占用的区域可能没有进入父窗口的有效更新区域，从而留下旧像素。这个风险可以解释“缩小后底部仍有色带”，但不能解释统计值变化。

## 证据

### 1. 统计数据不会随窗口缩放改变

- `internal/prestack/qc.go` 的质量统计和 32 个分箱只依赖 `PrestackIndex`、当前道集和分箱宽度。
- `BuildQCReport` 对当前道集做副本叠加，不修改全文件分布。
- 现有测试覆盖全文件分布、当前道集分布、零 Offset 和报告导出；没有窗口尺寸输入。
- 因此，缩放不应改变总道数、分箱数量或 Fold/Offset/Azimuth 统计。

### 2. 旧版布局确实会产生截图中的形状

旧版 `cmd/limage/prestack_qc_windows.go` 使用固定坐标：

- 摘要固定在 `y=115..268`；
- 图表固定从 `y=310` 开始；
- 图表底部使用 `clientBottom-50`；
- 标签又绘制到柱体底部下方。

窗口缩小或恢复后，这些固定区域可能与状态栏和完成标签重叠。公共 `drawAxisText` 还强制追加 `DT_SINGLELINE`，多行摘要会被裁剪为顶部的短线或点状残留。

### 3. 当前源码的 QC 绘图区已经被限制

当前未提交修改位于：

- `cmd/limage/prestack_qc_windows.go:24-72`：专用多行绘制、CRLF 规范化、内容矩形；
- `cmd/limage/prestack_qc_windows.go:82-146`：动态摘要高度、内容裁剪和窄窗口布局；
- `cmd/limage/prestack_qc_windows.go:178-194`：柱体内部裁剪；
- `cmd/limage/prestack_windows.go:1678-1707`：每次 `WM_PAINT` 先以白色兼容位图覆盖整个客户区；
- `cmd/limage/prestack_windows.go:2158-2195`：QC 的 `WM_SIZE`/`WM_EXITSIZEMOVE` 同步刷新。

当前布局为：

```text
内容左边 = 20
内容上边 = 114
内容右边 = clientRight - 20
内容下边 = clientBottom - 42
柱体裁剪下边 <= clientBottom - 92
```

因此，若运行的是最新构建，柱体不应绘制到状态栏或窗口底部；截图中仍延伸到下方的色带更像旧帧或旧程序输出。

## 仍需加固的代码路径

### A. 子控件区域的父窗口重绘不足

`layoutPrestackControls` 会移动/隐藏 `status`、`progress` 和 `progressLabel`。`setPrestackProgress`、`WM_SIZE` 和 `WM_EXITSIZEMOVE` 当前主要使用：

```text
InvalidateRect(parent, NULL, FALSE)
UpdateWindow(parent)
```

但窗口类返回 `1` 处理 `WM_ERASEBKGND`，并启用了 `WS_CLIPCHILDREN`。在部分 Windows 版本/主题下，隐藏子控件原区域不一定完整进入父窗口的更新区域，导致旧图表像素残留。

### B. 文本测量需要真正接收 `DT_CALCRECT` 的结果

当前 `measurePrestackQCText` 通过 `drawPrestackQCText` 测量，但后者接收 `RECT` 值副本；因此 `DrawTextW(DT_CALCRECT)` 对矩形的修改不会返回给调用方。代码目前用固定的 18 像素/显式行数作为兜底，能防止高度变成 1 像素，但这不是完整的 DPI 自适应测量。

### C. 截图不能证明使用了哪个构建

已知文件：

| 文件 | SHA-256 | 说明 |
|---|---|---|
| `SeisForgeStudio_v1.10.3_prestack_qc_x64.exe` | `A133BC31AA7C6233A52D7214CD074F4D16C090039317D368B93DC96DE554A62F` | 旧版 |
| `SeisForgeStudio_v1.10.3_prestack_qc_layoutfix_x64.exe` | `974ACD21D13F525956C8609BF1B77931A53764DB580433EB9CCC802C90F9362E` | 第一轮布局修复 |
| `SeisForgeStudio_v1.10.3_prestack_qc_layoutfix2_x64.exe` | `4BBF86AEE25168FB8050F4A04183BF05C302BBAFAAF4F97F5C349974557BB95D` | 当前源码对应构建 |

请先对实际运行文件执行：

```powershell
Get-FileHash .\SeisForgeStudio_v1.10.3_prestack_qc_layoutfix2_x64.exe -Algorithm SHA256
```

只有第三个哈希才能证明运行的是当前修复版本。

## 建议的下一步最小修复

1. 增加 `RedrawWindow` 调用，在 QC 页签的 `WM_SIZE`、`WM_EXITSIZEMOVE`、`setPrestackProgress` 和页签切换后执行：

   ```text
   RDW_INVALIDATE | RDW_ERASE | RDW_UPDATENOW | RDW_ALLCHILDREN
   ```

   这样可强制刷新父窗口和进度/完成标签的旧区域。

2. 将 QC 文本测量改为接收 `*RECT` 或直接在 `measurePrestackQCText` 中调用 `DrawTextW`，确保 `DT_CALCRECT` 的真实高度参与布局。

3. 在 `paintPrestack` 中显式设置 `SetTextColor(RGB(0,0,0))` 和 `SetBkMode(TRANSPARENT)`，避免兼容 DC 继承不可预测的文字状态。

4. 在 Windows GUI 环境做一次可重复验收：记录客户区、内容矩形、图表矩形和状态栏位置；连续缩放至 80%、67%、50%、33% 后恢复最大化，检查内容矩形外非背景像素数量。

## 已完成验证

- `GOOS=windows GOARCH=amd64 go test ./... -count=1`：通过。
- `git diff --check`：通过。
- `SeisForgeStudio_v1.10.3_prestack_qc_layoutfix4_x64.exe`：构建通过。
- PE Subsystem：Windows GUI（Subsystem 2）。

## 限制

当前执行环境没有 Wine 或 Windows GUI，无法直接复现用户的拖动缩放过程。因此，本报告可以确认源码路径和构建差异，但不能仅凭截图确认用户实际运行的 EXE 或测量 Windows 的真实更新区域。建议先核对 SHA-256；若确认运行 `layoutfix2` 后仍有残带，再实施上面的 `RedrawWindow` 加固。

## 后续加固已实施（layoutfix4）

针对“默认尺寸与最大尺寸显示不一致”，已在源码中实施以下修复：

- 新增 `RedrawWindow` 的 `RDW_INVALIDATE | RDW_ERASE | RDW_UPDATENOW | RDW_ALLCHILDREN` 全窗口刷新路径；
- QC 页签切换、窗口调整、进度条/完成标签切换时统一刷新父窗口和所有子控件区域；
- `DrawTextW(DT_CALCRECT)` 改为通过 `RECT` 指针返回真实测量高度，不再只依赖固定行高兜底；
- 每个 QC 兼容 DC 显式设置黑色文字和透明背景，避免恢复/最大化后继承不一致的 GDI 状态。

新构建（重绘加固）：

```text
SeisForgeStudio_v1.10.3_prestack_qc_layoutfix4_x64.exe
SHA-256: E3419A5FE287B8C0AE1F8B0957A927DBF33896AE951604A0FA63ACAFC2AC4D3F
```

该版本仍需在 Windows 上进行实际拖动缩放验收；如果 layoutfix4 仍出现伪影，应记录实际 EXE 路径、SHA-256、客户区尺寸和 QC 内容矩形，以便继续定位 DPI 或主题相关的 GDI 行为。

## 全部叠前页签同步修复

随后将同一策略扩展到 Gather、Geometry、Mapping 和 Compare：

- 页签切换、窗口缩放、进度条/完成标签切换都刷新父窗口及所有子控件；
- Compare 页面改用与 QC 相同的内容上下边界和裁剪方式；
- 默认窗口客户区契约和最小尺寸对所有页签统一生效；
- Gather 保留调整过程中的最后一帧预览，不触发额外 SEG-Y 读取。

对应测试程序：

```text
SeisForgeStudio_v1.10.3_prestack_panel_layoutfix_x64.exe
SHA-256: 1DD0754FE5E67B48E6B1A6ABAAF9693E2DC091CBF79D8512A6797F493D2FF2C8
```

另外已补齐窗口尺寸契约：创建叠前窗口时先用 `AdjustWindowRectEx` 将目标客户区 `1360×880` 转换为外框尺寸，并增加 `WM_GETMINMAXINFO` 的最小跟踪尺寸 `1120×700`。这样默认窗口不会因标题栏/边框被重复扣除而比布局基准小一截；最大化仍按系统客户区自适应，但两种状态使用同一套内容矩形计算。

