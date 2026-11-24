# SeisForge Studio poststack development rules

These rules cover poststack processing and Compare work. Keep changes scoped to the active development branch and do not modify unrelated worktrees.

## Boundaries

- Native Go and Win32 only. Do not add React, Electron, Python, GPU code, or a new GUI framework.
- A is read-only and the source SEG-Y is never modified.
- B owns an independent data source, Reader, Mapping, Index, cancellation function, and generation.
- Reuse the existing poststack Compare renderer; do not create a second A/B/Delta implementation.
- Do not change `.lidx`, `.cidx`, `.pidx`, `.ptx`, batch-plan schema, session-snapshot schema, or ordinary 2D/3D/curved/pseudo-3D behavior.
- Do not implement NMO, velocity analysis, stacking, AVO, Rebin, SEG-Y write-back, or undocumented processing algorithms.
- External algorithm modules are user-provided and authorized only. Never commit private algorithms, module binaries, dependencies, credentials, real project identifiers, machine paths, or test SEG-Y files.

## Lifecycle

Every background result must carry owner/window, workspace, dataset, processing, result, compare, and render generations plus the active panel dimensions. Stale results are released and discarded without updating UI state. Close, cancel, replace-B, page changes, range changes, and window close must release Readers, temporary files, bitmaps, and buffers.

## Readback and numerical regression safeguards

- Header-only format support is not amplitude-decoding support. Reject unsupported sample decoding explicitly; never turn an unreadable sample format into a successful blank plot.
- Geometry RAM reuse must remain bounded, independent of persistent `.lidx` storage. Eviction must not mutate an index already owned by a caller or change lazy grid preparation.
- Keep finite high-dynamic-range statistics and FFT calculations stable; reject genuinely unrepresentable results rather than reporting successful NaN/Inf or silently replacing a finite spectrum with zero.
- Trace picks carry the absolute time of the panel actually clicked. B's time origin must not be interpreted using A's sample clock.
- Distinguish native hidden-HWND checks, desktop GUI evidence, numerical/file readback and same-process load tests. Repeated test names and persistent cache files are not interchangeable with application resource leaks; report residual growth explicitly.

## Staged delivery

Keep documentation, pure algorithm/session code, B storage/Compare integration, and acceptance tests in separately reviewable commits. Run `gofmt`, `go test ./... -count=1`, `git diff --check`, Windows cross-compilation, and PE Subsystem 2 verification before each handoff.

## 工具栏与图标规范

1. 新增或修改工具栏按钮时，复用原生凸起／凹下边框、共享图标和 DPI 尺寸规则。普通命令松手后恢复；持续工具只有在真实启用时保持按下。焦点、悬停和禁用不能只靠颜色区别。
2. “比 / 体 / 谱 / 道 / 出”使用图标加短文字，在工具提示和无障碍名称中说明完整含义。
3. 相同功能在主窗口和 Compare 复用相同图标、文案、尺寸规则和动作语义。
4. “加载 B”“算法处理”“几何设置”等约定按钮不使用结尾省略号；长文件路径可以省略，但仍须能查看完整值。
5. 几何配置不重复放在主工具栏；IL↔XL 交换保留在“几何设置”。
6. 使用项目资源或兼容的矢量／原生绘制图标，不用 emoji 或依赖特殊字体的占位符。
7. UI 调整不改变既有命令、快捷键、数据语义或其他窗口状态；可编辑控件保留正常文字输入。
8. 验证常态、禁用、选中与高 DPI 状态，同时检查焦点反馈、文字裁切、基线对齐和图标清晰度。

## 科学绘图（scientific-figure）

