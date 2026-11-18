# Fimage64 UI reconstruction — v2

This pass reconstructs the visible workflow from the supplied Fimage v1.5.1 executable rather than inventing a replacement UI.

## Main window

Recovered defaults: legacy window Width 441, Height 573, 30-pixel top control bar, 16-pixel bottom information/progress panel, and the compact toolbar order:

Open → Save → separator → Color-map combo → separator → Fixed (1:1) → Colorbar/Mark → Histogram/Data → Parameters → separator → Zoom → Origin → separator → Feedback → Update → About.

The v2 x64 build uses the original embedded TImageList artwork for these tools with their original ImageIndex values.

## Legacy color maps

The original combo contains 20 palettes:

1. 红灰青
2. 白灰黑
3. 黑灰白
4. 蓝灰红
5. 蓝灰褐1
6. 绿灰褐1
7. 蓝灰褐2
8. 绿灰褐2
9. 蓝灰橙
10. 绿灰橙
11. 绿灰褐3
12. 蓝灰褐3
13. 蓝黄褐
14. 绿灰红
15. 红黄蓝
16. 红蓝黄
17. 黄红绿
18. 绿红蓝
19. 灰黄橙
20. 灰绿黄

The three anchor colors for every palette were recovered from the original 60-DWORD color table. The x64 implementation follows the recovered two-half interpolation rule (128 levels per half), rather than substituting generic modern color maps.

## Parameters dialog

Recovered client layout: 372 × 338 with three pages:

- 坐标设置
- 其它设置
- 波形显示

The current v2 build restores the legacy value-range panel under **其它设置**:

- 限定数据范围
- 原始范围 Min / Max
- 限制范围 Min / Max

The limited Min/Max values are functional and directly control amplitude-to-color mapping. Data gain (%) is also functional.

The waveform page and its legacy choices (波形图 / 变面积图 / 波形变面积 / 彩色图) are present for interface fidelity, but wiggle/VA rendering itself remains a later reconstruction target.

## Colorbar dialog

The x64 build restores the vertical legacy colorbar presentation and displays the current mapped maximum, zero, and minimum. Selecting a new palette in the main combo immediately updates both the seismic image and this colorbar window.

## Evidence retained in package

`recovery/original_ui/` contains text reconstructions of the four original binary DFM forms, the original TImageList binary stream, extracted color/mask strips, and the static DFM parser used for this pass.
