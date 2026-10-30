# 叠前批量复现计划

`internal/prestackbatch` 提供 Win32-free 的批处理计划模型，用于把现有
Figure Recipe、流程快照和 QC 报告目标编排成确定性任务清单。它只保存元数据
和输出目标，不读取或复制 SEG-Y 振幅，也不直接执行渲染。

计划 schema：`seisforge.prestack.batch.v1`。

计划可以包含：

- Figure Recipe → PNG 和结果 JSON；
- 流程快照 → QC JSON 或 CSV；
- A/B 源文件路径、大小、修改时间和可选 SHA-256；
- 应用版本、输出目录和稳定任务编号。

任务按规范化 recipe/session 路径及输出路径稳定排序，输出目标必须位于计划
输出目录内，不能使用绝对路径或 `..` 穿越。计划 JSON 使用临时文件、同步和
原子发布；旧计划不会被无意覆盖。

核心包只负责校验和编排；实际执行由 Windows 命令行入口完成。图片继续使用
已有 Figure Recipe 渲染器，QC 报告继续使用 `PrestackIndex.BuildQCReport` 和
现有 CSV/JSON 导出器。批处理在每个任务前检查取消状态和源文件身份，图片任务
复用完整 SHA/像素校验，QC 任务通过独立 Reader 和缓存索引读取道头。

Windows 构建提供无窗口批处理入口：

```text
SeisForgeStudio.exe --batch-plan plan.json [--overwrite]
```

Figure 任务复用已验证的绘图 recipe 渲染器；QC 任务重新打开流程快照中的 A
文件、通过元数据缓存建立索引，并按快照当前道集导出 QC JSON/CSV。每个 QC
任务独立关闭 Reader；任何任务失败都会停止后续任务并返回非零退出码。批处理
入口不修改 SEG-Y，不写入索引缓存以外的源目录，也不把振幅数组写入报告。