1. 修改单道、频谱、科研图模板或导出前，阅读 [地震科研绘图约定](docs/SCIENTIFIC_FIGURES.md) 和 [显示设置](docs/DISPLAY_SETTINGS.md)。`scientific-figure-making/SKILL.md` 在当前环境未安装或未登记可读位置；若后续登记了真实路径，修改前也必须读取，不使用猜测路径。
2. 图形复用共享样式、模板、数值格式化和场景导出入口。屏幕、PNG 和矢量 SVG 使用同一绘图规则。
3. 同一次分析的波形、残差、频谱、统计和导出必须使用同一份已提交的单道样点快照。
4. 显示范围、勾选、颜色和显示增益只改变绘制，不改变分析数据、FFT 或数值导出。
5. A/B/差默认采用共同尺度；若新增独立归一化或额外倍率，必须明确标注。
6. 共同 dB 参考在同一分析快照内保持稳定，纵轴允许正值。零能量不得绘制成 0 dB 峰。
7. 黑红用于剖面波形极性；A/B/差的颜色和线型用于标识来源，两套含义分别管理。
8. 异步任务只能整组提交给仍存活的所属窗口，且必须是当前最新的请求。
9. 工具栏按钮使用共享样式，保留动作、键盘快捷键、短文案和完整工具提示。
10. 交付时检查实际界面和导出文件；未验收的 DPI 或交互须注明，文档中的路径、模板和命令必须真实存在。
11. 图内框选、平移和复位只改变显示，不能改动分析时间窗、样点、FFT 或 SNR。单道窗口的手动分析起止（绝对秒）、应用与取消在顶部“分析时间窗”下拉框右侧，只有空间不足才换行；非手动模式不插入额外工具行。单道频谱自动起始范围为 `0–min(125 Hz, Nyquist)`，完整谱仍用于计算。
12. A/B 信噪比默认 A 为含噪输入、B 为干净参考，在真实几何和绝对采样时间对齐后的共同有限样点上计算 `10 log10(Σ B² / Σ (A−B)²)`。变更公式必须使旧结果键失效；不交换文件角色或 A−B 方向，不修改频谱的 dB 参考。显示有效配对数、实际时间范围与滤波状态；B 不是经确认的干净参考时，应说明该值只描述 B 相对于残差的能量比。
13. 可折叠设置必须参与真实布局，不能覆盖工具区或画布。重绘、切页、设置窗口关闭均不得改变外层或其他窗口的状态。
14. 自动整洁刻度选择真实刻度位置；显示单位和格式不改道坐标、样点或采样率。Compare 的 B／差默认复用 A 的坐标配置和实际刻度，显式解除后才独立，不能据此合并各图色标。
15. 逐道时间滤波保留处理请求的真实物理道顺序和采样轴。显示抽稀、wiggle 道密度和像素不得决定处理或数值导出范围；不能靠丢道或悄悄降阶处理异常。
16. 数据导出默认当前可见范围，并展示数值阶段、实际范围、道数、样点数和采样轴。当前有效结果只读取已提交的样值；超出处理覆盖时提示重新生成，不能再滤一次或填零。滤波设计、响应预览和实际执行复用同一个数值核心。
17. “出”直接打开或激活同一个数据导出窗口，不先弹旧的选择框或写文件。“范围”选择在上一行，下一行左侧空间起止、右侧时间起止，同高对齐，切换范围模式不改变行高；自动名称使用原始来源名和实际输出范围，手改名不被刷新覆盖。自动名遇冲突另取序号，自定义名覆盖须明确确认，且不能覆盖输入文件。成功后的动作只对应最近一次仍有效的成功结果。SEG-Y 发布前回读验证每道几何、采样间隔、样点数与时间原点；不得用顺序编号冒充 Crossline。
18. 算法及其子窗口关闭只结束自己的窗口生命周期，不切换所属窗口或其他窗口的显示状态。算法窗口按内容紧凑布局；常用参数、选项卡和运行区保持可访问，处理结果刷新不调整外窗。普通叠后与 Compare 共用横轴标签布局：按字体和 DPI 测量刻度末端到数字可见字形的间隙，以纵轴实际视觉间隙为参照，不比较文字框或机械套用同一 padding。轴标题另留距离，屏幕与 BMP 共用布局。
19. 叠后数值 SEG-Y 导出默认沿用可写的输入采样格式，无法沿用时用 IEEE 32 位；允许手动选择 IBM 32 位、IEEE 32 位或 IEEE 64 位，格式选择只影响写出。使用标准 IL/XL 道头、可直接复用源道头的物理 A/B 视图滤波结果沿用原文本卷头与扩展卷头，不追加处理卷头，不增加卷头设置。此路径裁剪或转换格式只更新必要的结构字段；仍需记录来源或几何映射的输出保留必需的元数据。符合源卷头保留条件的整线导出，同格式且道数及样点数不变时，核对输出与源文件字节数一致；滤波计算不因导出而改变。
