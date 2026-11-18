# 叠前道头缓存（测试版）

重复打开 A/B 时会尝试复用已解码道头，减少完整道头扫描。道集、QC、Geometry
关联和 Compare 匹配仍由当前 Mapping 的元数据重新建立；振幅和显示算法不变。

- 默认位置：用户缓存目录下 `Limage/prestack_metadata/v1`，Windows 通常位于
  `%LocalAppData%\Limage\prestack_metadata\v1`。
- 状态栏显示“缓存命中”或“缓存失效，扫描道头”；索引、渲染分别完成。
- 改变 Mapping 或源文件身份会使用新的缓存键。增益、色标、排序、分箱和样窗
  不属于缓存身份，也不会因此重新扫描道头。
- 缓存损坏、不兼容或超过大小上限会重新扫描；无法写缓存时仍能浏览，状态栏
  会显示非致命提示。
- 缓存仅包含道头及来源身份，不包含振幅、样点数组或修改后的 SEG-Y。
  完整来源路径属于本地元数据，请勿将缓存作为脱敏文件分享。

## 强制扫描

在启动程序前设置环境变量，关闭本次进程的缓存读写：

```powershell
$env:SEISFORGE_PRESTACK_CACHE_BYPASS = '1'
& '.\SeisForgeStudio_v1.10.4_pidx_test_x64.exe'
```

恢复默认行为：

```powershell
Remove-Item Env:\SEISFORGE_PRESTACK_CACHE_BYPASS
```

程序已启动时，在另一个终端修改变量不会影响那个进程，请重新启动。
缓存抽样指纹不是全文件完整性校验：同大小、恢复原修改时间且只修改未抽中的
道头或振幅时可能无法检测。怀疑外部编辑影响时请使用强制扫描；加载期间不要
修改输入文件。

## 数据安全与范围

缓存不会写在 SEG-Y 数据目录，不改源 SEG-Y、既有 `.lidx/.cidx/.ptx`、未知
`.pidx` 或 JSON 参数。取消任务、关闭 B 或切换项目会丢弃旧 UI 结果并清理该
任务的临时缓存。缓存不负责关闭 Reader，A/B 后台任务仍独立管理它们。

本次仅为独立测试构建，不替换 RC1、不移动标签、不自动 push 或发布。
