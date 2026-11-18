package pseudo3d

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
)

func TestPlanTextureSizesHonorsBudgetAndCaps(t *testing.T) {
	requests := make([]TextureRequest, 62)
	for i := range requests {
		requests[i] = TextureRequest{ID: string(rune('a' + i)), Traces: 4000, Samples: 5000}
	}
	const budget = int64(8 << 20)
	plans := PlanTextureSizes(requests, budget)
	var bytes int64
	for _, size := range plans {
		if size.Width > MaxTextureWidth || size.Height > MaxTextureHeight || size.Width < 2 || size.Height < 2 {
			t.Fatalf("invalid planned size: %+v", size)
		}
		bytes += int64(size.Width * size.Height)
	}
	if bytes > budget {
		t.Fatalf("planned textures exceed budget: %d > %d", bytes, budget)
	}
}

func TestPlanTimeWindowTextureSizesRetainsDensityInsteadOfRefillingBudget(t *testing.T) {
	requests := make([]TextureRequest, 62)
	fullSamples := make(map[string]int, len(requests))
	for i := range requests {
		id := string(rune('a' + i))
		requests[i] = TextureRequest{ID: id, Traces: 4000, Samples: 1250}
		fullSamples[id] = 5000
	}
	fullRequests := make([]TextureRequest, len(requests))
	for i, request := range requests {
		fullRequests[i] = TextureRequest{ID: request.ID, Traces: request.Traces, Samples: fullSamples[request.ID]}
	}
	full := PlanTextureSizes(fullRequests, TextureBudget)
	quarter := PlanTimeWindowTextureSizes(requests, fullSamples, TextureBudget)
	var fullBytes, quarterBytes int64
	for id, size := range quarter {
		if size.Width != full[id].Width || size.Height > int(math.Ceil(float64(full[id].Height)*.25)) {
			t.Fatalf("time-window plan changed horizontal density or refilled height: full=%+v quarter=%+v", full[id], size)
		}
		fullBytes += int64(full[id].Width * full[id].Height)
		quarterBytes += int64(size.Width * size.Height)
	}
	if quarterBytes*100 > fullBytes*26 {
		t.Fatalf("quarter time window retained too much texture memory: %d of %d", quarterBytes, fullBytes)
	}
}

