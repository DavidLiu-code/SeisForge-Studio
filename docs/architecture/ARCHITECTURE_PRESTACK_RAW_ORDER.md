# Prestack Raw-Order Architecture

## 数据流

SEG-Y 卷头和道头建立 `PrestackIndex`。索引中的 `Records` 保持文件物理顺序，`GatherRaw` 使用零基半开区间生成连续物理道号，不建立新的分组表，也不复制振幅数据。

界面以 1-based 道号输入起止范围，边界转换后调用 `PrestackIndex.Gather`，再复用现有 `RenderWithOptions`/`RenderTraceIndices` 读取样点窗口。样点和毫秒范围只影响读取窗口，不改变道序。

## 代次与生命周期

原始道序范围变化时取消旧 Reader 任务，清空旧索引图像并增加渲染代次。结果必须同时匹配数据集、工作区、选择和窗口尺寸代次；过期结果关闭 Reader 且不得交换到窗口。

窗口调整期间由 `WM_ENTERSIZEMOVE` 取消旧尺寸任务但保留最后一帧有效位图，`WM_SIZE` 只更新布局并在新场景矩形内裁剪/缩放该帧，`WM_EXITSIZEMOVE` 经过约 100 ms 防抖后按最终尺寸执行一次渲染。绘制始终限制在当前场景矩形内。

## 键值列表与整体范围

真实道集键值仍由 `AvailableGathers`/`AvailableGathersConfigured` 完整返回；UI 在列表首位插入 `GatherKey{All: true}`，选择后由 `Gather` 汇总全部物理道，不改变真实键值表或缓存格式。原始道序范围优先于该合成键，继续使用独立范围模型。

键值 ComboBox 显式启用 `WS_VSCROLL` 并保留弹出列表高度，避免两行工具栏布局压扁原生列表。选择通过 `WM_PRESTACK_COMBO_COMMIT` 延后应用；下拉展开时快捷键路由不拦截原生列表消息。

## 黑色像素说明

增益模式下，原始异常振幅可能被裁剪到色标黑端。该情况通过渲染统计和状态栏提示，不默认删除、插值或改变数据，以保持地震振幅语义。

## 未改变内容

现有 CMP/Shot/Receiver/Common Offset 道集、普通二维、规则三维、弯线、伪三维、色标、AGC、JSON、`.pidx` 和缓存格式保持兼容。
