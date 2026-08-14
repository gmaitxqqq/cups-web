package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log"
	"math"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/phpdave11/gofpdf"
	"github.com/phpdave11/gofpdf/contrib/gofpdi"
	"golang.org/x/image/draw"
)

const (
	defaultComposeMarginMM = 5.0
	dashLineLenMM          = 3.0
	dashLineGapMM          = 2.0
	dashLineGrayLevel      = 180
)

type composePage struct {
	pdfPath string
	pageNo  int
	imgPath string
	imgCfg  image.Config
}

// composeInvoice 把多张发票/文档 N 合 1 拼到同一张 A4 上。
// layout 决定输出纸张方向 (P/L) 与网格 (行 × 列)：
//   "2up"            -> 纵向2合1：A4 纵向，2 行 1 列（上下两格）
//   "2up-landscape"  -> 横向2合1：A4 横向，1 行 2 列（左右两格）
//   "4up"            -> 纵向4合1：A4 纵向，2 行 2 列
//   "4up-landscape"  -> 横向4合1：A4 横向，2 行 2 列
// 任意未知值回退到纵向2合1，保证向后兼容。
func composeInvoice(ctx context.Context, fileHeaders []*multipart.FileHeader, layout string, marginMM float64) (string, func(), error) {
	if len(fileHeaders) == 0 {
		return "", nil, errors.New("no files provided")
	}

	var orient string
	var gridRows, gridCols int
	switch layout {
	case "2up-landscape":
		orient, gridRows, gridCols = "L", 1, 2
	case "4up":
		orient, gridRows, gridCols = "P", 2, 2
	case "4up-landscape":
		orient, gridRows, gridCols = "L", 2, 2
	default: // "2up" 及未知值：纵向2合1
		orient, gridRows, gridCols = "P", 2, 1
	}

	tmpDir, err := os.MkdirTemp("", "compose-invoice-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	pages, err := collectPages(ctx, fileHeaders, tmpDir)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if len(pages) == 0 {
		cleanup()
		return "", nil, errors.New("no pages to compose")
	}

	var pageW, pageH float64
	if orient == "L" {
		pageW, pageH = 297.0, 210.0
	} else {
		pageW, pageH = 210.0, 297.0
	}
	// 用 gofpdf.New 直接指定方向，能正确写出横向/纵向 MediaBox
	// （NewCustom + OrientationStr 在横向时不会正确翻转尺寸）。
	pdf := gofpdf.New(orient, "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)

	imp := gofpdi.NewImporter()

	cellW := pageW / float64(gridCols)
	cellH := pageH / float64(gridRows)
	perPage := gridRows * gridCols

	// 边距边界保护：0 ~ 半单元格宽度/高度（防止负槽位或重叠）
	if marginMM < 0 {
		marginMM = 0
	}
	maxMargin := math.Min(cellW, cellH) / 2
	if marginMM > maxMargin {
		marginMM = maxMargin
	}

	for i := 0; i < len(pages); i += perPage {
		pdf.AddPage()
		for s := 0; s < perPage; s++ {
			idx := i + s
			if idx >= len(pages) {
				break
			}
			row := s / gridCols
			col := s % gridCols
			slotX := float64(col)*cellW + marginMM
			slotY := float64(row)*cellH + marginMM
			slotW := cellW - 2*marginMM
			slotH := cellH - 2*marginMM
			if err := placePageInSlot(pdf, imp, &pages[idx], slotX, slotY, slotW, slotH); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("page %d: %w", idx+1, err)
			}
		}
		drawGridDashLines(pdf, pageW, pageH, gridCols, gridRows)
	}

	outPath := filepath.Join(tmpDir, "invoice_composed.pdf")
	if err := pdf.OutputFileAndClose(outPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return outPath, cleanup, nil
}

// drawGridDashLines 在合成页上画格子分隔虚线（列分隔为竖线、行分隔为横线），
// 方便打印后沿虚线裁剪。perPage=1 时不画线。
func drawGridDashLines(pdf *gofpdf.Fpdf, pageW, pageH float64, gridCols, gridRows int) {
	for c := 1; c < gridCols; c++ {
		x := float64(c) * pageW / float64(gridCols)
		drawDashLine(pdf, x, 0, x, pageH)
	}
	for r := 1; r < gridRows; r++ {
		y := float64(r) * pageH / float64(gridRows)
		drawDashLine(pdf, 0, y, pageW, y)
	}
}

const (
	idCardWidthMM  = 85.6
	idCardHeightMM = 54.0
	idCardGapMM    = 30.0
)

func composeIdCard(ctx context.Context, fileHeaders []*multipart.FileHeader, paper string) (string, func(), error) {
	if len(fileHeaders) != 2 {
		return "", nil, errors.New("id card mode requires exactly 2 files (front and back)")
	}

	tmpDir, err := os.MkdirTemp("", "compose-idcard-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	pages, err := collectPages(ctx, fileHeaders, tmpDir)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if len(pages) < 2 {
		cleanup()
		return "", nil, errors.New("failed to process id card files")
	}

	for i := range pages {
		if pages[i].imgPath != "" {
			cropped, cfg, cerr := autoCropCard(pages[i].imgPath, tmpDir)
			if cerr == nil {
				pages[i].imgPath = cropped
				pages[i].imgCfg = cfg
			}
		}
	}

	var pageW, pageH float64
	if paper == "A5" {
		pageW, pageH = 148.0, 210.0
		pdf := gofpdf.NewCustom(&gofpdf.InitType{
			OrientationStr: "P",
			UnitStr:        "mm",
			Size:           gofpdf.SizeType{Wd: pageW, Ht: pageH},
		})
		pdf.SetMargins(0, 0, 0)
		pdf.SetAutoPageBreak(false, 0)
		pdf.AddPage()

		imp := gofpdi.NewImporter()

		halfH := pageH / 2
		cardX := (pageW - idCardWidthMM) / 2
		card1Y := (halfH - idCardHeightMM) / 2
		card2Y := halfH + (halfH-idCardHeightMM)/2

		if err := placePageInSlot(pdf, imp, &pages[0], cardX, card1Y, idCardWidthMM, idCardHeightMM); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("front: %w", err)
		}
		if err := placePageInSlot(pdf, imp, &pages[1], cardX, card2Y, idCardWidthMM, idCardHeightMM); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("back: %w", err)
		}

		outPath := filepath.Join(tmpDir, "idcard_composed.pdf")
		if err := pdf.OutputFileAndClose(outPath); err != nil {
			cleanup()
			return "", nil, err
		}
		return outPath, cleanup, nil
	}

	pageW, pageH = 210.0, 297.0
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	imp := gofpdi.NewImporter()

	halfH := pageH / 2
	cardX := (pageW - idCardWidthMM) / 2
	card1Y := (halfH - idCardHeightMM) / 2
	card2Y := halfH + (halfH-idCardHeightMM)/2

	if err := placePageInSlot(pdf, imp, &pages[0], cardX, card1Y, idCardWidthMM, idCardHeightMM); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("front: %w", err)
	}
	if err := placePageInSlot(pdf, imp, &pages[1], cardX, card2Y, idCardWidthMM, idCardHeightMM); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("back: %w", err)
	}

	outPath := filepath.Join(tmpDir, "idcard_composed.pdf")
	if err := pdf.OutputFileAndClose(outPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return outPath, cleanup, nil
}