func TestDecimateTrajectoryPreservesEndpointsAndDistance(t *testing.T) {
	x, y, d := make([]float64, 1000), make([]float64, 1000), make([]float64, 1000)
	for i := range x {
		x[i], y[i], d[i] = float64(i), math.Sin(float64(i)/20), float64(i)*2
	}
	points, u, err := DecimateTrajectory(x, y, d, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 64 || points[0].X != 0 || points[len(points)-1].X != 999 || u[0] != 0 || u[len(u)-1] != 1 {
		t.Fatalf("unexpected decimation: points=%d first=%+v last=%+v u=[%g,%g]", len(points), points[0], points[len(points)-1], u[0], u[len(u)-1])
	}
	for i := 1; i < len(u); i++ {
		if u[i] <= u[i-1] {
			t.Fatalf("texture coordinates are not increasing at %d", i)
		}
	}
}

func TestRenderCurtainProducesTexturePixelsAndTrueBounds(t *testing.T) {
	var palette [256]Color
	for i := range palette {
		palette[i] = Color{R: byte(i), G: byte(255 - i), B: byte(i / 2)}
	}
	texture := &Texture{Width: 4, Height: 4, Indices: []byte{
		0, 64, 128, 255,
		16, 80, 144, 240,
		32, 96, 160, 224,
		48, 112, 176, 208,
	}}
	scene := Scene{Curtains: []Curtain{{ID: "line", Points: []Point{{1000, 2000}, {1400, 2100}, {1600, 2500}}, U: []float64{0, .5, 1}, TimeMaxMS: 2000, Texture: texture}}}
	image, stats, err := Render(scene, DefaultCamera(), 500, 360, palette)
	if err != nil {
		t.Fatal(err)
	}
	if len(image) != 500*360*4 || stats.TexturedCurtains != 1 || stats.Triangles != 4 || stats.Pixels == 0 {
		t.Fatalf("unexpected render stats: %+v len=%d", stats, len(image))
	}
	if stats.Bounds.XMin != 1000 || stats.Bounds.XMax != 1600 || stats.Bounds.YMin != 2000 || stats.Bounds.YMax != 2500 || stats.Bounds.TimeMaxMS != 2000 {
		t.Fatalf("true coordinate bounds were not preserved: %+v", stats.Bounds)
	}
}

func TestRenderWithCancelMatchesRenderAndStopsAtCurtainBoundary(t *testing.T) {
	scene := testPickScene()
	var palette [256]Color
	regular, regularStats, err := Render(scene, DefaultCamera(), 320, 240, palette)
	if err != nil {
		t.Fatal(err)
	}
	checked, checkedStats, err := RenderWithCancel(scene, DefaultCamera(), 320, 240, palette, func() bool { return false })
	if err != nil || !bytes.Equal(regular, checked) || regularStats != checkedStats {
		t.Fatalf("cancel-aware renderer changed output: err=%v regular=%+v checked=%+v", err, regularStats, checkedStats)
	}
	calls := 0
	_, _, err = RenderWithCancel(scene, DefaultCamera(), 320, 240, palette, func() bool {
		calls++
		return calls >= 2
	})
	if !errors.Is(err, ErrRenderCanceled) {
		t.Fatalf("expected cancellation at curtain boundary, got %v", err)
	}
}

func TestCameraNormalizationKeepsIndependentLimits(t *testing.T) {
	camera := (Camera{Elevation: 999, FOV: 999, Zoom: 0, AxisXScale: 99, AxisYScale: 0, VerticalScale: 0}).Normalized()
	if camera.Elevation != 80 || camera.FOV != 120 || camera.Zoom != 1 || camera.AxisXScale != 8 || camera.AxisYScale != 1 || camera.VerticalScale != .55 {
		t.Fatalf("unexpected normalized camera: %+v", camera)
	}
}

func TestXYRangeNormalizesAndRejectsDegenerate(t *testing.T) {
	r := (XYRange{XMin: 10, XMax: -2, YMin: 8, YMax: 1, Valid: true}).Normalized()
	if !r.Valid || r.XMin != -2 || r.XMax != 10 || r.YMin != 1 || r.YMax != 8 {
		t.Fatalf("unexpected normalized range: %+v", r)
	}
	if (XYRange{XMin: 1, XMax: 1, YMin: 0, YMax: 2, Valid: true}).Normalized().Valid {
		t.Fatal("degenerate range was accepted")
	}
}

func TestClipTrajectoryCrossesWithBothEndpointsOutside(t *testing.T) {
	runs, err := ClipTrajectory([]int64{10, 11}, []float64{-5, 15}, []float64{5, 5}, []float64{0, 20}, XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true})
	if err != nil || len(runs) != 1 {
		t.Fatalf("unexpected runs=%+v err=%v", runs, err)
	}
	run := runs[0]
	if len(run.TraceIndices) != 2 || run.PositionStart != 5 || run.PositionEnd != 15 || len(run.Points) != 2 || run.Points[0].X != 0 || run.Points[1].X != 10 {
		t.Fatalf("unexpected clipped run: %+v", run)
	}
}

func TestClipTrajectorySeparatesMultipleEntries(t *testing.T) {
	x := []float64{-2, 5, 12, 5, -2}
	y := []float64{5, 5, 5, 5, 5}
	d := []float64{0, 7, 14, 21, 28}
	runs, err := ClipTrajectory([]int64{0, 1, 2, 3, 4}, x, y, d, XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true})
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected two independent runs, got %d err=%v: %+v", len(runs), err, runs)
	}
	for i, run := range runs {
		if len(run.Points) < 2 || run.Points[0].X < 0 || run.Points[len(run.Points)-1].X > 10 {
			t.Fatalf("run %d escaped the range: %+v", i, run)
		}
	}
}

