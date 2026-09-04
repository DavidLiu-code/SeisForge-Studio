# SeisForge Studio 1.9.15 架构

## 设计目标

SeisForge Studio 是原生 Windows x64 应用。核心层负责 SEG-Y、几何、项目、缓存和无 UI 计算；`cmd/limage` 通过 Win32 适配器复用现有二维、真三维和伪三维绘制路径。主要约束是：

- Dataset、Geometry 与 Workspace 相互独立。
- 每个工作区拥有独立 Reader 生命周期，不共享可被另一窗口关闭的文件句柄。
- 后台任务只产生 Go 数据并通过窗口消息交付，绝不直接操作 HWND。
- 显示参数、几何缓存和源振幅分离；缓存失效不改变 SEG-Y。
- 三维与伪三维是两个独立渲染器，伪三维不冒充体数据。

## 分层与依赖

```text
Home / Recent / Drag & Drop / Workspace commands
                        |
                 internal/app
              /         |         \
 internal/dataset  internal/project  internal/workspace
          \          internal/geometry          /
                         |
                    internal/segy
                         |
          Win32 adapters and sessions (cmd/limage)
            /               |                 \
       2-D/Compare       Volume3D        Crooked/Pseudo3D
```

核心包职责：

| 包 | 职责 |
| --- | --- |
| `internal/app` | 文件/项目到工作区的统一路由，维护当前 Dataset 或 CrookedProject |
| `internal/dataset` | 绝对路径和不可变文件元数据；按需创建独立 `segy.File` |
| `internal/geometry` | Line2D、Regular3D、CrookedLine 几何及 Bounds/TraceLocation |
| `internal/project` | 多弯线项目、自然排序、导航文件解析和线名匹配 |
| `internal/workspace` | 工作区注册、原子切换、恢复历史和异步 generation |
| `internal/segy` | 头解析、样点解码、索引、切片、振幅映射、频谱兼容路径和 SEG-Y 导出 |
| `internal/pseudo3d` | XY/时间裁剪、幕布模型、相机投影、拾取和 CPU Z-buffer |
| `internal/pseudocache` | `.ptx` 持久纹理缓存、CRC、原子写入和 LRU 清理 |
| `internal/pseudoexport` | 伪三维裁剪导出计划、校验、执行和 JSON 清单 |
| `internal/segyanalysis` | 单道统计、FFT 和无 UI 分析计算 |
| `internal/license` | 机器码载荷和 Ed25519 签名验证 |

`cmd/limage` 仍持有各 Win32 窗口的 session、控件、GDI 缓冲和兼容全局状态。核心包不依赖 Win32。

## 打开与工作区切换

统一路径为：

```text
Application.OpenPath/OpenCrookedPaths/OpenCrookedFolder
  -> Dataset metadata validation or CrookedProject creation
  -> WorkspaceManager.Open/OpenProject/OpenEmpty
  -> target adapter accepts request
  -> commit generation and hide previous workspace
  -> asynchronous geometry/render preparation
```

同步打开失败时，Manager 不提交切换，原工作区保持可用。切换已提交后发生的几何或渲染失败留在目标工作区显示，不自动回退到错误的二维页面。

Workspace 数字 `1/2/3` 分别保持二维、三维、弯线 Recent 兼容。Geometry 类型与 Workspace 类型无隐式等价关系；显式入口优先于自动推荐。

## 数据和 Reader 生命周期

`SeismicDataset` 只保存路径、大小、修改时间、端序、采样间隔、样点数、格式码、道数和可选 Geometry。初次打开只验证头和记录元数据，随后关闭验证 Reader。

二维 A/B、真三维、活动弯线、后台几何、伪三维单线任务和分析窗口均通过 `OpenReader` 获得独立句柄。任务完成、取消、过期或报错时由创建者关闭。伪三维纹理生成后不保留该线 Reader。

## 几何与项目

- `Line2DGeometry` 按原始道序表示普通二维。
- `RegularGridGeometry` 包装 `.lidx` 中的 Inline/Crossline 索引并提供精确 bin 查找。
- `CrookedLineGeometry` 保存道号、CDP、X/Y、累计距离和范围，不复制振幅。
- `CrookedProject` 保存项目根、可选导航文件和测线数据集列表，不保存活动线、视窗或相机。

弯线项目可用导航点进行线名匹配和十进制坐标校准，但最终显示采用逐道 SEG-Y Geometry 的校准副本。原始 `.cidx` 和源道头均不修改。

## 渲染数据流

### 二维与比较

二维按当前 trace/sample 范围读取，映射为八位色标索引，再生成 BGRA 缓冲。静态剖面/坐标与动态十字线、标注分层绘制，减少鼠标移动重绘。A/B 与差值面按几何位置联动。

### 真三维

Inline、Crossline 和 Time Slice 保留各自显示栅格，但从三张当前切片的合并有限值分布计算一组共享 `MapMin/MapMax`。三面随后使用相同色标区间映射，避免局部极值令某一面全黑。软件投影和 Z-buffer 在 CPU 上执行。

### 伪三维

每条弯线按真实 XY 轨迹分成竖直纹理四边形；时间使用实际采样间隔。XY AOI 由 `ClipTrajectory` 产生一个或多个连续道段，时间窗换算为每条线自己的样点范围。渲染使用透视校正纹理插值和 Z-buffer，但不进行测线间插值。

冷加载采用输出驱动的支撑道/样点计划、只读映射或稀疏批量回退。场景加载期间合并低分辨率预览，完成后提交一次全分辨率最终渲染。现场索引纹理预算为 192 MiB，单线最大 1024×1536；持久 `.ptx` 默认上限为 1 GiB。

## 异步与 UI 线程

Workspace、项目、活动线、XY 范围、时间范围、纹理和场景各自使用 generation/token。新请求出现后：

1. 未开始任务从队列跳过。
2. 运行任务在 I/O 或幕布边界检查取消。
3. Reader、映射和临时缓冲正常释放。
4. 只有代次仍匹配的结果可以 `PostMessage` 给当前窗口。

后台线程不创建、销毁或修改 Win32 控件。GDI DC/Bitmap 的交换与释放只在 UI 线程完成。

## 分析与导出

SEG-Y 分析每次选择只读取文件头、一个 240 字节道头和选定样点窗。返回结果是独立 Go 副本，分析窗口不持有工作区 Reader。`internal/segyanalysis` 计算统计和频谱，表格及绘图留在 Win32 层。

二维、真三维和伪三维导出均写入新文件。伪三维导出先冻结几何和范围快照，再以临时文件、同步、重开校验和提交的顺序完成；运行中的相机或选择变化不会修改既定计划。

## 持久状态

品牌迁移暂时保留历史 `%LOCALAPPDATA%\Limage` 和缓存内部标识。当前 schema/格式为：

- `start_center.json` v1
- `volume_view.json` v1
- `crooked_view.json` v1
- `crooked_style.json` v1
- `pseudo_view.json` v2（兼容 v1）
- `.lidx` v1、`.cidx` v1、`.ptx` v1

具体迁移和清理规则见[兼容性说明](COMPATIBILITY.md)。阶段性设计决策见[历史架构索引](history/README.md)。
