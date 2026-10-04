# SeisForge Studio

[![Windows CI](https://github.com/DavidLiu-code/SeisForge-Studio/actions/workflows/ci.yml/badge.svg)](https://github.com/DavidLiu-code/SeisForge-Studio/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-2ea44f.svg)](LICENSE)

**Seismic Visualization, Reconstruction & Enhancement**<br>
**震铸地震数据处理平台**

SeisForge Studio 是面向 Windows x64 的原生 SEG-Y 浏览、质量检查与轻量分析工具。当前版本为 **1.10.3**，提供常规二维剖面、规则三维体、二维叠后弯线项目、伪三维幕布、叠前道集/QC 和只读 SEG-Y 文件分析。

> 项目此前使用 **Limage** 名称。历史版本文档中的 Limage/Fimage 名称为开发沿革记录；当前产品名称统一为 **SeisForge Studio**。

## 主要能力

| 工作区 | 能力 |
| --- | --- |
| 二维剖面 | Inline、Crossline、Time Slice 浏览；矩形缩放；A/B 联动和 A-B 差值；平均频谱；标注与剖面导出 |
| 三维数据体 | Inline、Crossline、Time 三正交切片；共享振幅归一化；CPU 深度缓冲；相机、FOV、轴倍率；A/B/差值和三维范围导出 |
| 二维叠后弯线 | 单文件、多选文件或文件夹项目；XY Geometry 总图；Trace/CDP/Distance 横轴；项目导航与道头坐标识别 |
| 伪三维幕布 | 多弯线按真实 XY-Time 位置竖立；测线筛选；空间 AOI 与时间窗裁剪；二维联动；范围导出；持久纹理缓存 |
| 叠前工作区 | CMP、Shot、Receiver、Common Offset 和原始道序浏览；Geometry、QC、单道分析和只读道集导出 |
| SEG-Y 分析 | ASCII/EBCDIC 文本卷头、二进制卷头、240 字节道头、原始 Hex、单道波形、统计和频谱 |

所有数据读取和渲染均在本机完成。程序按需读取道和样点窗口，不要求把完整地震体复制到内存。

## 快速开始

1. 从 GitHub Releases 下载 Windows x64 程序，或按[构建说明](docs/BUILDING.md)自行构建。
2. 启动程序，从首页选择“二维剖面”“三维数据体”或“弯曲测线”。
3. 打开 `.sgy` / `.segy` 文件；弯线工作区还可多选文件、打开文件夹或拖放项目。
4. 规则三维数据首次打开时会建立几何索引，界面显示识别、索引和首个切片的进度。
5. 在二维、弯线二维或三维的 2D Full View 中启用“道”，单击剖面查看单道波形、频谱和头信息。

详细操作见[中文用户指南](docs/USER_GUIDE.zh-CN.md)和[快捷键表](docs/SHORTCUTS.md)。

## 数据支持与边界

- 读取标准定长道 SEG-Y，支持大端和可识别的小端文件、扩展文本卷头以及 IBM/IEEE 浮点和常见整数样点格式。
- 样点格式码 4 可查看卷头和道头，但当前振幅解码器不绘制其波形。
- 三维几何默认从道头识别 Inline/Crossline；无法可靠识别时可手动设置字节位置或交换 IL/XL。
- 弯线坐标可从 Ensemble、Source 或 Group X/Y 自动识别，也可使用 1-based 自定义道头字节。
- 伪三维是沿测线轨迹布置的二维地震幕布，不对测线间振幅进行插值或体素化。
- 当前读取路径按固定长度道布局工作；文件声明可变长度道、附加道头块或不确定扩展文本头时，分析窗口会给出提示。

## 文档

- [文档导航](docs/README.md)
- [用户指南](docs/USER_GUIDE.zh-CN.md)
- [快捷键与鼠标操作](docs/SHORTCUTS.md)
- [当前架构](docs/ARCHITECTURE.md)
- [兼容性与旧名称迁移](docs/COMPATIBILITY.md)
- [构建与验证](docs/BUILDING.md)
- [1.10.3 发布说明](docs/releases/v1.10.3.md)
- [版本记录](CHANGELOG.md)
- [第三方声明](THIRD_PARTY_NOTICES.md)
- [历史工程文档索引](docs/history/README.md)

## 从源码构建

要求 Windows x64、Go 1.23 或更高兼容版本，以及 PowerShell。仓库仅使用 Go 标准库。

```powershell
go test ./... -count=1
.\build_windows_release.cmd -Output SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe
```

发布脚本使用 `-H=windowsgui` 构建，并校验 PE Subsystem 为 Windows GUI，双击启动不会附带控制台窗口。

## 兼容性说明

为继续读取既有用户配置、索引和许可证，1.10.3 暂时保留 `%LOCALAPPDATA%\Limage` 以及旧缓存内部标识。不要仅因产品改名手工移动或重命名这些文件。详情见[兼容性说明](docs/COMPATIBILITY.md)。

## 许可与反馈

源代码按 [MIT License](LICENSE) 发布。第三方参考与历史来源见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

问题、建议和可复现样例请通过 GitHub Issues 提交。请勿上传包含保密测线名称、坐标、振幅或客户信息的数据；必要时使用脱敏后的最小样例。
