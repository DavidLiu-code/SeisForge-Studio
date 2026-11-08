# 叠前流程快照与恢复

叠前工作区支持保存和恢复 `seisforge.prestack.session.v1` 快照。快照记录
Gather、Compare、QC、Geometry 和 Mapping 的元数据与显示状态，不复制 SEG-Y
振幅、不保存 Reader、GDI 对象或派生索引。

## 使用

在 Gather 或 Compare 工具栏点击“保存流程”保存 JSON；点击“打开流程”选择
已有快照。恢复会重新打开 A/B 数据、按快照 Mapping 重建索引，并恢复道集、
一级/二级排序、分箱、样点范围、色标、增益、AGC 和 Compare 筛选。

文件移动后可以重新选择同一 SEG-Y。恢复阶段先检查文件大小和修改时间；生成
绘图代码或导出前仍由绘图配方执行完整 SHA-256 校验，以保证可复现输出。

A 或 B 无法验证时，不会覆盖当前已打开的工作区；B 恢复失败时保留 A 并显示
错误原因。快照不改变既有 `.lidx`、`.cidx`、`.pidx`、`.ptx` 或 JSON 参数格式。

## 兼容与边界

快照采用严格 schema 版本校验，未知版本、非法范围和不完整 Mapping 会被拒绝。
它只恢复浏览状态，不执行 NMO、速度分析、叠加、AVO、Rebin、振幅插值或
SEG-Y 写回。