func TestClipTrajectoryBoundaryAndOutside(t *testing.T) {
	selected := XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true}
	boundary, err := ClipTrajectory([]int64{0, 1}, []float64{0, 10}, []float64{0, 0}, []float64{0, 10}, selected)
	if err != nil || len(boundary) != 1 {
		t.Fatalf("boundary line should be included: runs=%d err=%v", len(boundary), err)
	}
	outside, err := ClipTrajectory([]int64{0, 1}, []float64{-3, -1}, []float64{3, 8}, []float64{0, 5}, selected)
	if err != nil || len(outside) != 0 {
		t.Fatalf("outside line should be rejected: runs=%+v err=%v", outside, err)
	}
}

func testPickScene() Scene {
	texture := &Texture{Width: 2, Height: 2, Indices: []byte{64, 128, 192, 255}}
	return Scene{
		Bounds:   Bounds{XMin: 0, XMax: 10, YMin: 0, YMax: 10, TimeMaxMS: 1000, Valid: true},
		Curtains: []Curtain{{ID: "east-west", Name: "EW", Points: []Point{{0, 5}, {10, 5}}, U: []float64{0, 1}, TimeMaxMS: 1000, Texture: texture}},
	}
}

func TestUnprojectTimePlaneRoundTripsPerspectiveAndOrthographic(t *testing.T) {
	scene := testPickScene()
	for _, fov := range []float64{35, 0} {
		camera := DefaultCamera()
		camera.FOV = fov
		frame := ProjectFrame(scene, camera, 640, 480)
		if !frame.Valid {
			t.Fatalf("frame is invalid for FOV %g", fov)
		}
		projection := ProjectCurtain(scene, camera, 640, 480, 0)
		if !projection.Valid {
			t.Fatalf("curtain projection is invalid for FOV %g", fov)
		}
		for i, source := range scene.Curtains[0].Points {
			for _, sample := range []struct {
				point ScreenPoint
				time  float64
			}{{projection.Top[i], 0}, {projection.Bottom[i], 1}} {
				world, ok := UnprojectTimePlane(scene, camera, 640, 480, sample.point.X, sample.point.Y, sample.time)
				if !ok || math.Hypot(world.X-source.X, world.Y-source.Y) > 1e-7 {
					t.Fatalf("round trip FOV=%g point=%d time=%g: got=%+v ok=%v want=%+v", fov, i, sample.time, world, ok, source)
				}
			}
		}
	}
}

func TestPickReturnsCurtainCoordinates(t *testing.T) {
	scene := testPickScene()
	camera := DefaultCamera()
	p := newProjector(scene, camera, 640, 480).project(Point{5, 5}, .5)
	pick, ok := Pick(scene, camera, 640, 480, p.x-.5, p.y-.5)
	if !ok {
		t.Fatal("center of textured curtain was not picked")
	}
	if pick.CurtainID != "east-west" || math.Abs(pick.U-.5) > .02 || math.Abs(pick.TimeFraction-.5) > .02 || math.Abs(pick.World.Y-5) > 1e-7 {
		t.Fatalf("unexpected pick result: %+v", pick)
	}
	if _, ok := Pick(scene, camera, 640, 480, 1, 1); ok {
		t.Fatal("blank background unexpectedly picked a curtain")
	}
}

func TestPickUsesFrontmostCurtainAtCrossing(t *testing.T) {
	texture := &Texture{Width: 2, Height: 2, Indices: []byte{1, 2, 3, 4}}
	scene := Scene{Bounds: Bounds{XMin: 0, XMax: 10, YMin: 0, YMax: 10, TimeMaxMS: 1, Valid: true}, Curtains: []Curtain{
		{ID: "horizontal", Points: []Point{{0, 5}, {10, 5}}, U: []float64{0, 1}, Texture: texture},
		{ID: "vertical", Points: []Point{{5, 0}, {5, 10}}, U: []float64{0, 1}, Texture: texture},
	}}
	camera := DefaultCamera()
	p := newProjector(scene, camera, 700, 500).project(Point{5, 5}, .5)
	pick, ok := Pick(scene, camera, 700, 500, p.x-.5, p.y-.5)
	if !ok {
		t.Fatal("curtain crossing was not picked")
	}
	if p.depth >= math.Inf(1) {
		t.Fatal("invalid projected crossing depth")
	}
	// Both curtains occupy the same world point at their crossing; the picker
	// must return one of those frontmost equal-depth surfaces, never a blank.
	if pick.CurtainID != "horizontal" && pick.CurtainID != "vertical" {
		t.Fatalf("unexpected crossing pick: %+v", pick)
	}
}

