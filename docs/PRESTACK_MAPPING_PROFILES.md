# 叠前 Mapping Profile

Mapping Profile 用于保存已经确认的叠前 `HeaderMapping`，减少重复打开同类
SEG-Y 时的人工确认。Profile 是独立的版本化 JSON 文件，schema 为
`seisforge.prestack.mapping-profile.v1`。

每个 Profile 包含：

- 名称和目标（A 或 B）；
- 经过校验的 HeaderMapping；
- 可选的数据源路径提示；
- 采样道头的覆盖率、冲突数、置信度和警告。

Profile 不保存 Reader、Index、振幅、样点、窗口句柄或缓存。应用 Profile
时仍需使用当前 SEG-Y Reader 重新建立索引；覆盖率和置信度仅用于界面提示，
不能替代实际映射校验。

文件保存采用临时文件、同步和原子发布。未知 schema、重复名称、非法字节
位置、非有限置信度和重复字段摘要都会被拒绝。Profile 的名称不区分大小写，
同名保存必须显式覆盖。
