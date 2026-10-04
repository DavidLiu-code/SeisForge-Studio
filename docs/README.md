# SeisForge Studio 文档

本目录提供 SeisForge Studio 1.10.3 的使用、构建、架构和兼容性说明。

## 使用者

- [中文用户指南](USER_GUIDE.zh-CN.md)：从打开数据到二维、三维、弯线和 SEG-Y 分析的完整流程。
- [快捷键与鼠标操作](SHORTCUTS.md)：二维、弯线、真三维和伪三维交互速查。
- [兼容性说明](COMPATIBILITY.md)：旧 Limage 设置、缓存、Recent 和许可证的保留策略。
- [1.10.0 发布说明](releases/v1.10.0.md)：叠前独立工作区、道头索引、道集与 Geometry 使用说明。
- [1.10.2 发布说明](releases/v1.10.2.md)：原始叠前道序、范围读取和窗口调整稳定性。
- [1.10.3 发布说明](releases/v1.10.3.md)：叠前 QC、异步稳定性和震源—检波点联动。
- [原始叠前道序架构](architecture/ARCHITECTURE_PRESTACK_RAW_ORDER.md)：物理道序、范围、代次和窗口尺寸生命周期。
- [版本记录](../CHANGELOG.md)：1.9.x–1.10.x 用户可见变化。

## 开发者

- [当前架构](ARCHITECTURE.md)：核心包、数据流、异步边界和资源生命周期。
- [构建与验证](BUILDING.md)：Windows x64 测试、发布构建和 PE 校验。
- [品牌规范](BRAND.md)：产品名称、副标题、旧名称和文件命名规则。
- [第三方声明](../THIRD_PARTY_NOTICES.md)：设计参考与历史来源。
- [历史工程文档](history/README.md)：旧版本说明、Phase 架构和验证记录索引。

## 文档约定

- 未特别说明时，“道号”在界面中为 1-based；代码内部物理道索引通常为 0-based。
- SEG-Y 字节位置按行业文档使用 1-based 编号。
- `IL`、`XL`、`Time Slice`、`AOI` 等界面术语保留英文缩写，以便与道头和文件记录对应。
- 历史文档保留当时的产品名、文件名和测试口径；当前文档统一使用 SeisForge Studio。
