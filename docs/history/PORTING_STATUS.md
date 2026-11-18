# Porting status — prototype 2

## Recovered from the legacy executable

- ASPack-compressed Borland C++Builder application code/data/resources.
- Main `TImgForm`, input `TTypeForm`, parameter `TParaForm`, colorbar `TMarkForm`, and supporting forms.
- Original toolbar geometry and the embedded 24-image `TImageList` used by Fimage.
- Twenty legacy color-map names, all 60 stored anchor colors, and the palette interpolation behavior.
- Exact value-range controls and the three-tab parameter-dialog structure.

## Implemented in x64 prototype 2

- Native Windows amd64 GUI executable.
- 64-bit SEG-Y file sizes/offsets and streaming trace reads.
- Original-style compact icon toolbar.
- All 20 legacy palettes with functional switching.
- Colorbar/mark display linked to the current palette/range.
- Functional manual Min/Max data-range limiting.
- Data gain (%) and reset/origin behavior.
- Color BMP export.
- Restored parameter/waveform controls for workflow fidelity.

## Next targets

1. Activate legacy wiggle / variable-area / wiggle+VA / color display modes.
2. Reconstruct `TTypeForm` behavior for Sun/PC header/no-header and generic binary files.
3. Restore interactive zoom/selection and fixed-size/axis controls.
4. Restore custom color-anchor editing and colorbar export.
