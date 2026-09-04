# Limage v1.2.7 — parameter UI, GUI subsystem, and application icon

## 1. Parameter dialog overlap fixed
The v1.2.6 anti-mosaic controls were created with `WS_VISIBLE` but were not registered as controls belonging to the third tab. They therefore remained visible while the first tab was selected and covered coordinate controls.

v1.2.7 makes every display-mode control a member of the **波形显示** page and reorganizes the page into three non-overlapping groups:
- 放大显示方式
- 类型设定
- 范围设定

The anti-mosaic selector remains:
- 快速像素
- 平滑插值
- 自适应（default/recommended）

## 2. Console/DOS window removed
The Windows executable is now linked with:

`-ldflags="-H=windowsgui"`

PE validation reports:

`Subsystem 00000002 (Windows GUI)`

Therefore launching Limage by double-click should no longer create a console window.

## 3. New Limage application icon
A custom Limage icon was created using an **L + seismic wiggle** motif. The multi-resolution ICO is embedded in the Go executable using `go:embed`. At startup, Limage creates native large/small Windows icon handles from the embedded resource and assigns them to every registered top-level window class.

This covers the visible workspace/taskbar/Alt-Tab window icons without requiring an external `.ico` file at runtime.
