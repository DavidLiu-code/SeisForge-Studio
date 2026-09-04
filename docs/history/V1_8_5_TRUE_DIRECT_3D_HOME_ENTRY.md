# Limage v1.8.5 — 首页三维入口真正直达 Volume

## 问题

v1.8.4 虽然在二维加载完成后立即调用了 3D Volume，但首页“三维数据体”仍复用了旧的二维 `loadFile()` 管线：

`Home 3D -> loadFile -> 2D render -> workspaceSyncAFromMain -> showVolumeWindow`

这意味着三维入口本质上仍先建立二维剖面状态，在部分 Windows 前台/窗口切换情况下用户仍可能停留在二维工作区。

## v1.8.5 修复

新增 `showVolumeWindowForPath(path, directHomeOpen)`，允许 3D Volume 直接从 SEG-Y 路径启动，不依赖二维 `sf`、二维 render 或 compare workspace 数据状态。

首页“三维数据体”的新路径为：

`Home 3D -> 选择 SEG-Y -> validate -> showVolumeWindowForPath -> startVolumePrepareForSide`

明确不再调用：

- `loadFile()`
- `loadSelectedFile()`
- `rerender()`
- `workspaceSyncAFromMain()`

因此从首页进入三维时不会先生成二维剖面，也不会把二维工作区作为中间页面。

## 额外行为

- 首页 3D 入口默认使用完整 SEG-Y 时间范围，不继承之前二维裁剪范围。
- 关闭 3D Volume 后返回 Start Center；不会因为 3D 入口偷偷加载二维 A 而落回二维剖面。
- 最近打开列表中标记为“三维数据体”的项目同样走直接 3D 路径。
- 首页无指定工作区的拖放，如果自动识别为规则 IL/XL 三维，也会走直接 3D 路径。
- 2D、弯线、A/B/差、切片风格同步、NumPad 修复保持不变。
