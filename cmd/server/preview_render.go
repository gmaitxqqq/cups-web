package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// renderPDFToPreviewPages 把最终 PDF 的每一页渲染成一张 PNG（110 DPI，屏幕预览足够），
// 以 data URL 数组返回，由前端逐页展示并提供翻页（左右箭头 / 1/N 计数）。
//
// 设计要点：
//   - 用 GS 的 png16m 设备逐页渲染（110 DPI，文件不大）
//   - 每页独立返回，不再纵向拼接成一张长图（避免"多页被误认为挤在一页"）
//   - 纯 stdlib 实现，不引入额外依赖
func renderPDFToPreviewPages(ctx context.Context, pdfPath string) ([]string, func(), error) {
	gsBin, err := exec.LookPath("gs")
	if err != nil {
		return nil, nil, fmt.Errorf("preview: ghostscript %w", errBinaryNotInstalled)
	}

	numPages, _ := countPDFPages(pdfPath)
	if numPages < 1 {
		numPages = 1
	}

	tmpDir, err := os.MkdirTemp("", "pdf-preview-")
	if err != nil {
		return nil, nil, fmt.Errorf("preview: tmpdir: %w", err)
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
		return nil, nil, fmt.Errorf("preview: gs render failed: %w - %s", err, firstErrorLine(string(out)))
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
		return nil, nil, fmt.Errorf("preview: gs produced no PNG for %s", filepath.Base(pdfPath))
	}

	pages := make([]string, 0, len(pageFiles))
	for _, f := range pageFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("preview: read page png: %w", err)
		}
		pages = append(pages, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	return pages, cleanup, nil
}
