package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	bundleSchemaLegacy     = 1
	bundleSchema           = 2
	maxManifestBytes       = 1 << 20
	maxBundleAssetBytes    = 16 << 20
	maxBundleAssetPixels   = 32 << 20
	maxBundleDimension     = 8192
	maxBundleSurfaces      = 16
	maxBundleVariants      = 32
	maxBundleHitRegions    = 256
	maxBundleTextSlots     = 64
	maxTextFontFamilies    = 8
	maxBundleProgressSlots = 16
	maxBundleCellSlots     = 64
)

type bundleManifest struct {
	Schema         int             `json:"schema"`
	Project        string          `json:"project"`
	DefaultSurface string          `json:"defaultSurface"`
	Version        pageVersion     `json:"version"`
	Surfaces       []bundleSurface `json:"surfaces"`
}

type bundleSurface struct {
	ID            string          `json:"id"`
	LogicalWidth  int             `json:"logicalWidth"`
	LogicalHeight int             `json:"logicalHeight"`
	HitRegions    []hitRegion     `json:"hitRegions"`
	Dynamic       dynamicSlots    `json:"dynamic,omitempty"`
	Variants      []bundleVariant `json:"variants"`
}

type dynamicSlots struct {
	Text     []textSlot     `json:"text,omitempty"`
	Progress []progressSlot `json:"progress,omitempty"`
	Cells    []cellSlot     `json:"cells,omitempty"`
}

type slotRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type textSlot struct {
	Bind         string     `json:"bind"`
	Rect         slotRect   `json:"rect"`
	FontFamily   string     `json:"fontFamily"`
	FontFamilies []string   `json:"fontFamilies,omitempty"`
	FontSize     float64    `json:"fontSize"`
	FontWeight   int        `json:"fontWeight"`
	Color        string     `json:"color"`
	Align        string     `json:"align"`
	ToneColors   toneColors `json:"toneColors,omitempty"`
}

type progressSlot struct {
	Bind       string     `json:"bind"`
	Rect       slotRect   `json:"rect"`
	Color      string     `json:"color"`
	ToneColors toneColors `json:"toneColors,omitempty"`
}

type cellSlot struct {
	Bind            string     `json:"bind"`
	Rects           []slotRect `json:"rects"`
	Colors          []string   `json:"colors"`
	BackgroundColor string     `json:"backgroundColor,omitempty"`
}

type toneColors struct {
	Good    string `json:"good,omitempty"`
	Warn    string `json:"warn,omitempty"`
	Danger  string `json:"danger,omitempty"`
	Offline string `json:"offline,omitempty"`
}

type hitRegion struct {
	Action string  `json:"action"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type bundleVariant struct {
	Scale      float64 `json:"scale"`
	File       string  `json:"file"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	SourceHash string  `json:"sourceHash,omitempty"`
}

type renderedSurface struct {
	Manifest bundleManifest
	Surface  bundleSurface
	Variant  bundleVariant
	Image    image.Image
}

func loadRenderedSurface(bundleDir string, surfaceID string, dpi uint32) (*renderedSurface, error) {
	manifest, err := readManifest(bundleDir)
	if err != nil {
		return nil, err
	}
	return loadRenderedSurfaceFromManifest(bundleDir, manifest, surfaceID, dpi)
}

func loadRenderedSurfaceAtScale(
	bundleDir string,
	surfaceID string,
	targetScale float64,
) (*renderedSurface, error) {
	manifest, err := readManifest(bundleDir)
	if err != nil {
		return nil, err
	}
	return loadRenderedSurfaceFromManifestAtScale(
		bundleDir,
		manifest,
		surfaceID,
		targetScale,
	)
}

func loadRenderedSurfaceFromManifest(
	bundleDir string,
	manifest bundleManifest,
	surfaceID string,
	dpi uint32,
) (*renderedSurface, error) {
	return loadRenderedSurfaceFromManifestAtScale(
		bundleDir,
		manifest,
		surfaceID,
		float64(dpi)/96,
	)
}

