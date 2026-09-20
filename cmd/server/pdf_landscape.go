package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ═══════════════════════════════════════════════════════════════════
// 横向打印归一化（Landscape print normalization）
// ═══════════════════════════════════════════════════════════════════
//
// 背景（Issue: 横向4合1打成纵向）：
// 本部署的打印机（brlaser / Brother 系）PPD 中没有 *OpenUI *Orientation 组，
// PageSize / PageRegion 只有纵向条目（A4 = 595x842pt）。这类 PPD 的
// orientation-requested-supported 实际上只有 3（portrait），客户端提交的
// orientation-requested=4 不会传导到打印驱动，也不会触发 pdftopdf 的旋转，
// 结果是：横向页面（842x595pt）被原样塞进纵向纸张（595x842pt）——
// 右侧约 30% 的内容直接跑到成像区之外被裁掉（发票第二列消失），
// 而 print-scaling=none 又阻止了任何缩放兜底。
//
// 解决办法：不再依赖打印机的方向能力，在提交打印前把「横向作业」自己
// 归一化成【纵向纸张页面 + 内容旋转 90° 填满整页】。GS 的 FIXEDMEDIA +
// PDFFitPage 会把整页等比缩放（297x210 → 210x297，比例 0.7071，正好铺满
// 无白边），Orientation 1 负责把内容旋转 90°，与 CUPS 自身处理横向文档
// 到纵向纸张时的标准行为完全一致。
//
// 归一化后按 portrait 提交（不再发送 orientation-requested=4），
// 打印机只需处理最普通的纵向 A4，任何驱动都能正确输出。
// 预览仍展示横向版面，用户把出纸横向摆放即可看到横向 2x2 效果。

// paperSizePoints 把前端纸张名称换算成 Ghostscript 设备尺寸（单位：pt）。
// 与 internal/ipp.paperSizeToIPP 的纸张表保持一致；未识别的名称回退 A4。
func paperSizePoints(size string) (float64, float64) {
	switch size {
	case "A5":
		return 420, 595
	case "A3":
		return 842, 1191
	case "A2":
		return 1191, 1684
	case "A1":
		return 1684, 2384
	case "Letter":
		return 612, 792
	case "Legal":
		return 612, 1008
	case "5inch": // 5 x 7 in
		return 360, 504
	case "6inch": // 3.5 x 5 in
		return 252, 360
	case "7inch": // 7 x 5 in
		return 504, 360
	case "8inch": // 8 x 10 in
		return 576, 720
	case "10inch": // 10 x 12 in
		return 720, 864
	default:
		return 595, 842 // A4
	}
}

// orientPDFForLandscapePrint 把「横向打印」的作业转成纵向纸张上的旋转版：
//
//	输入：任意 PDF（典型为横向 A4，842x595pt，如横向 2/4 合 1 发票）
//	输出：每页 MediaBox 为纵向纸张尺寸（A4 → 595x842pt）的 PDF，
//	      原页面内容等比缩放并旋转 90° 后填满整页。
//
// 返回的 OutputPath 可直接交给 IPP 以 portrait 提交。Cleanup 负责清理临时目录。
// 页数与源文档一致（旋转不改变页数），因此调用方可以沿用原来的 pages 计数。
func orientPDFForLandscapePrint(ctx context.Context, inputPath string, paperSize string) (*normalizePDFResult, error) {
	gsBin, err := exec.LookPath("gs")
	if err != nil {
		return nil, fmt.Errorf("landscape-orient: ghostscript %w", errBinaryNotInstalled)
	}

	pageW, pageH := paperSizePoints(paperSize)
	// 归一化目标是纵向纸张：宽必须小于高（照片纸表里存在宽>高的条目，这里纠正）
	if pageW > pageH {
		pageW, pageH = pageH, pageW
	}

	tmpDir, err := os.MkdirTemp("", "pdf-landscape-")
	if err != nil {
		return nil, fmt.Errorf("landscape-orient: tmpdir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	outPath := filepath.Join(tmpDir, "landscape-oriented.pdf")

	args := []string{
		"-dNOPAUSE", "-dBATCH", "-dSAFER", "-dQUIET",
		"-sDEVICE=pdfwrite",
		"-dCompatibilityLevel=1.4",
		"-sOutputFile=" + outPath,
		// 固定为纵向纸张尺寸，避免 GS 沿用源页面（横向）尺寸
		"-dFIXEDMEDIA",
		fmt.Sprintf("-dDEVICEWIDTHPOINTS=%.0f", pageW),
		fmt.Sprintf("-dDEVICEHEIGHTPOINTS=%.0f", pageH),
		// 等比缩放并居中，保证整页内容完整落在纸张内（不裁切）
		"-dPDFFitPage",
		// 关掉 GS 的自动旋转，旋转完全由下面的 Orientation 决定，行为可预测
		"-dAutoRotatePages=/None",
		// Orientation 1 = 内容旋转 90°（与 CUPS 处理横向文档到纵向纸张的方向一致）
		"-c", "<</Orientation 1>> setpagedevice",
	}
	// cidfmap 搜索路径（Docker 运行时为 /etc/ghostscript），本地开发时为空
	args = append(args, cidfmapPreambleArgs()...)
	// -f 必须是最后一个参数，其后跟着输入文件
	args = append(args, "-f", inputPath)

	start := time.Now()
	cmd := exec.CommandContext(ctx, gsBin, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("landscape-orient: gs failed: %w - %s", err, firstErrorLine(string(out)))
	}
	if st, serr := os.Stat(outPath); serr != nil || st.Size() == 0 {
		cleanup()
		return nil, fmt.Errorf("landscape-orient: gs produced empty output: %v", serr)
	}

	log.Printf("[print] landscape-oriented to portrait sheet %.0fx%.0fpt elapsed=%s in=%s",
		pageW, pageH, time.Since(start).Round(time.Millisecond), filepath.Base(inputPath))

	return &normalizePDFResult{
		OutputPath: outPath,
		Cleanup:    cleanup,
		Method:     "landscape-orient",
	}, nil
}