func TestCleanStyleRemovesGuidesWithoutChangingTexturePixels(t *testing.T) {
	texture := &Texture{Width: 2, Height: 2, Indices: []byte{128, 128, 128, 128}}
	var palette [256]Color
	palette[128] = Color{R: 12, G: 34, B: 56}
	base := Scene{Bounds: Bounds{XMin: 0, XMax: 10, YMin: 0, YMax: 10, TimeMaxMS: 1000, Valid: true}, Curtains: []Curtain{{
		ID: "line", Points: []Point{{0, 5}, {10, 5}}, U: []float64{0, 1}, Texture: texture, Highlighted: true,
	}}}
	standard := base
	standard.Style = StyleStandard
	clean := base
	clean.Style = StyleClean
	standardImage, standardStats, err := Render(standard, DefaultCamera(), 420, 320, palette)
	if err != nil {
		t.Fatal(err)
	}
	cleanImage, cleanStats, err := Render(clean, DefaultCamera(), 420, 320, palette)
	if err != nil {
		t.Fatal(err)
	}
	if standardStats.Pixels != cleanStats.Pixels || standardStats.Triangles != cleanStats.Triangles {
		t.Fatalf("style changed seismic raster work: standard=%+v clean=%+v", standardStats, cleanStats)
	}
	texturePixels, sharedTexturePixels := 0, 0
	for i := 0; i < len(cleanImage); i += 4 {
		if cleanImage[i] == 56 && cleanImage[i+1] == 34 && cleanImage[i+2] == 12 {
			texturePixels++
			if standardImage[i] == cleanImage[i] && standardImage[i+1] == cleanImage[i+1] && standardImage[i+2] == cleanImage[i+2] {
				sharedTexturePixels++
			}
		}
	}
	if texturePixels == 0 || sharedTexturePixels == 0 {
		t.Fatalf("style did not retain seismic texture pixels: clean=%d shared=%d", texturePixels, sharedTexturePixels)
	}
	if string(standardImage) == string(cleanImage) {
		t.Fatal("standard and clean guide layers unexpectedly match")
	}
}

func phase12ParallelScene() (Scene, [256]Color) {
	var palette [256]Color
	for index := range palette {
		palette[index] = Color{R: byte(index), G: byte(255 - index), B: byte((index * 37) & 255)}
	}
	texture := &Texture{Width: 16, Height: 16, Indices: make([]byte, 16*16)}
	for index := range texture.Indices {
		texture.Indices[index] = byte((index*29 + index/7) & 255)
	}
	scene := Scene{Bounds: Bounds{XMin: -20, XMax: 120, YMin: -25, YMax: 125, TimeMinMS: 200, TimeMaxMS: 2200, Valid: true}}
	for line := 0; line < 8; line++ {
		points, u := make([]Point, 7), make([]float64, 7)
		for point := range points {
			fraction := float64(point) / float64(len(points)-1)
			points[point] = Point{X: fraction*100 + math.Sin(float64(line))*8, Y: float64(line)*14 + math.Sin(fraction*math.Pi*2+float64(line))*15}
			u[point] = fraction
		}
		scene.Curtains = append(scene.Curtains, Curtain{ID: string(rune('a' + line)), Points: points, U: u,
			TimeStartMS: 200 + float64(line%3)*90, TimeEndMS: 2200 - float64(line%2)*170, Texture: texture,
			Highlighted: line == 2, Hovered: line == 5})
	}
	return scene, palette
}