func loadRenderedSurfaceFromManifestAtScale(
	bundleDir string,
	manifest bundleManifest,
	surfaceID string,
	targetScale float64,
) (*renderedSurface, error) {
	if !finite(targetScale) || targetScale <= 0 || targetScale > 8 {
		return nil, fmt.Errorf("invalid UI target scale %.4g", targetScale)
	}
	surface, err := manifest.findSurface(surfaceID)
	if err != nil {
		return nil, err
	}

	variant, err := surface.nearestVariant(targetScale)
	if err != nil {
		return nil, err
	}

	assetPath, err := safeBundlePath(bundleDir, variant.File)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(assetPath)
	if err != nil {
		return nil, fmt.Errorf("opening UI asset %q: %w", variant.File, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking UI asset %q: %w", variant.File, err)
	}
	if info.Size() <= 0 || info.Size() > maxBundleAssetBytes {
		return nil, fmt.Errorf("UI asset %q has invalid size %d", variant.File, info.Size())
	}
	config, _, err := image.DecodeConfig(io.LimitReader(file, maxBundleAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("checking UI asset %q: %w", variant.File, err)
	}
	if err := validateImageSize(config.Width, config.Height); err != nil {
		return nil, fmt.Errorf("checking UI asset %q: %w", variant.File, err)
	}
	if config.Width != variant.Width || config.Height != variant.Height {
		return nil, fmt.Errorf(
			"UI asset %q is %dx%d, manifest declares %dx%d",
			variant.File,
			config.Width,
			config.Height,
			variant.Width,
			variant.Height,
		)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding UI asset %q: %w", variant.File, err)
	}

	decoded, _, err := image.Decode(io.LimitReader(file, maxBundleAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decoding UI asset %q: %w", variant.File, err)
	}
	if decoded.Bounds().Dx() != variant.Width || decoded.Bounds().Dy() != variant.Height {
		return nil, fmt.Errorf(
			"UI asset %q is %dx%d, manifest declares %dx%d",
			variant.File,
			decoded.Bounds().Dx(),
			decoded.Bounds().Dy(),
			variant.Width,
			variant.Height,
		)
	}
	targetWidth := int(math.Round(float64(surface.LogicalWidth) * targetScale))
	targetHeight := int(math.Round(float64(surface.LogicalHeight) * targetScale))
	if err := validateImageSize(targetWidth, targetHeight); err != nil {
		return nil, fmt.Errorf("scaling UI surface %q: %w", surface.ID, err)
	}
	if targetWidth != variant.Width || targetHeight != variant.Height {
		decoded = scaleImage(decoded, targetWidth, targetHeight)
	}
	variant.Scale = targetScale
	variant.Width = targetWidth
	variant.Height = targetHeight

	return &renderedSurface{
		Manifest: manifest,
		Surface:  surface,
		Variant:  variant,
		Image:    decoded,
	}, nil
}

func readManifest(bundleDir string) (bundleManifest, error) {
	path := filepath.Join(bundleDir, "manifest.json")
	file, err := os.Open(path)
	if err != nil {
		return bundleManifest{}, fmt.Errorf("reading UI bundle manifest: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return bundleManifest{}, fmt.Errorf("reading UI bundle manifest: %w", err)
	}
	if len(contents) > maxManifestBytes {
		return bundleManifest{}, fmt.Errorf("UI bundle manifest exceeds %d bytes", maxManifestBytes)
	}

	var manifest bundleManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return bundleManifest{}, fmt.Errorf("parsing UI bundle manifest: %w", err)
	}
	if manifest.Schema != bundleSchemaLegacy && manifest.Schema != bundleSchema {
		return bundleManifest{}, fmt.Errorf("unsupported UI bundle schema %d", manifest.Schema)
	}
	if manifest.Project != projectID {
		return bundleManifest{}, fmt.Errorf("unexpected UI bundle project %q", manifest.Project)
	}
	if len(manifest.Surfaces) == 0 {
		return bundleManifest{}, errors.New("UI bundle contains no surfaces")
	}
	if len(manifest.Surfaces) > maxBundleSurfaces {
		return bundleManifest{}, fmt.Errorf("UI bundle has too many surfaces: %d", len(manifest.Surfaces))
	}
	foundDefault := false
	surfaceIDs := make(map[string]struct{}, len(manifest.Surfaces))
	for _, surface := range manifest.Surfaces {
		if _, duplicate := surfaceIDs[surface.ID]; duplicate {
			return bundleManifest{}, fmt.Errorf("UI bundle contains duplicate surface %q", surface.ID)
		}
		surfaceIDs[surface.ID] = struct{}{}
		if surface.ID == manifest.DefaultSurface {
			foundDefault = true
		}
		if surface.ID == "" || surface.LogicalWidth <= 0 || surface.LogicalHeight <= 0 {
			return bundleManifest{}, errors.New("UI bundle contains an invalid surface")
		}
		if len(surface.Variants) == 0 || len(surface.Variants) > maxBundleVariants {
			return bundleManifest{}, fmt.Errorf("UI surface %q has an invalid variant count", surface.ID)
		}
		if len(surface.HitRegions) > maxBundleHitRegions {
			return bundleManifest{}, fmt.Errorf("UI surface %q has too many hit regions", surface.ID)
		}
		if err := validateDynamicSlots(surface); err != nil {
			return bundleManifest{}, err
		}
		for _, variant := range surface.Variants {
			if variant.Scale <= 0 || variant.File == "" {
				return bundleManifest{}, fmt.Errorf("UI surface %q has an invalid variant", surface.ID)
			}
			if variant.SourceHash != "" && !isLowerHex(variant.SourceHash, 64) {
				return bundleManifest{}, fmt.Errorf(
					"UI surface %q has an invalid source hash",
					surface.ID,
				)
			}
			if err := validateImageSize(variant.Width, variant.Height); err != nil {
				return bundleManifest{}, fmt.Errorf("UI surface %q: %w", surface.ID, err)
			}
		}
	}
	if !foundDefault {
		return bundleManifest{}, fmt.Errorf("default UI surface %q was not found", manifest.DefaultSurface)
	}
	return manifest, nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHexLetter := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHexLetter {
			return false
		}
	}
	return true
}

func validateDynamicSlots(surface bundleSurface) error {
	if len(surface.Dynamic.Text) > maxBundleTextSlots ||
		len(surface.Dynamic.Progress) > maxBundleProgressSlots ||
		len(surface.Dynamic.Cells) > 1 {
		return fmt.Errorf("UI surface %q has too many dynamic slots", surface.ID)
	}
	textBindings := make(map[string]struct{}, len(surface.Dynamic.Text))
	for _, slot := range surface.Dynamic.Text {
		if !validTextBinding(slot.Bind) {
			return fmt.Errorf("UI surface %q has invalid text binding %q", surface.ID, slot.Bind)
		}
		if _, duplicate := textBindings[slot.Bind]; duplicate {
			return fmt.Errorf("UI surface %q repeats text binding %q", surface.ID, slot.Bind)
		}
		textBindings[slot.Bind] = struct{}{}
		if !rectWithinSurface(slot.Rect, surface) {
			return fmt.Errorf("UI surface %q has an out-of-bounds text slot", surface.ID)
		}
		validFont := validFontFamily(slot.FontFamily) &&
			len(slot.FontFamilies) <= maxTextFontFamilies
		for _, family := range slot.FontFamilies {
			validFont = validFont && validFontFamily(family)
		}
		if len(slot.FontFamilies) > 0 {
			validFont = validFont && slot.FontFamilies[0] == slot.FontFamily
		}
		validSize := finite(slot.FontSize) && slot.FontSize >= 4 && slot.FontSize <= 96
		validWeight := slot.FontWeight >= 100 && slot.FontWeight <= 1000
		validAlign := slot.Align == "left" || slot.Align == "center" || slot.Align == "right"
		if !validFont || !validSize || !validWeight || !validAlign ||
			!validSlotColor(slot.Color) || !validToneColors(slot.ToneColors) {
			return fmt.Errorf("UI surface %q has an invalid text slot", surface.ID)
		}
	}

	progressBindings := make(map[string]struct{}, len(surface.Dynamic.Progress))
	for _, slot := range surface.Dynamic.Progress {
		if slot.Bind != "quota.progress" || !rectWithinSurface(slot.Rect, surface) ||
			!validSlotColor(slot.Color) || !validToneColors(slot.ToneColors) {
			return fmt.Errorf("UI surface %q has an invalid progress slot", surface.ID)
		}
		if _, duplicate := progressBindings[slot.Bind]; duplicate {
			return fmt.Errorf("UI surface %q repeats progress binding %q", surface.ID, slot.Bind)
		}
		progressBindings[slot.Bind] = struct{}{}
	}

	for _, slot := range surface.Dynamic.Cells {
		validColorCount := len(slot.Colors) == 5 || len(slot.Colors) == 6
		if slot.Bind != "statistics.monthCells" || len(slot.Rects) == 0 ||
			len(slot.Rects) > maxBundleCellSlots || !validColorCount {
			return fmt.Errorf("UI surface %q has an invalid cell slot", surface.ID)
		}
		if slot.BackgroundColor != "" && !validSlotColor(slot.BackgroundColor) {
			return fmt.Errorf("UI surface %q has an invalid cell background color", surface.ID)
		}
		for _, rect := range slot.Rects {
			if !rectWithinSurface(rect, surface) {
				return fmt.Errorf("UI surface %q has an out-of-bounds cell", surface.ID)
			}
		}
		for _, value := range slot.Colors {
			if !validSlotColor(value) {
				return fmt.Errorf("UI surface %q has an invalid cell color", surface.ID)
			}
		}
	}
	return nil
}

func validFontFamily(value string) bool {
	return strings.TrimSpace(value) != "" &&
		len(value) <= 128 &&
		!strings.ContainsRune(value, '\x00')
}

func textSlotFontFamilies(slot textSlot) []string {
	if len(slot.FontFamilies) > 0 {
		return slot.FontFamilies
	}
	return []string{slot.FontFamily}
}

func validTextBinding(value string) bool {
	switch value {
	case "runtime.model",
		"runtime.effort",
		"runtime.speed",
		"quota.remaining",
		"quota.reset",
		"statistics.total",
		"statistics.peak",
		"statistics.duration",
		"statistics.currentStreak",
		"statistics.longestStreak",
		"statistics.month",
		"statistics.previousMonth",
		"statistics.nextMonth",
		"statistics.viewMonth",
		"statistics.viewWeek",
		"statistics.viewCumulative",
		"statistics.viewDetail",
		"statistics.detailInput",
		"statistics.detailOutput",
		"statistics.detailTotal",
		"statistics.detailCached",
		"statistics.detailReasoning",
		"statistics.detailCacheHit",
		"statistics.detailCost",
		"statistics.labelInput",
		"statistics.labelOutput",
		"statistics.labelTotal",
		"statistics.labelCached",
		"statistics.labelReasoning",
		"statistics.labelCacheHit",
		"statistics.labelCost",
		"toast.title",
		"toast.message":
		return true
	default:
		return false
	}
}

func rectWithinSurface(rect slotRect, surface bundleSurface) bool {
	values := []float64{rect.X, rect.Y, rect.Width, rect.Height}
	for _, value := range values {
		if !finite(value) {
			return false
		}
	}
	return rect.X >= 0 && rect.Y >= 0 && rect.Width > 0 && rect.Height > 0 &&
		rect.X+rect.Width <= float64(surface.LogicalWidth)+0.01 &&
		rect.Y+rect.Height <= float64(surface.LogicalHeight)+0.01
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validToneColors(colors toneColors) bool {
	for _, value := range []string{colors.Good, colors.Warn, colors.Danger, colors.Offline} {
		if value != "" && !validSlotColor(value) {
			return false
		}
	}
	return true
}

func validSlotColor(value string) bool {
	if len(value) != 7 && len(value) != 9 {
		return false
	}
	if value[0] != '#' {
		return false
	}
	for _, character := range value[1:] {
		isNumber := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'f'
		isUpper := character >= 'A' && character <= 'F'
		if !isNumber && !isLower && !isUpper {
			return false
		}
	}
	return true
}

func (manifest bundleManifest) findSurface(surfaceID string) (bundleSurface, error) {
	if surfaceID == "" {
		surfaceID = manifest.DefaultSurface
	}
	for _, surface := range manifest.Surfaces {
		if surface.ID == surfaceID {
			return surface, nil
		}
	}
	return bundleSurface{}, fmt.Errorf("UI surface %q was not found", surfaceID)
}

func (surface bundleSurface) nearestVariant(targetScale float64) (bundleVariant, error) {
	if len(surface.Variants) == 0 {
		return bundleVariant{}, fmt.Errorf("UI surface %q has no scale variants", surface.ID)
	}

	nearest := surface.Variants[0]
	distance := math.Abs(nearest.Scale - targetScale)
	for _, candidate := range surface.Variants[1:] {
		candidateDistance := math.Abs(candidate.Scale - targetScale)
		if candidateDistance <= distance {
			nearest = candidate
			distance = candidateDistance
		}
	}
	return nearest, nil
}

func validateImageSize(width int, height int) error {
	if width <= 0 || height <= 0 || width > maxBundleDimension || height > maxBundleDimension {
		return fmt.Errorf("invalid image size %dx%d", width, height)
	}
	if width*height > maxBundleAssetPixels {
		return fmt.Errorf("image size %dx%d exceeds the pixel budget", width, height)
	}
	return nil
}

func scaleImage(source image.Image, width int, height int) image.Image {
	bounds := source.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return source
	}

	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	scaleX := float64(bounds.Dx()) / float64(width)
	scaleY := float64(bounds.Dy()) / float64(height)
	for y := range height {
		sourceY := (float64(y)+0.5)*scaleY - 0.5
		y0, y1, weightY := interpolationCoordinates(sourceY, bounds.Min.Y, bounds.Max.Y)
		for x := range width {
			sourceX := (float64(x)+0.5)*scaleX - 0.5
			x0, x1, weightX := interpolationCoordinates(sourceX, bounds.Min.X, bounds.Max.X)
			pixel := bilinearColor(
				source.At(x0, y0),
				source.At(x1, y0),
				source.At(x0, y1),
				source.At(x1, y1),
				weightX,
				weightY,
			)
			destination.SetRGBA(x, y, pixel)
		}
	}
	return destination
}

func interpolationCoordinates(value float64, minimum int, maximum int) (int, int, float64) {
	if value <= float64(minimum) {
		return minimum, minimum, 0
	}
	if value >= float64(maximum-1) {
		return maximum - 1, maximum - 1, 0
	}
	lower := int(math.Floor(value))
	weight := value - float64(lower)
	upper := lower + 1
	return lower, upper, weight
}

func bilinearColor(
	topLeft color.Color,
	topRight color.Color,
	bottomLeft color.Color,
	bottomRight color.Color,
	weightX float64,
	weightY float64,
) color.RGBA {
	tlR, tlG, tlB, tlA := topLeft.RGBA()
	trR, trG, trB, trA := topRight.RGBA()
	blR, blG, blB, blA := bottomLeft.RGBA()
	brR, brG, brB, brA := bottomRight.RGBA()
	interpolateChannel := func(tl uint32, tr uint32, bl uint32, br uint32) uint8 {
		top := float64(tl)*(1-weightX) + float64(tr)*weightX
		bottom := float64(bl)*(1-weightX) + float64(br)*weightX
		return uint8(math.Round((top*(1-weightY) + bottom*weightY) / 257))
	}
	return color.RGBA{
		R: interpolateChannel(tlR, trR, blR, brR),
		G: interpolateChannel(tlG, trG, blG, brG),
		B: interpolateChannel(tlB, trB, blB, brB),
		A: interpolateChannel(tlA, trA, blA, brA),
	}
}

func safeBundlePath(bundleDir string, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	isParent := clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
	if filepath.IsAbs(clean) || isParent {
		return "", fmt.Errorf("invalid UI asset path %q", relative)
	}
	return filepath.Join(bundleDir, clean), nil
}
