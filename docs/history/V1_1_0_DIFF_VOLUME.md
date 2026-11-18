# Limage v1.1.0 — 差值 QC 与三视图联动

## 主界面入口

- **比**：A/B 双数据对比窗口。
- **体**：单个叠后三维体的 Inline / Crossline / Time Slice 三视图联动窗口。

## “比”窗口新增“差”

“差”按钮是一个可切换按钮。打开后，比窗口从 A/B 两幅图扩展为 A/B/差三幅联动视图。

- Inline 模式：显示 A Inline、B Inline、A-B residual Inline。
- Crossline 模式：显示 A Crossline、B Crossline、A-B residual Crossline。
- Time Slice 模式：显示 A Time Slice、B Time Slice、A-B residual Time Slice。
- 十字星、Zoom、矩形/椭圆/线段标注同步到可见的三个面板。
- A/B 继续使用共享显示范围；残差采用独立的**零对称范围** `[-M,+M]`，避免正负残差显示偏置。
- Inline/Crossline 残差只在 A/B 实际几何线号一致时计算，防止错误地相减不同位置的数据。
- Inline/Crossline 残差按共同几何坐标配对 trace 后计算真实振幅 `A-B`，不是两张屏幕图像做像素相减。
- Time Slice 残差在共同 IL/XL 网格上计算，并只在 A/B 均有有效 bin 的位置显示。

## “体”三视图联动

“体”会自动完成：

1. 自动估计 Inline/Crossline trace-header byte；
2. 建立或读取 persistent geometry cache；
3. 建立 Time Slice slab cache；
4. 打开三幅正交视图。

三幅图分别为：

- **Inline**：横轴 Crossline，纵轴 Time；
- **Crossline**：横轴 Inline，纵轴 Time；
- **Time Slice**：横轴 Crossline，纵轴 Inline。

### 联动方式

三幅图共享同一个 `(Inline, Crossline, Sample/Time)` 数据坐标十字星。

- 在 Inline 上单击：更新 Crossline 位置和 Time，Crossline 与 Time Slice 自动更新；
- 在 Crossline 上单击：更新 Inline 位置和 Time，Inline 与 Time Slice 自动更新；
- 在 Time Slice 上单击：更新 Inline 与 Crossline，两个垂直剖面自动跳到该位置；
- 鼠标悬停时只移动三幅图的同步十字星，不触发 SEG-Y 重读。

顶部提供 Inline / Crossline / Time 的前后导航按钮，以及色标、自动识别、还原和关闭。

### 缓存与性能

- Time Slice 继续使用 4/8/16/32-sample adaptive slab cache；
- 当前 slab 未命中 RAM 时显示等待鼠标指针；
- 当前 slab 加载完成后预取相邻 slab；
- Time Slice 上改变 IL/XL 只重绘垂直剖面，不重新读 Time Slice；
- 三视图悬停采用静态底图 + 局部十字星刷新，避免重新引入闪烁。

## 快捷键

在“比”和“体”窗口均支持：

- `Q`：Gain +1%
- `W`：Gain -1%
- `E`：下一个色标
- `R`：上一个色标

## 版本

Limage v1.1.0, native Windows x86-64.
