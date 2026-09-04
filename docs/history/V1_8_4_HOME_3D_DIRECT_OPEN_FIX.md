# Limage v1.8.4 — 首页三维数据体直接进入 3D 修复

## 问题

从 Start Center 点击“**三维数据体**”并选择 SEG-Y 后，某些情况下显示出来的仍像二维线/二维切片，而不是完整三维体。

根因有两层：

1. 3D Volume 中通过双击进入的 `2D Full View` 使用 `volumeFocusPanel` 保存当前聚焦切片。旧版本关闭 Volume 时没有清除此状态；下一次重新创建 Volume 时可能继续使用旧 panel index。
2. 首页 3D 打开完成流程会先更新底层二维工作区，再调用 `showVolumeWindow()`，因此视觉流程不是严格的“Home → 3D”。

## v1.8.4 修复

### 1. 新 Volume 强制从完整三维体开始

`showVolumeWindow()` 创建新窗口时固定重置：

- `volume3D = true`
- `volumeFocusPanel = -1`
- `volumeHoverAxis = -1`
- `volumeActiveAxis = Time Slice`

因此之前是否看过 Inline / Crossline / Time Slice 的 2D Full View，都不会影响下一次从首页进入三维。

### 2. 关闭 Volume 时再次清理 Full View 状态

`WM_DESTROY` 同样把 `volumeFocusPanel` 清为 `-1`，使 2D Full View 严格限定在当前 Volume 窗口生命周期内。

### 3. 首页“三维数据体”改为直接 Volume 路径

`completeWorkspaceOpen()` 在确认用户选择的是三维工作区后：

1. 记录最近文件 / 工作区；
2. 重置 2D Full View 状态；
3. 直接创建 3D Volume；
4. 3D 窗口创建成功后立即返回，不先刷新二维工作区。

只有 Volume 顶层窗口创建失败时，才保留已加载数据并退回二维工作区，避免出现空白程序。

## 预期操作

**Start Center → 三维数据体 → 选择 SEG-Y → 直接出现完整 3D Volume。**

之后：

- 双击 3D 中某个切片 → 进入该切片 2D Full View；
- 双击或 Esc → 返回 3D；
- 关闭 3D 后再次从首页进入 → 仍从完整 3D 开始。

## 保留行为

- `干净 / CIGVis / 解释 / 标准` 的体/切片风格同步不变；
- 默认 3D 交互不变；
- 首页拖放、Recent、自动推荐不变；
- A/B/差三维对比不变；
- v1.7.2 NumPad 1 / End 区分修复不变。
