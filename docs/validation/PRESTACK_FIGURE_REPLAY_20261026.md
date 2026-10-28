# 叠前绘图配方验证记录（2026-10-26）

## 来源与产物

- 工作树：`Limage_v1.8.7_src_pidx` / `prestack-pidx`。
- 基线：`657ae058e3b8cf66a7d999572c32a74931285f54`，另加本次未提交绘图配方功能。
- Go：`go1.26.7 windows/amd64`。
- 产品内版本保持 `1.10.4-rc1`，没有晋升版本、移动标签或覆盖既有候选产物。
- 构建：`build_windows_release.ps1 -Output SeisForgeStudio_v1.10.4_figure_recipe_test_x64.exe`。
- 文件大小：6,458,880 bytes。
- PE Subsystem：`2`（Windows GUI）。
- EXE SHA-256：`4ae5efe60d599ea492af80db61ac50805431750d3e41de82d857228893c8cc20`。

EXE 与测试 SEG-Y 不提交。主工作树原有插值修改未带入本工作树。

## 自动测试

| 项目 | 结果 |
| --- | --- |
| `go test ./... -count=1` | PASS；最终构建运行完整套件，cmd/limage 4.087s、figurebundle 0.750s |
| `git diff --check` | PASS |
| Gather Image/Wiggle × Trace/Offset，共用生产 GDI painter 与数据复绘像素对照 | PASS |
| Compare 匹配、缺失槽、窄窗口、不兼容轴、A-only、空筛选、4K 布局 | PASS |
| 固定布局、字体、色标 LUT、逻辑道/物理道映射和栅格尺寸检查 | PASS |
| 程序/源文件 SHA、修改振幅但恢复 mtime、参考图不符、无效配方拒绝 | PASS |
| 后台取消、串行 worker、旧 owner/dataset 结果丢弃 | PASS |
| GDI/DC/font/bitmap 与 Reader 释放、独立 A/B Reader 独占打开检查 | PASS |
| 新目录原子发布、并发目标、失败清理、PNG 覆盖保护、路径别名/硬链接保护 | PASS |
| PowerShell 脚本 BOM 与静态转义检查 | PASS |
| 原生 PowerShell 脚本运行（系统默认策略） | BLOCKED / 最终套件 SKIP：有效策略 Restricted；未更改策略 |
| 输出父目录 symlink 测试 | SKIP：创建 symlink 所需 Windows 权限不可用 |

前期独立参数转义测试曾使用测试子进程专用执行策略参数；最终测试已删除该参数，
只读查询现有策略，受限环境明确跳过脚本执行。生产生成脚本从未更改执行策略。

## 实际构建 EXE 的数据驱动复绘

使用 `SEISFORGE_FIGURE_TEST_TOOL` 指向上方最终产物，执行：

```text
go test ./cmd/limage -run '^TestPrestackFigureBuiltBinaryReplay$' -count=1 -v
```

正常许可证读取在沙箱中被拒绝后，经批准在沙箱外执行该固定测试。
使用本机现有授权流程，没有绕过许可证，也未读取或输出许可内容。
测试生成临时 A/B SEG-Y、捕获基准、生成配方，再启动真正的 Windows GUI EXE
命令行入口；不注入测试授权函数、不复制参考图作为结果、不创建可见窗口。

| 页面 | 实际输出 | 复绘像素 SHA-256 | 结果 |
| --- | --- | --- | --- |
| Gather | 900 × 463 | `da8c3193f228bfe6679bea8bb495f32866aa5d6535672a39537125d9df334bd7` | PASS |
| Compare | 860 × 472 | `8973eda79afa48bd49b8bfab902fd720bb465581d48fc02a85fc5a3aa4b1f35e` | PASS |

最终实际二进制测试耗时 0.475s。临时数据和输出由测试清理。
像素哈希包含尺寸和规范化 RGBA，不是 PNG 压缩文件字节哈希。

## 未执行与边界

- 未开启自动化鼠标；新增按钮的人工点击、保存对话框和真实桌面高 DPI 交互尚待验收。
- 未把这次离屏验证表述为完整 GUI 验收。
- 未对 4GB 真实 SEG-Y 执行完整 SHA 的性能基准；校验实现为流式、有限内存读取。
- 未承诺其他电脑字体/Windows 栅格化环境必然相同；不一致必须拒绝发布 PNG。
- 仅支持 Prestack Gather / Compare；不改变普通二维、三维、弯线、伪三维渲染。
- 不改源 SEG-Y、既有缓存或显示参数文件，不增加处理算法。
- 无 commit、push 或 Release；本次变更保留待审阅。