func autoCropCard(inputPath string, tmpDir string) (string, image.Config, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return inputPath, image.Config{}, err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return inputPath, image.Config{}, err
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 100 || h < 100 {
		return inputPath, image.Config{Width: w, Height: h}, nil
	}

	lum := make([][]uint8, h)
	for y := 0; y < h; y++ {
		lum[y] = make([]uint8, w)
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			lum[y][x] = uint8((r*299 + g*587 + b*114) / 1000 >> 8)
		}
	}

	rowSum := make([]float64, h)
	colSum := make([]float64, w)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx := int(lum[y][x+1]) - int(lum[y][x-1])
			gy := int(lum[y+1][x]) - int(lum[y-1][x])
			mag := math.Sqrt(float64(gx*gx + gy*gy))
			rowSum[y] += mag
			colSum[x] += mag
		}
	}

	smoothK := maxInt(w, h) / 40
	if smoothK < 3 {
		smoothK = 3
	}
	rowSmooth := smoothSlice(rowSum, smoothK)
	colSmooth := smoothSlice(colSum, smoothK)

	rowMax := sliceMax(rowSmooth)
	colMax := sliceMax(colSmooth)
	rowThresh := rowMax * 0.15
	colThresh := colMax * 0.15

	top, bottom := 0, h-1
	left, right := 0, w-1
	for top < h && rowSmooth[top] < rowThresh {
		top++
	}
	for bottom > 0 && rowSmooth[bottom] < rowThresh {
		bottom--
	}
	for left < w && colSmooth[left] < colThresh {
		left++
	}
	for right > 0 && colSmooth[right] < colThresh {
		right--
	}

	cropW := right - left
	cropH := bottom - top
	if cropW < w*3/10 || cropH < h*3/10 || cropW <= 0 || cropH <= 0 {
		return inputPath, image.Config{Width: w, Height: h}, nil
	}

	cropped := image.NewRGBA(image.Rect(0, 0, cropW, cropH))
	draw.Draw(cropped, cropped.Bounds(), img, image.Pt(left+bounds.Min.X, top+bounds.Min.Y), draw.Src)

	seq := atomic.AddUint64(&downscaleSeq, 1)
	outPath := filepath.Join(tmpDir, "cropped_"+itoa(int(seq))+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		return inputPath, image.Config{Width: w, Height: h}, err
	}
	if err := jpeg.Encode(outFile, cropped, &jpeg.Options{Quality: 95}); err != nil {
		outFile.Close()
		os.Remove(outPath)
		return inputPath, image.Config{Width: w, Height: h}, err
	}
	outFile.Close()

	return outPath, image.Config{Width: cropW, Height: cropH}, nil
}

