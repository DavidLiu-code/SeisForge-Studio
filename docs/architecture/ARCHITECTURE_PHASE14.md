# Limage Phase 14 架构说明：SEG-Y 文件与单道分析

## 目标与边界

Phase 14 为二维查看工作区增加轻量、只读的 SEG-Y 分析能力：查看主/扩展文本卷头、二进制卷头、所选道的 240 字节道头，以及单道波形、振幅统计和频谱。分析窗口不修改 SEG-Y，不持有工作区 Reader，也不改变二维、弯线、真三维或伪三维的色标、gain/clip、相机、缓存和参数文件。

本阶段继续采用现有固定长度道布局。二进制卷头声明可变长度道、附加道头块或 `-1` 个扩展文本头时，分析页给出兼容性警告，但不在本阶段重建可变长道索引或扫描 `EndText` stanza。样点格式码 4 可以查看全部卷头和道头；由于现有振幅解码器不支持该格式，波形与频谱明确显示不可用，不把无效结果画成零振幅。

## 只读接口

`internal/segy` 提供三组不暴露 `os.File` 的接口：

- `ReadTextHeaders` 返回主文本卷头和正计数扩展文本卷头。`TextHeader` 保存独立 `Raw` 副本、固定 40×80 行、自动识别结果，并可通过 `Decode` 在 ASCII 与 EBCDIC CP037 之间重复解码。
- `ReadBinaryHeader` 返回 400 字节原始副本、常用 Rev 0/1 字段、样点格式名称、端序、修订号和布局警告。
- `ReadTraceHeader` 按零基文件道号返回 240 字节原始副本及常用道头字段。坐标、高程和炮点号应用各自 SEG-Y scalar，`HeaderField.Raw` 仍保存未缩放整数。

所有 `HeaderField` 使用中文名称和 SEG-Y 1-based 字节位置：二进制卷头使用文件字节 3201–3600，道头使用 1–240。每项同时保存稳定 `Key`、原始整数、解释值和单位，界面无需再次按字节猜测类型。每次调用都重新分配 `Raw` 和字段切片，调用者修改返回值不会影响 Reader 或后续读取。

单道振幅继续复用 `File.ReadTraceWindow`，只读取当前可见样点窗或用户明确选择的完整道，不扫描整个文件。`internal/segyanalysis` 是无 UI 的纯计算层：

- `ComputeTraceStats` 计算有限值计数、零值计数、Min/Max/Mean/RMS/StdDev、峰值和精确 P01/P05/P50/P95/P99。零始终是有效振幅；NaN/Inf 只计数，不进入数值统计；不猜测或自动删除 `-999999` 等项目特定哨兵值。
- `ComputeTraceSpectrum` 对完整输入去均值、加 Hann 窗并向上零填充到二次幂，输出单边线性幅值和归一化 dB。最少 32 点、最大 NFFT 65536，频率轴由实际采样间隔计算。
- `ComputeAmplitudeSpectrum` 保留 floor-power-of-two、4096 上限的兼容选项，供已有平均频谱路径共享原算法口径。

## 选择与分析数据流

二维工作区只负责把屏幕位置解析成物理 SEG-Y 道，不把渲染位图列号当作源道号。选择结果统一表示为 `traceAnalysisSelection`，其 `traceAnalysisTarget` 只含角色、绝对路径、零基道号、可见样点范围、光标样点和可选 IL/XL；它不包含 HWND、Reader 或振幅数组。

主要集成点如下：

