# SeisForge Studio 品牌规范

## 标准名称

| 用途 | 标准写法 |
| --- | --- |
| 产品名 | **SeisForge Studio** |
| 英文副标题 | **Seismic Visualization, Reconstruction & Enhancement** |
| 中文名 | **震铸地震数据处理平台** |
| 当前版本 | **1.9.15** |
| 可执行文件前缀 | `SeisForgeStudio_` |

产品名中的 `SeisForge` 和 `Studio` 均首字母大写，中间保留空格。文件名因跨工具兼容可写为无空格的 `SeisForgeStudio`。不要使用 `Seisforge`、`Seis Forge` 或 `SeisForgeStudio` 作为界面标题。

推荐的首次出现形式：

```text
SeisForge Studio
Seismic Visualization, Reconstruction & Enhancement
震铸地震数据处理平台
```

正文后续可简称“SeisForge Studio”或“本程序”，不单独把 “SeisForge” 当作另一个产品名。

## 版本与文件命名

- 窗口标题：`SeisForge Studio v1.9.15 [x64] - <工作区>`。
- Windows 发布文件：`SeisForgeStudio_v1.9.15_windows_x64.exe`。
- 校验文件：`SHA256SUMS_v1.9.15.txt`。
- GitHub Release 标签：`v1.9.15`。

版本号与产品名分离。功能代号可出现在 Release Notes 中，不写入长期产品名。

## 旧名称

- **Limage** 是 1.9.15 及更早开发过程使用的旧产品名。
- **Fimage** 只用于说明早期兼容界面和历史来源，不是 SeisForge Studio 的别名。
- 新增的用户界面、当前文档、问题模板和发布说明不再使用 Limage 作为品牌。
- 历史架构、版本、验证和校验记录保留原名称，避免改变既有证据、文件名和哈希语义；在索引页统一解释即可。
- `%LOCALAPPDATA%\Limage`、缓存 magic、许可证派生字符串和 Win32 类名属于兼容标识，不作为用户可见品牌宣传。

## 文案语气

- 优先使用明确、可验证的行为描述，例如“按需读取当前样点窗”。
- 对伪三维明确使用“幕布”，不得表述为测线间插值后的体数据。
- 不宣称支持尚未实现的解释、反演、重建算法；英文副标题表达产品方向，不等于每项能力已在 1.9.15 完成。
- 性能数据必须同时注明数据规模、缓存条件和机器差异，不把单机验收结果写成普遍保证。

## 商标与第三方名称

SEG-Y、CIGVis、Windows、Go 等名称仅用于兼容性或来源说明。第三方许可与引用集中记录在 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。本项目不声称与这些项目或权利人存在隶属或背书关系。
