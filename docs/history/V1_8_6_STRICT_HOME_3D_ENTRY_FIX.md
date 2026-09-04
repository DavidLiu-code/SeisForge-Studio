# Limage v1.8.6 — 首页“三维数据体”严格 3D 入口修复

## 问题

v1.8.4 / v1.8.5 虽然已经尝试绕开二维 `loadFile()`，但“选择文件”仍发生在 Start Center/二维工作区仍处于前台的阶段，且 3D 显示状态只在部分入口点重置。Windows 实机仍可能给人“点击三维后进入二维剖面”的结果。

## v1.8.6 的结构性修复

首页点击 **三维数据体** 后，执行顺序改为：

1. 立即创建真正的 `Limage64Volume` 顶层窗口；
2. 将 Start Center / 二维 compare 窗口隐藏；
3. 将 Volume 强制设为 `3D View`，`volumeFocusPanel=-1`；
4. 文件选择框以 **3D Volume 窗口**为 owner 打开；
5. 用户选择 SEG-Y 后，直接调用 3D geometry loader；
6. A 数据异步准备完成时，再执行一次一次性的 3D 状态锁定；
7. 全程不调用二维 `loadFile()` / `rerender()` / `workspaceSyncAFromMain()`。

因此，点击“三维数据体”以后，即使尚未选择数据，应用的可见工作区也已经是 3D Volume，而不是二维剖面。

## 额外修复

- 空的 3D Volume shell 现在支持直接拖入 SEG-Y 作为 A；加载 A 后再拖文件仍保持原有“作为 B 对比”的行为。
- 新建 3D A 数据时始终从真正的 3D View 开始；之后用户主动切换“平铺”仍然有效。
- 重新执行“识别”不会无条件把用户已经选择的平铺模式切回 3D，3D 强制仅用于新打开 A 的一次性保护。
- 3D 窗口标题明确为“`三维数据体`”，状态栏加载完成后明确显示 `3D View`。

## 回归验证

- `go test ./... -count=1`：PASS
- Windows amd64 GUI build：PASS
- PE 类型：PE32+ Windows GUI x86-64
- 首页 3D 路由静态断言：6/6 PASS
- 测试叠后三维 SEG-Y 几何：IL byte 189，XL byte 193，40×50 网格，2000 valid traces，0 duplicates，Poststack=true

## 手工测试路径

启动 Limage → 点击“三维数据体”。此时在文件选择之前，后台就已经创建真正的三维窗口。选择三维 SEG-Y 后，应直接在该窗口中显示 3D 正交切片体；不会出现二维剖面工作区作为中间或最终页面。
