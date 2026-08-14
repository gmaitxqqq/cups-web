package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
)

// renderPDFToPreviewPNG 把最终 PDF 渲染成一张"纵向拼接"的预览 PNG（多页拼为一图），
// 用于前端 <img> 直接显示，彻底规避 pdf.js 在受限环境下 worker 无法加载导致的预览失败。
//
// 设计要点：
//   - 用 GS 的 png16m 设备渲染每一页（110 DPI，屏幕预览足够，文件不大）
//   - 多页纵向拼接成一张长图，预览区一次展示全部页
//   - 纯 stdlib image 拼接，不引入额外依赖
func renderPDFToPreviewPNG(ctx context.Context, pdfPath string) (string, func(), error) {
	gsBin, err := exec.LookPath("gs")
	if err != nil {
		return "", nil, fmt.Errorf("preview: ghostscript %w", errBinaryNotInstalled)
	}

	numPages, _ := countPDFPages(pdfPath)
	if numPages < 1 {
		numPages = 1
	}

	tmpDir, err := os.MkdirTemp("", "pdf-preview-")
	if err != nil {
		return "", nil, fmt.Errorf("preview: tmpdir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	pagePat := filepath.Join(tmpDir, "p%d.png")
	args := []string{
		"-dNOPAUSE", "-dBATCH", "-dSAFER", "-dQUIET",
		"-sDEVICE=png16m",
		"-r110",
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4",
		fmt.Sprintf("-sOutputFile=%s", pagePat),
		pdfPath,
	}
	cmd := exec.CommandContext(ctx, gsBin, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("preview: gs render failed: %w - %s", err, firstErrorLine(string(out)))
	}

	// 收集 GS 产出的各页 PNG（命名 p1.png / p2.png ...）
	var pageFiles []string
	for p := 1; p <= numPages; p++ {
		f := filepath.Join(tmpDir, fmt.Sprintf("p%d.png", p))
		if _, err := os.Stat(f); err == nil {
			pageFiles = append(pageFiles, f)
		}
	}
	// 兜底：单页可能只生成 p1.png（上面已覆盖），这里防止命名不一致导致遗漏
	if len(pageFiles) == 0 {
		if matches, _ := filepath.Glob(filepath.Join(tmpDir, "p*.png")); len(matches) > 0 {
			pageFiles = matches
		}
	}
	if len(pageFiles) == 0 {
		cleanup()
		return "", nil, fmt.Errorf("preview: gs produced no PNG for %s", filepath.Base(pdfPath))
	}

	outPNG := filepath.Join(tmpDir, "preview.png")
	if err := stitchPNGVertical(pageFiles, outPNG); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("preview: stitch: %w", err)
	}
	return outPNG, cleanup, nil
}

// stitchPNGVertical 把多张 PNG 纵向拼接为一张（宽度取最大页宽，居中；白底）。
// 相邻页之间留有可见间隔并画一条分隔线，避免用户把"多页预览长图"误认为"全部挤在一页"。
func stitchPNGVertical(files []string, outPath string) error {
	var imgs []image.Image
	maxW := 0
	totalH := 0
	const sepGap = 36 // 页与页之间的间隔像素（@110DPI 约 3mm）
	for _, f := range files {
		rfp, err := os.Open(f)
		if err != nil {
			return err
		}
		img, err := png.Decode(rfp)
		rfp.Close()
		if err != nil {
			return err
		}
		imgs = append(imgs, img)
		b := img.Bounds()
		if b.Dx() > maxW {
			maxW = b.Dx()
		}
		totalH += b.Dy()
	}
	if maxW <= 0 || totalH <= 0 {
		return fmt.Errorf("preview: empty stitched image")
	}
	totalH += sepGap * (len(imgs) - 1) // 页间间隔

	dst := image.NewRGBA(image.Rect(0, 0, maxW, totalH))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	y := 0
	for idx, img := range imgs {
		b := img.Bounds()
		x := (maxW - b.Dx()) / 2 // 水平居中
		draw.Draw(dst, image.Rect(x, y, x+b.Dx(), y+b.Dy()), img, b.Min, draw.Src)
		// 在页与页之间画一条浅灰分隔虚线，明确"这是两页"
		if idx < len(imgs)-1 {
			gy := y + b.Dy() + sepGap/2
			for gx := 0; gx < maxW; gx += 18 {
				for o := 0; o < 9 && gx+o < maxW; o++ {
					dst.Set(gx+o, gy, color.RGBA{190, 190, 190, 255})
				}
			}
		}
		y += b.Dy() + sepGap
	}

	outF, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outF.Close()
	return png.Encode(outF, dst)
}
