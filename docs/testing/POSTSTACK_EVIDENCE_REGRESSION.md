# 叠后刻度与数值回归证据

验收日期：2026-11-17。历史合成回归记录；不代表新发布构建的桌面验收。

## 修改与复测

- 剖面横轴沿用共享的 GDI 字形基线布局，默认增加 2 DIP，从 2 到 4；纵轴、标题专用间隔、刻度值与采样不变。使用生产绘制器向位图绘制同样的刻度文本，再实际数刻度线和字形之间的空白像素，而非比较外接框设置。
- 外部 B 按 A 当前实际可见绝对时间窗转换到 B 自己的样点索引。此前 B 的起点不同也先套用 A 的索引，导致合法重叠样点遗失。普通对比的单道分析和区域统计共享有效道头采样轴；实际道头 dt 不同不再产生虚假配对。
- 正式逐道滤波在最后一道回调之后也检查源身份；如果发布后的检查才发现失效，回收仅由本任务新写出的 artifact，不触碰已有输出。旧 B 在失败时不替换。

## 结果

| 场景 | 预期与实测 | 状态、证据 |
| --- | --- | --- |
| 横轴/纵轴视觉间隙，单、双、三图 | GDI 实际像素（横/纵）：96 DPI 为 4/5，120 为 5/5，144 为 6/7。实测桌面图中首尾正常，标题独立，双/三图绘图区顶底对齐；双图最大化、还原未改变坐标范围。 | 通过；`regression-window-native.log`、`A.jpg`、`B-empty.jpg`、`A-B.jpg`、`A-B-difference.jpg`、`A-B-maximized.jpg` |
| B 为 SNR 参考 | `0`, `6.02059991328`, `17.0329137812`, `−5.31478917042 dB`，四例各 2 个有效配对，容差 `1e−10 dB`；±∞、全零未定义、无配对不可计算都通过。 | 通过；`TestReferenceSNREnergyAndMasks`，`regression-numeric-gdi.log` |
| SNR 绝对时间与单道/区域一致 | A 800 ms、2 ms，分析 900–1200 ms；B 起点 750/850/1100 ms 时由错误的 126/126/1 个配对纠正为 151/151/51，区域和单道均为 0 dB。B 道头 dt=3 ms 时，B 波形仍有 902–1199 ms 的 100 点，但不生成 SNR/差值。 | 通过；`TestCompareDataInfoAlignsRegionalAndSingleTraceByAbsoluteTime`，`regression-numeric-gdi.log` |
| SEG-Y 全线、裁切、几何与稀疏匹配 | `IL1870`、`XL184…2456` 的 `2273×351`，缩窄、Wiggle 每 37 道和 17% 增益时，完整范围仍逐道逐样点一致；裁切 `XL1120–1322 / 1086–1176 ms` 为 `203×46`，`dt=2 ms`，首末 `936143 / 1138188`。IEEE float64 精确比较。稀疏 B 重排后按 A 的几何顺序输出 4 道，删中间/边缘各为 3 道，删边后最后 XL=225。 | 通过；`TestCompareNumericalExportRegression*`，`regression-numeric-gdi.log` |
| 滤波边界、响应与失败恢复 | `dt=2 ms` 的 8,000 点 10/40/100 Hz 各测第 `[2000:6000]` 样点，与独立闭式频响比较（绝对容差 `2e−8`），覆盖 Butterworth、Chebyshev I/II、Elliptic 及因果/双向配置。代表性零相位 Butterworth 带通增益为 `0.0023141172 / 0.9997001837 / 0.9999440060`。4 道保留 `t₀=800 ms` 和几何；脉冲不串入零道；P1/P1/P2/P1 的两次后续 P1 精确一致；重读 IEEE64 保留所有值。162.5 Hz 通过；短裁窗按真实上下文计算，250 Hz 拒绝，源变化和失败不覆盖旧 B。 | 通过；`TestTemporalFilterIndependentResponseAtKnownTones`、`TestBandpassModule*`、`TestPoststackFailedWorkerDoesNotReplaceAcceptedBOrDelta` |
| 单道分析与乱序 | 桌面第一行输入 `0.9–1.2 s` → `900–1200 ms`，A/B/差各 151 点；合成 A 物理道 12 对应 B 物理道 4，SNR 16.05 dB，Nyquist 250 Hz，默认可见谱至 125 Hz。框选只缩放波形视口，分析摘要和右侧频谱不变；振幅设置无重叠。自动验证无效草稿不覆盖快照；R2 先完成时，晚到的 R1 不覆盖 R2 道号、新 B、FFT、−6.0206 dB、标题、统计或 SVG；窗口销毁取消任务并清空待处理结果。 | 通过；`trace-applied.jpg`、`trace-local-zoom.jpg`、`trace-amplitude.jpg`、`trace-stats.jpg`；桌面 Q/W 从 0% 到 1% 再回 0%，输入焦点保护另见 `TestPoststackDisplayShortcutRoutesAndFocus` |
| “出”、数值阶段、文件名 | UI 直接显示一个导出窗，手动空间与时间同一行；从 GUI 写出已校验的 8×351 合成截面，重名选择 `_002`，成功后的两个操作变为可用。自动逐个重读 A/B/A−B 的基准/有效样值，核对已处理局部 B 裁切、重开自动/自定义名字和源保护。 | 通过；`export-manual.jpg`、`export-success.jpg`、`TestCompareNumericalExportLocalAndCommittedStage`、`TestCompareNumericalExportRegressionAutoNameReload` |
| 窗口关闭与任务 | 桌面预览成功后按下方“关闭”，Compare 的 B 和视窗均保留。原生 HWND 测试检查空闲 X、完成后关闭、失败 Alt+F4 对所属窗口状态无副作用，另覆盖响应/日志/设置关闭、运行中延迟销毁与取消清理。 | 桌面和原生测试分别通过所列场景；`algorithm-preview.jpg`、`algorithm-after-X.jpg`、`A-B-difference.jpg`、`regression-window-native.log` |

