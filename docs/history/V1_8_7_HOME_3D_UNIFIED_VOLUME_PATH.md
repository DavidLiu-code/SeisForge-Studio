# Limage v1.8.7 — 首页三维入口统一到“体”按钮路径

## 根因

v1.8.5/v1.8.6 的首页“三维数据体”使用了一套独立的 Home 3-D shell / direct loader；而二维工作区中已经验证正常的“体”按钮使用 `showVolumeWindow()`。两条路径虽然最终都使用 Volume renderer，但窗口生命周期、数据准备入口和前台切换并不完全一致。

Windows 实机连续反馈表明：首页专用路径仍可能把用户留在二维工作区，因此本版不再继续修补第二套入口，而是取消首页对它的调用。

## v1.8.7 行为

首页点击“三维数据体”后：

1. 选择 SEG-Y；
2. Start Center 立即隐藏；
3. SEG-Y 在隐藏的兼容控制器中完成必要初始化；
4. `completeWorkspaceOpen()` 识别 `workspaceMode3D`；
5. 调用与二维工作区“体”按钮完全相同的 `showVolumeWindow()`；
6. 再次强制 `volume3D=true`、`volumeFocusPanel=-1` 并将 Volume 窗口置前。

因此用户可见路径为：

`首页 -> 三维数据体 -> 选择 SEG-Y -> 3D Volume`

二维渲染仅作为隐藏兼容初始化步骤存在，不会成为可见工作区。

## 诊断

每次从首页进入三维时会覆盖写入：

`%LOCALAPPDATA%\Limage\home3d_trace.log`

日志只记录流程状态和文件 basename，不记录 SEG-Y 内容。若 Windows 实机仍出现异常，可据此判断程序实际停在哪一层。

## 保留

- 3D 风格：干净 / CIGVis / 解释 / 标准；
- 体切片 2D Full View 风格同步；
- 默认 CIGVis 交互；
- 首页拖放与最近文件；
- A/B/差三维对比；
- NumPad 1 / End 修复。
