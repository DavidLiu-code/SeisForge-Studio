# Prestack 批处理执行报告

批处理计划执行时会在计划旁生成一个独立的 metadata-only sidecar：

```text
<plan-stem>.execution.json
```

格式版本为 `seisforge.prestack.batch.execution.v1`，不改变既有
`seisforge.prestack.batch.v1` 计划格式。报告记录：

- 计划路径、应用版本、运行 ID、开始/结束时间和覆盖模式；
- Figure → QC 的固定任务顺序；
- 每个任务的状态、输出路径、Figure result 路径、耗时时间戳和错误信息；
- 整体成功、失败或取消状态。

报告不包含 Reader、索引指针、振幅、样点数组或完整图像缓存。CLI 可以用
`--batch-report report.json` 指定 sidecar 路径；未指定时使用计划旁的默认路径。
GUI 执行完成后会在状态栏显示计划和执行报告路径。

在叠前 Gather/Compare 工具栏点击“查看执行报告”可以打开只读查看器。查看器
只读取该 JSON sidecar，不打开 SEG-Y Reader，也不读取振幅。可按“全部任务”、
“失败/取消”、“仅成功”或“未完成”筛选任务；“复制报告路径”复制 sidecar
路径，“打开输出目录”打开报告中第一个任务输出所在目录。筛选和路径操作不会
修改报告、计划或任何源文件。

报告采用临时文件写入、同步关闭后原子发布。报告写入失败只产生警告，不改变既有
Figure/QC 执行顺序或源文件保护逻辑。