- 普通二维 A/B/差窗口通过“道”模式进入连续选道。Inline/Crossline 使用 Geometry 线的真实 trace/coordinate 列表找最近道；Time Slice 使用 `GeometryIndex.TraceAt`；差值面板只选择 A/B 共同 IL/XL 并生成一对目标。
- 弯线二维从下方剖面的当前显示坐标反算原始采集道和样点，保留当前线名、CDP 与校准后 XY 作为上下文。Geometry 总图缩放、AOI 与时间范围仍由原交互状态机拥有。
- 真三维只在 Inline、Crossline 或 Time Slice 的 `2D Full View` 中启用“道”。点击位置先由 `volumeWorldAtPixel` 还原为 IL/XL/样点，再吸附到当前 `GeometryIndex` 的实际网格并调用 `TraceAt`，因此分析的是物理 SEG-Y 道而不是切片纹理列。退出 Full View、切换体数据或返回三维时会同时退出选道模式；三维场景旋转、切片拖动和原双击逻辑不变。
- `showTraceAnalysisSelection` 复用一个非模态 `Limage64TraceAnalysis` 窗口。来源、界面 1-based 道号、上一道、下一道、转到以及“当前可见/完整道”都会进入同一加载函数。

分析窗包含“卷头”“道头”“单道分析”三页。卷头页可选择主/扩展文本头并手动覆盖编码，同时显示文件概览、二进制字段和 Hex；道头页显示所选道的解释字段、一致性警告和 Hex；单道页绘制时间向下的波形、统计信息与归一化频谱。A/B 目标分别显示；差值选择只有在采样间隔和样点窗一致时才计算逐样点 A-B，并用同一统计和频谱核心分析。复制操作只把当前页文本写入剪贴板，不写源文件。

## 异步代次与资源生命周期

`startTraceAnalysisLoad` 每次增加 `traceAnalysisGeneration`，克隆不含句柄的选择快照，然后启动后台构建。每个目标在 Worker 内单独 `segy.Open(path)`，按顺序读取卷头、道头和所需单道窗口，并在该目标完成或报错时 `defer Close`；Reader 永远不会进入返回结果或成为窗口全局状态。

Worker 仅产生 Go 数据副本，通过 `traceAnalysisReadyMu` 保护的待取表和 `WM_TRACE_ANALYSIS_READY = WM_USER + 520` 通知 UI 线程。结果入队和 UI 接收都必须匹配当前 generation 与有效窗口；新选择、切换道号/时间窗或关闭窗口后，旧结果不得替换当前页面。UI 处理消息时先从待取表删除对应项，再交换 `traceAnalysisCurrent` 并重建文本或重绘。关闭窗口会增加 generation、清空待取结果和 UI 引用；仍在运行的 Worker 最终关闭自己的 Reader，并在发现代次过期时直接丢弃结果，不向已销毁 HWND 发消息。

后台线程不调用控件更新、GDI 绘制或剪贴板 API。所有 HWND、文本控件、页面切换和绘图只在 UI 线程执行。分析窗口关闭不会关闭或替换二维工作区现有 Reader；二维工作区切线、重载和关闭也不会让已经复制进分析任务的路径/道号引用悬空。

## 测试与兼容性

自动测试覆盖：

- ASCII、EBCDIC CP037、主/扩展文本卷头、重复解码、40×80 行和返回副本隔离。
- 大端/小端二进制卷头、Rev 1 字段、中文字段及 1-based 字节位置。
- 正、负、零坐标/高程 scalar，道头 ns/dt 不一致、首末道和越界道号。
- 格式码 4 的卷头/道头仍可读且产生明确振幅解码警告。
- `-1` 扩展文本头计数、声明计数超出当前 Header 区域，以及小端文件中的扩展 EBCDIC 文本头。
- 单道统计的零、NaN/Inf、常数、大动态范围和精确百分位；正弦主频、Nyquist、短窗、非有限值与 FFT 上限。
- 二维/弯线的像素到真实道映射、A/B 共同几何配对、可见样点范围和分析选择模式退出。
- 异步快速换道、窗口关闭、过期 generation、Reader 关闭与格式码 4 的 header-only 路径。

Phase 14 不修改 `.lidx`、`.cidx`、`.ptx`、Recent 或任何 JSON 格式，也不增加全文件逐道统计。现有二维/三维显示和导出结果继续由原回归测试冻结。