func smoothSlice(data []float64, k int) []float64 {
	n := len(data)
	out := make([]float64, n)
	half := k / 2
	for i := 0; i < n; i++ {
		sum := 0.0
		count := 0
		for j := i - half; j <= i+half; j++ {
			if j >= 0 && j < n {
				sum += data[j]
				count++
			}
		}
		out[i] = sum / float64(count)
	}
	return out
}

func sliceMax(data []float64) float64 {
	m := 0.0
	for _, v := range data {
		if v > m {
			m = v
		}
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func collectPages(ctx context.Context, fileHeaders []*multipart.FileHeader, tmpDir string) ([]composePage, error) {
	var pages []composePage

	for idx, fh := range fileHeaders {
		src, err := fh.Open()
		if err != nil {
			return nil, err
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext == "" {
			ext = ".bin"
		}
		localPath := filepath.Join(tmpDir, "input_"+itoa(idx)+ext)
		dst, err := os.Create(localPath)
		if err != nil {
			src.Close()
			return nil, err
		}
		if _, err := dst.ReadFrom(src); err != nil {
			dst.Close()
			src.Close()
			return nil, err
		}
		dst.Close()
		src.Close()

		kind := detectFileKind(localPath, fh.Filename)
		switch kind {
		case fileKindImage:
			finalPath, cfg, err := downscaleImageIfNeeded(localPath, tmpDir)
			if err != nil {
				return nil, err
			}
			pages = append(pages, composePage{imgPath: finalPath, imgCfg: cfg})

		case fileKindPDF:
			numPages, err := countPDFPages(localPath)
			if err != nil {
				numPages = 1
			}
			// 统一用 Ghostscript 光栅化每页为高 DPI 图片（300DPI JPEG），
			// 再由 placePageInSlot 以图片方式贴到合成版面。
			// 彻底规避两个问题：
			//   1) CJK 字体未嵌入（STSong-Light 等）→ pdf.js 预览空白/失败
			//   2) GS pdfwrite 字体替换后度量不匹配 → 文字挤在一起/出框
			imgPages, err := renderPDFToImages(ctx, localPath, numPages, tmpDir)
			if err != nil {
				return nil, fmt.Errorf("cannot process PDF %s: %w", fh.Filename, err)
			}
			pages = append(pages, imgPages...)

		case fileKindOFD:
			outPath, cleanupConv, err := convertOFDToPDF(ctx, localPath)
			if err != nil {
				return nil, err
			}
			permPath := filepath.Join(tmpDir, "ofd_converted_"+itoa(idx)+".pdf")
			if copyErr := copyFile(outPath, permPath); copyErr != nil {
				cleanupConv()
				return nil, copyErr
			}
			cleanupConv()
			numPages, _ := countPDFPages(permPath)
			if numPages < 1 {
				numPages = 1
			}
			for p := 1; p <= numPages; p++ {
				pages = append(pages, composePage{pdfPath: permPath, pageNo: p})
			}

		case fileKindOffice:
			outPath, cleanupConv, err := convertOfficeToPDF(ctx, localPath)
			if err != nil {
				return nil, err
			}
			permPath := filepath.Join(tmpDir, "office_converted_"+itoa(idx)+".pdf")
			if copyErr := copyFile(outPath, permPath); copyErr != nil {
				cleanupConv()
				return nil, copyErr
			}
			cleanupConv()
			numPages, _ := countPDFPages(permPath)
			if numPages < 1 {
				numPages = 1
			}
			for p := 1; p <= numPages; p++ {
				pages = append(pages, composePage{pdfPath: permPath, pageNo: p})
			}

		default:
			return nil, errors.New("unsupported file type: " + fh.Filename)
		}
	}

	return pages, nil
}

func placePageInSlot(pdf *gofpdf.Fpdf, imp *gofpdi.Importer, page *composePage, slotX, slotY, slotW, slotH float64) (err error) {
	if page.imgPath != "" {
		imgW := float64(page.imgCfg.Width)
		imgH := float64(page.imgCfg.Height)
		scale := math.Min(slotW/imgW, slotH/imgH)
		if scale <= 0 {
			scale = 1
		}
		w := imgW * scale
		h := imgH * scale
		x := slotX + (slotW-w)/2
		y := slotY + (slotH-h)/2
		opts := gofpdf.ImageOptions{ImageType: "", ReadDpi: true}
		pdf.ImageOptions(page.imgPath, x, y, w, h, false, opts, 0, "")
		return nil
	}

	if page.pdfPath != "" {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("failed to import PDF %s: %v", filepath.Base(page.pdfPath), r)
			}
		}()

		tplId := imp.ImportPage(pdf, page.pdfPath, page.pageNo, "/MediaBox")
		sizes := imp.GetPageSizes()
		srcW, srcH := 210.0, 297.0
		if ps, ok := sizes[page.pageNo]; ok {
			if mb, ok := ps["/MediaBox"]; ok {
				if w, ok := mb["w"]; ok {
					srcW = w
				}
				if h, ok := mb["h"]; ok {
					srcH = h
				}
			}
		}
		scale := math.Min(slotW/srcW, slotH/srcH)
		if scale <= 0 {
			scale = 1
		}
		w := srcW * scale
		h := srcH * scale
		x := slotX + (slotW-w)/2
		y := slotY + (slotH-h)/2
		imp.UseImportedTemplate(pdf, tplId, x, y, w, h)
	}
	return nil
}

func drawDashLine(pdf *gofpdf.Fpdf, x1, y1, x2, y2 float64) {
	pdf.SetDrawColor(dashLineGrayLevel, dashLineGrayLevel, dashLineGrayLevel)
	pdf.SetLineWidth(0.3)
	pdf.SetDashPattern([]float64{dashLineLenMM, dashLineGapMM}, 0)
	pdf.Line(x1, y1, x2, y2)
	pdf.SetDashPattern([]float64{}, 0)
	pdf.SetDrawColor(0, 0, 0)
}

func canGofpdiParse(pdfPath string) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	imp := gofpdi.NewImporter()
	pdf := gofpdf.New("P", "mm", "A4", "")
	imp.ImportPage(pdf, pdfPath, 1, "/MediaBox")
	return true
}