## 重跑

合成回归夹具在 `t.TempDir()` 生成：规则道使用 `1000*i+k`，稀疏道使用固定重排 `[3,1,0,2]`，滤波用确定性正弦与脉冲；无随机输入，不需要种子。真实资料不随仓库分发；有授权时可通过环境变量运行相应只读测试。GUI 使用已有合成 `Synthetic_A.sgy`，验证的是一个 8 道、351 样点、800–1500 ms 剖面；大夹具完整及裁切的数据正确性以上述文件重读断言为准。

```powershell
go test ./cmd/limage ./internal/poststack ./internal/segy ./internal/segyanalysis -count=1 -v -run 'Test(CompareDataInfo|CompareNumericalExport|NumericalExport|ReferenceSNR|TemporalFilter|BandpassModule|TraceScientific)'
go test ./cmd/limage -count=1 -v -run 'Test(CompareLabelPaddingUsesDIP|CompareEqualLayout|DisplayMeasuredLayout|Algorithm.*(Close|Native)|Poststack.*(Close|Shortcut|FailedWorker)|SettingsClose|LegacyParameterClose|TraceScientificLate|TraceScientificManual)'
go test ./... -count=1
git diff --check
go test -c -o "$env:TEMP\limage.windows.test.exe" ./cmd/limage
go build -trimpath '-ldflags=-s -w -H=windowsgui' -o "$env:TEMP\SeisForgeStudio_poststack_regression_x64.exe" ./cmd/limage
```

历史桌面记录为 125%（120 DPI）；100% 和 150% 只运行了原生 GDI 与布局测试，没有切换 Windows 缩放重新操作完整流程。实体 Alt+F4、运行中关闭及所有子窗口/窗口大小排列没有逐一在桌面重复；证据表区分了实际截图和原生测试覆盖。