func TestPhase12ParallelBandsMatchSerialPixels(t *testing.T) {
	base, palette := phase12ParallelScene()
	for _, style := range []DisplayStyle{StyleClean, StyleCIGVis, StyleInterpretation, StyleStandard} {
		for _, fov := range []float64{0, 35} {
			scene := base
			scene.Style = style
			camera := DefaultCamera()
			camera.FOV = fov
			serial, serialStats, err := renderWithWorkers(scene, camera, 720, 513, palette, nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, workers := range []int{2, 4, 8} {
				parallel, parallelStats, err := renderWithWorkers(scene, camera, 720, 513, palette, nil, workers)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(parallel, serial) || parallelStats != serialStats {
					t.Fatalf("parallel raster changed style=%d FOV=%g workers=%d: serial=%+v parallel=%+v", style, fov, workers, serialStats, parallelStats)
				}
			}
		}
	}
}

func TestPhase12ParallelBandsCancelAtCurtainBoundary(t *testing.T) {
	scene, palette := phase12ParallelScene()
	var checks int64
	_, _, err := renderWithWorkers(scene, DefaultCamera(), 900, 700, palette, func() bool {
		return atomic.AddInt64(&checks, 1) > 2
	}, 8)
	if !errors.Is(err, ErrRenderCanceled) {
		t.Fatalf("parallel renderer did not cancel: %v", err)
	}
}

func phase12PerformanceScene() (Scene, [256]Color) {
	var palette [256]Color
	for index := range palette {
		palette[index] = Color{R: byte(index), G: byte(255 - index), B: byte((index * 17) & 255)}
	}
	texture := &Texture{Width: 64, Height: 96, Indices: make([]byte, 64*96)}
	for index := range texture.Indices {
		texture.Indices[index] = byte((index*31 + index/11) & 255)
	}
	scene := Scene{Style: StyleStandard, Bounds: Bounds{XMin: -50, XMax: 1050, YMin: -50, YMax: 1050, TimeMinMS: 0, TimeMaxMS: 3200, Valid: true}}
	for line := 0; line < 62; line++ {
		points, u := make([]Point, 96), make([]float64, 96)
		for point := range points {
			fraction := float64(point) / float64(len(points)-1)
			if line%2 == 0 {
				points[point] = Point{X: fraction*1000 + math.Sin(fraction*12+float64(line))*18, Y: float64(line)*16 + math.Sin(fraction*7+float64(line))*32}
			} else {
				points[point] = Point{X: float64(line)*16 + math.Sin(fraction*9+float64(line))*30, Y: fraction*1000 + math.Cos(fraction*11+float64(line))*16}
			}
			u[point] = fraction
		}
		scene.Curtains = append(scene.Curtains, Curtain{ID: fmt.Sprintf("line-%02d", line), Points: points, U: u,
			TimeStartMS: float64(line%4) * 20, TimeEndMS: 3200 - float64(line%3)*25, Texture: texture,
			Highlighted: line == 31})
	}
	return scene, palette
}

func BenchmarkPhase12ParallelRaster(b *testing.B) {
	scene, palette := phase12PerformanceScene()
	for _, workers := range []int{1, 8} {
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			for iteration := 0; iteration < b.N; iteration++ {
				if _, _, err := renderWithWorkers(scene, DefaultCamera(), 1200, 800, palette, nil, workers); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestProjectPointMatchesFrameProjector(t *testing.T) {
	scene := testPickScene()
	for _, fov := range []float64{0, 35} {
		camera := DefaultCamera()
		camera.FOV = fov
		frame := ProjectFrame(scene, camera, 640, 480)
		if !frame.Valid {
			t.Fatal("frame projection is invalid")
		}
		point, ok := ProjectPoint(scene, camera, 640, 480, Point{scene.Bounds.XMin, scene.Bounds.YMin}, 0)
		if !ok {
			t.Fatalf("point projection failed at FOV %.0f", fov)
		}
		if math.Abs(point.X-frame.Base[0].X) > 1e-9 || math.Abs(point.Y-frame.Base[0].Y) > 1e-9 || math.Abs(point.Depth-frame.Base[0].Depth) > 1e-9 {
			t.Fatalf("ProjectPoint diverged from renderer frame at FOV %.0f: point=%+v frame=%+v", fov, point, frame.Base[0])
		}
	}
}

func TestTimeRangeNormalizationClampingAndSampleWindow(t *testing.T) {
	rangeMS := (TimeRange{StartMS: 999, EndMS: 501, Valid: true}).Normalized()
	if !rangeMS.Valid || rangeMS.StartMS != 501 || rangeMS.EndMS != 999 {
		t.Fatalf("unexpected normalized range: %+v", rangeMS)
	}
	start, end, actual, ok := rangeMS.SampleWindow(2000, 1000)
	if !ok || start != 251 || end != 500 || actual.StartMS != 502 || actual.EndMS != 1000 {
		t.Fatalf("unexpected sample window: %d-%d %+v ok=%v", start, end, actual, ok)
	}
	if _, _, _, ok := (TimeRange{StartMS: 2500, EndMS: 3000, Valid: true}).SampleWindow(2000, 1000); ok {
		t.Fatal("out-of-record time range was accepted")
	}
	start, end, actual, ok = (TimeRange{}).SampleWindow(4000, 11)
	if !ok || start != 0 || end != 10 || actual.StartMS != 0 || actual.EndMS != 40 {
		t.Fatalf("full sample window changed: %d-%d %+v ok=%v", start, end, actual, ok)
	}
}

func TestPartialTimeCurtainProjectionAndPickUseAbsoluteMilliseconds(t *testing.T) {
	texture := &Texture{Width: 8, Height: 8, Indices: make([]byte, 64)}
	scene := Scene{Bounds: Bounds{XMin: 0, XMax: 10, YMin: 0, YMax: 10, TimeMinMS: 0, TimeMaxMS: 2000, Valid: true},
		Curtains: []Curtain{{ID: "partial", Points: []Point{{0, 5}, {10, 5}}, U: []float64{0, 1},
			TimeStartMS: 500, TimeEndMS: 1500, Texture: texture}}}
	for _, fov := range []float64{0, 35} {
		camera := DefaultCamera()
		camera.FOV = fov
		projection := ProjectCurtain(scene, camera, 640, 480, 0)
		if !projection.Valid {
			t.Fatalf("partial curtain projection failed at FOV %.0f", fov)
		}
		expectedTop, _ := ProjectPoint(scene, camera, 640, 480, scene.Curtains[0].Points[0], .25)
		expectedBottom, _ := ProjectPoint(scene, camera, 640, 480, scene.Curtains[0].Points[0], .75)
		if math.Hypot(projection.Top[0].X-expectedTop.X, projection.Top[0].Y-expectedTop.Y) > 1e-7 ||
			math.Hypot(projection.Bottom[0].X-expectedBottom.X, projection.Bottom[0].Y-expectedBottom.Y) > 1e-7 {
			t.Fatalf("partial curtain was stretched at FOV %.0f", fov)
		}
		projector := newProjector(scene, camera, 640, 480)
		middle := projector.project(Point{5, 5}, .5)
		pick, ok := Pick(scene, camera, 640, 480, middle.x-.5, middle.y-.5)
		if !ok || math.Abs(pick.TimeMS-1000) > 5 || math.Abs(pick.TimeFraction-.5) > .02 {
			t.Fatalf("absolute-time pick mismatch at FOV %.0f: %+v ok=%v", fov, pick, ok)
		}
	}
}

func TestProjectTimeRangeAndUnprojectFrameTime(t *testing.T) {
	scene := Scene{Bounds: Bounds{XMin: 0, XMax: 10, YMin: 0, YMax: 10, TimeMinMS: 0, TimeMaxMS: 2000, Valid: true}}
	camera := DefaultCamera()
	frame := ProjectTimeRange(scene, camera, 640, 480, TimeRange{StartMS: 400, EndMS: 1200, Valid: true})
	if !frame.Valid || frame.Bounds.TimeMinMS != 400 || frame.Bounds.TimeMaxMS != 1200 {
		t.Fatalf("unexpected projected time range: %+v", frame)
	}
	full := ProjectFrame(scene, camera, 640, 480)
	start, okStart := UnprojectFrameTime(scene, camera, 640, 480, full.Base[0].X, full.Base[0].Y)
	end, okEnd := UnprojectFrameTime(scene, camera, 640, 480, full.Top[0].X, full.Top[0].Y)
	if !okStart || !okEnd || math.Abs(start) > .1 || math.Abs(end-2000) > .1 {
		t.Fatalf("frame time inverse mismatch: start=%g end=%g", start, end)
	}
}
