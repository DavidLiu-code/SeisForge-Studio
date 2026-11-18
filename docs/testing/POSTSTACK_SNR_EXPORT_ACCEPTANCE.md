# 叠后 SNR、数值导出和算法窗口验收

- 历史合成回归记录；不是新发布构建的桌面验收。
- Windows x64 / Go `go1.26.7`：`go test ./... -count=1`、`go test -c ./cmd/limage`、`git diff --check` 和 Windows GUI 构建通过。
- 候选：`SeisForgeStudio_poststack_snr_export_compact_x64.exe`，9,247,744 字节，PE Subsystem `2`；SHA-256 `074DF9D0D80A771B7E63A544758410EAFD74FA41D886572F34FC4DCB4805348C`。

## 数值与文件

- 共同有限样点、B 参考能量：`A=2B` 为 `0 dB`，`A=1.5B` 约 `+6.0206 dB`，双点合计约 `+17.0329 dB`，排除 NaN 后约 `−5.3148 dB`。测试还覆盖正负无穷、两侧全零、无 B、版本失效及单道/区域共用同一实现。
- 合成 2,273 道验证 `900–1500 ms` 裁窗，逐道回读原几何，`t₀=900 ms`、`dt=2 ms`、301 样点。
- Windows 原生专项测试覆盖自动名称的实际采样端点、连续 `_001`/`_002`、保护输入文件与确认替换；布局测试覆盖 96/120/144 DPI 和窄窗。原生窗口测试覆盖 X、关闭按钮、Alt+F4 对应关闭消息，以及日志、响应子窗和其他已最小化窗口的隔离，不替代各 DPI 下的桌面实测。

## Windows 桌面实测

- 合成 A（Inline 1001，8 道 × 351 样点，800–1500 ms）进入 Compare；内置逐道时间滤波生成正式 B，A/B/A−B 同时显示。阶数和相位在常用参数可见，无纵向滚动栏；处理前后外框不变。点击算法窗口底部“关闭”，原 Compare 的位置、大小、线号和三幅图保留。
- 工具栏“出”直接打开统一导出窗口，没有旧的选择弹框；可见范围切为手动后，两个时间框原位变为可编辑，保持在右侧，窗口、摘要和底部按钮不移位。
- 实际连续导出同一配置两次，第一次使用 `Synthetic_A_A_XL2000-2007_T800-1500ms.sgy`，第二次使用 `_001.sgy`；两份均通过回读，并在文件系统中独立存在。导出只启用本次成功操作入口，未替换正式 B，合成 A 的 SHA-256 与测试前副本一致。
- “查看数据信息”实测为 `12.86 dB`，显示 A 含噪、B 为干净参考，统计范围为 8 道、2,808 个配对有效样点、800–1500 ms。
- 桌面输入工具对子窗口键盘会切回 Compare 父窗口，未人工遍历所有关闭/取消组合、手动输入和自定义覆盖确认，也未切换系统 DPI；相关已覆盖项见上方自动测试。未启动外部生产算法。本轮不改变源 SEG-Y、算法输入和缓存格式。

主要文件：`internal/segyanalysis/reference_snr*.go`；`cmd/limage/compare_data_info_windows*.go`、`trace_analysis_*`、`trace_scientific_stats_windows*.go`；`cmd/limage/compare_export_*`、`internal/segy/numerical_export*`、`cmd/limage/compare_windows.go`；`cmd/limage/poststack_processing_windows.go`、`poststack_algorithm_*`、`poststack_filter_response_windows.go`、`internal/poststack/bandpass_module.go`；`cmd/limage/display_plot_layout_windows*`、`display_surface_windows.go`，以及 `AGENTS.md` 和 `docs/SCIENTIFIC_FIGURES.md`。本段仅列本轮相关文件，保留工作树中此前未提交改动。