func renderPDFToImages(ctx context.Context, pdfPath string, numPages int, tmpDir string) ([]composePage, error) {
	gsBin, err := exec.LookPath("gs")
	if err != nil {
		return nil, fmt.Errorf("ghostscript not found, cannot render PDF to images")
	}

	log.Printf("[compose] gofpdi cannot parse %s, falling back to gs rendering (%d pages)", filepath.Base(pdfPath), numPages)

	var pages []composePage
	for p := 1; p <= numPages; p++ {
		seq := atomic.AddUint64(&downscaleSeq, 1)
		outPath := filepath.Join(tmpDir, fmt.Sprintf("pdfrender_%d.jpg", seq))

		args := []string{
			"-dNOPAUSE", "-dBATCH", "-dSAFER", "-dQUIET",
			"-sDEVICE=jpeg", "-dJPEGQ=95", "-r300",
			// 按内容区的 CropBox 裁掉白边（如滴滴发票内容只占上半页，
			// 不裁的话会把整张 A4 渲染进去，放进半页槽位时铺不满）。
			// 无 CropBox 的 PDF 会自动回退到 MediaBox，安全通用。
			"-dUseCropBox",
			fmt.Sprintf("-dFirstPage=%d", p),
			fmt.Sprintf("-dLastPage=%d", p),
			"-sOutputFile=" + outPath,
			pdfPath,
		}
		cmd := exec.CommandContext(ctx, gsBin, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("gs render page %d failed: %w - %s", p, err, string(out))
		}

		f, err := os.Open(outPath)
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read rendered image: %w", err)
		}
		pages = append(pages, composePage{imgPath: outPath, imgCfg: cfg})
	}
	return pages, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}
