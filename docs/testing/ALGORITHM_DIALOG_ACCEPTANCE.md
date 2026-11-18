# 叠后独立算法处理窗口验收

## 已通过

- 原生 Windows：Go `1.26.7 windows/amd64`；`gofmt`、`go test ./... -count=1`、`git diff --check`、Windows 测试程序编译均通过。
- 自动回归覆盖常用参数顺序、折叠和固定底栏、窄窗口、100%/150%/200% DPI 布局、GDI 字体释放、日志和进度、预览与正式结果分离、过期任务、取消及关闭后的清理确认。DPI 和资源项为自动测试，不是三台显示器的人工验收。
- 真实 Windows 窗口使用 36 道 × 256 样点的二维合成文件：从“比”进入 Compare，点击“算法处理…”只打开独立窗口，未执行任务；确认参数布局、运行设置和固定底栏可达；关闭并重开后保留展开状态。

## GUI 截图与未验收项

- 完成状态截图、GUI 的真实预览/正式执行、运行中取消及关闭确认未计为通过：外部公开 Copy 模块首次运行须经用户确认；相应状态转换与清理已由自动测试覆盖。未使用真实工区数据。

## 交付构建

- `SeisForgeStudio_poststack_algorithm_ui_x64.exe`：6,534,656 字节；PE Subsystem `2`（Windows GUI）。
- SHA-256：`E3CC8C458DF9A1AF51F72D9BCD2A8CBB58564887DA9423BBA3F413C434C708C6`。

## 后续：Compare 即时分栏与运行反馈

- 进入“比”即显示 A 与 B 占位；B 准备期间显示加载信息，实际渲染后才提示 B 已显示。退出 Compare 恢复单幅 A。只在有 B 且启用差值时显示第三幅。
- 原生 Win32 按钮与命中测试通过；完整合成回归通过真实的 `BM_CLICK`、公开 Copy 子进程、artifact B 加载、A/B/差画面生成。10 道 × 55 样点可见窗保留，Copy 差值为零，A 字节未改。授权确认后再校验原窗口/输入；同尺寸、同视图重绘不取消任务，真实输入变化仍取消并显示原因。
- 修复版 `SeisForgeStudio_poststack_compare_feedback_x64.exe`：6,544,896 字节，PE Subsystem `2`，SHA-256 `CCE00D5B9D98F19FF9EF0869F7054C7DDAAE97D946ADD998F86723F8BA0E7051`。
