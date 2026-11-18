# Third-party notices

SeisForge Studio 源代码按仓库根目录的 MIT License 发布。以下名称和项目仅用于说明设计参考与历史兼容来源；本项目不声称与其权利人存在隶属、认证或背书关系。

## CIGVis

SeisForge Studio 的部分三维视觉层级和交互设计参考了 CIGVis 的公开实现与文档，包括 turntable 风格相机、切片拖动和交界线视觉组织。相关功能由本项目使用原生 Win32 与 CPU 软件渲染独立实现。

CIGVis is distributed under the MIT License.<br>
Copyright (c) 2023 Jintao Li.<br>
Repository: <https://github.com/JintaoLee-Roger/cigvis>

SeisForge Studio 不捆绑 CIGVis Python 包、PySide6、VisPy 或 OpenGL 运行时。

## Legacy Fimage/Limage references

历史工程文档记录了早期 Fimage 工作流的兼容恢复，以及后续使用 Limage 名称的开发阶段。当前产品名称为 SeisForge Studio；Fimage 和 Limage 仅在历史说明、兼容目录、缓存标识和旧发布文件名中保留。

早期界面兼容内容包括工具栏布局、显示参数结构和 20 套历史色标。当前实现为原生 Windows x64 代码。涉及旧名称的历史文件正文保留，以维护版本与验证记录的可追溯性。

## Platform and toolchain

Windows 和相关 Win32 API 名称属于 Microsoft。Go 名称及工具链属于其各自权利人。仓库当前没有第三方 Go module 依赖；`go.mod` 仅声明本项目模块和 Go 版本。
