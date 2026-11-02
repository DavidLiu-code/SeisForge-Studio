# Prestack 批处理计划编辑器（第一阶段）

当前版本提供计划编辑核心模型，兼容既有 `seisforge.prestack.batch.v1` JSON。

## 支持的编辑

- 查看 Figure 与 QC 任务摘要；
- 在各自任务类型内移动顺序；
- 删除任务；
- 修改输出目录；
- 修改 Figure 的 PNG/result 文件名；
- 修改 QC 输出文件名和 JSON/CSV 格式；
- 保存前执行完整路径、重复目标和源身份校验。

编辑只操作计划元数据，不读取振幅、不持有 SEG-Y Reader，也不会修改源文件。

## 执行顺序兼容性

现有 v1 schema 将 Figure 和 QC 分成两个数组，执行器仍按 Figure → QC 的顺序执行。
因此第一阶段只允许在同一类型内排序，不引入跨类型任务列表，避免改变已有计划的执行语义。

核心 API 位于 `internal/prestackbatch/editor.go`：

```go
editor, err := prestackbatch.NewEditor(plan)
tasks := editor.Tasks()
err = editor.Move(ref, destination)
err = editor.Remove(ref)
err = editor.SetOutputDirectory(directory)
err = editor.UpdateFigureOutput(ref, output, result)
err = editor.UpdateQCOutput(ref, output, format)
err = editor.Save(path, overwrite)
```

`Plan()`、`Tasks()` 和编辑器内部均使用防御性副本。任何失败的路径、格式或重复目标更新都不会部分修改当前计划。

## 原生 Win32 编辑窗口

叠前 Gather 和 Compare 工具栏现在提供“编辑批处理计划”入口。打开计划后，独立窗口只持有
`PlanEditor` 的元数据副本，并提供：

- Figure/QC 任务列表（固定 Figure → QC 分组）；
- 同类型任务上移、下移和删除；
- 输出目录重映射；
- Figure PNG/result 文件名和 QC JSON/CSV 文件名、格式修改；
- “另存为”保存新计划，拒绝覆盖原计划或已有目标。

保存后编辑器会重新加载计划并执行 `Validate`。取消或关闭窗口会丢弃全部未保存修改，不打开
Reader、不读取 SEG-Y，也不改变当前 Gather/Compare 工作区。
