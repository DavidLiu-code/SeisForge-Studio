# 历史工程文档索引

SeisForge Studio 在 1.9.15 及更早开发阶段使用 **Limage** 名称；更早的界面恢复记录还会出现 **Fimage**。这些名称、旧 EXE 文件名、版本标题和校验值是历史证据，不代表当前产品名称，因此归档正文保持原样。

当前使用说明以[根 README](../../README.md)、[用户指南](../USER_GUIDE.zh-CN.md)和[当前架构](../ARCHITECTURE.md)为准。

## Phase 架构记录

Phase 架构文件归档在 `docs/architecture/`：

- [Phase 1：Dataset / Geometry / Workspace / Application](../architecture/ARCHITECTURE_PHASE1.md)
- [Phase 2：二维叠后弯线与三维索引进度](../architecture/ARCHITECTURE_PHASE2.md)
- [Phase 3：多测线弯线项目](../architecture/ARCHITECTURE_PHASE3.md)
- [Phase 4：空弯线工作区与伪三维](../architecture/ARCHITECTURE_PHASE4.md)
- [Phase 5：XY AOI 与三维操作统一](../architecture/ARCHITECTURE_PHASE5.md)
- [Phase 6：幕布拾取、范围编辑与风格](../architecture/ARCHITECTURE_PHASE6.md)
- [Phase 7：二维联动与 AOI 增量更新](../architecture/ARCHITECTURE_PHASE7.md)
- [Phase 8：交互与完成状态细化](../architecture/ARCHITECTURE_PHASE8.md)
- [Phase 9：合并 I/O 与持久纹理缓存](../architecture/ARCHITECTURE_PHASE9.md)
- [Phase 10：单次坐标检测与加载调度](../architecture/ARCHITECTURE_PHASE10.md)
- [Phase 11：伪三维时间范围](../architecture/ARCHITECTURE_PHASE11.md)
- [Phase 12：稀疏支撑读取与并行 Z-buffer](../architecture/ARCHITECTURE_PHASE12.md)
- Phase 13 未单独形成架构文件；实现与验收见[伪三维裁剪导出验证](validation/VALIDATION_v1.9.13_pseudo_crop_export.txt)和[CHANGELOG](../../CHANGELOG.md)。
- [Phase 14：SEG-Y 文件与单道分析](../architecture/ARCHITECTURE_PHASE14.md)
- [Phase 15：卷头与道头表格化](../architecture/ARCHITECTURE_PHASE15.md)
- [真三维共享归一化修复](../architecture/ARCHITECTURE_VOLUME_CLIM_FIX.md)

## 早期版本说明

`V*.md` 文件记录 1.0.0–1.8.7 的逐版本功能与修复，当前归档目录按文件名保留：

- `V1_COMPARE_LINK_ANNOTATION.md`、`V1_0_1_*` 至 `V1_0_3_*`：二维比较、联动和闪烁修复。
- `V1_1_0_*` 至 `V1_3_3_*`：差值、频谱、Time Slice、导出、许可证和三维基础。
- `V1_4_0_*` 至 `V1_6_8_*`：真三维视觉、相机、FOV、坐标轴和默认参数。
- `V1_7_0_*` 至 `V1_7_2_*`：三维 A/B/差值、子体导出和数字键盘。
- `V1_8_0_*` 至 `V1_8_7_*`：首页、工作区、拖放及三维直达入口。
- `V8_CHANGES.md`、`V9_COMPARE_TIMESLICE.md`、`V10_AUTO_GEOMETRY.md`：更早的阶段编号文档。

原型界面恢复说明位于：

- `UI_RECONSTRUCTION_V2.md` 至 `UI_RECONSTRUCTION_V7_GAIN.md`
- `PORTING_STATUS.md`

这些文档描述当时的恢复过程，不能替代当前功能说明。

## 验证与校验

旧版 `VALIDATION_*.txt`、`SHA256SUMS_*.txt`、benchmark 和测试结果归档在 `docs/history/validation/`。其中可能记录仅适用于开发机的测试路径、旧文件名和当时的性能环境；引用结果时必须同时保留版本与测试条件。

历史 SHA-256 只对应原始旧版 EXE。任何重新命名、重新构建或品牌更新后的程序都必须生成新的校验值，不得沿用历史记录。
