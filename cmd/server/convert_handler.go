package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

func convertHandler(w http.ResponseWriter, r *http.Request) {
	// Expect multipart form
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	// 读取方向和纸张大小参数
	orientation := r.FormValue("orientation")
	paperSize := r.FormValue("paper_size")

	// 图片排版参数（仅对图片转换生效）：scale=0 自动适应；1-100 为手动百分比；
	// align=center/left/right 控制水平对齐；valign=top/center/bottom 控制垂直对齐。
	// 后端 convertImageToPDF / convertImagesMultiToPDF 使用。
	scaleStr := r.FormValue("scale")
	align := r.FormValue("align")
	valign := r.FormValue("valign")
	var scalePct float64
	if scaleStr != "" {
		if v, e := strconv.ParseFloat(scaleStr, 64); e == nil {
			scalePct = v
		}
	}
	if scalePct < 0 {
		scalePct = 0
	}
	if scalePct > 100 {
		scalePct = 100
	}
	switch align {
	case "left", "right":
	default:
		align = "center"
	}
	switch valign {
	case "top", "bottom":
	default:
		valign = "center"
	}

	var outPath string
	var outCleanup func()
	var outFilename string
	var err error

	// 优先处理多文件字段（图片合并场景）
	if r.MultipartForm != nil {
		if headers, ok := r.MultipartForm.File["files"]; ok && len(headers) > 0 {
			outPath, outCleanup, err = convertImagesMultiToPDF(headers, orientation, paperSize, scalePct, align, valign)
			if err != nil {
				http.Error(w, "conversion failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer outCleanup()

			// 输出文件名：优先用前端传入的 name，否则用默认的
			outFilename = r.FormValue("name")
			if outFilename == "" {
				outFilename = "合并图片.pdf"
			}

			streamPDF(w, outPath, outFilename)
			return
		}
	}

	// 单文件分支
	file, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	inPath, cleanup, err := saveTempUpload(file, fh.Filename)
	if err != nil {
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}
	defer cleanup()

	ctx, cancel := convertTimeoutContext(r.Context())
	defer cancel()

	kind := detectFileKind(inPath, fh.Filename)
	switch kind {
	case fileKindImage:
		outPath, outCleanup, err = convertImageToPDF(inPath, orientation, paperSize, scalePct, align, valign)
	case fileKindText:
		outPath, outCleanup, err = convertTextToPDF(inPath, orientation, paperSize)
	case fileKindOFD:
		outPath, outCleanup, err = convertOFDToPDF(ctx, inPath)
	case fileKindPDF:
		// 默认不再对上传 PDF 走 gs：客户端在 UI 点击"应用 GS 规范化"时
		// 才会带上 normalize=true 显式触发，用于修复 CJK 字体乱码等问题。
		// 否则原样回传，预览端使用原始字节，打印端也读同一份字节，预览/打印一致。
		if r.FormValue("normalize") == "true" {
			diagnosePDF(inPath)
			// format=imagepdf: 光栅化路径（图片 PDF），解决两个问题：
			//   1) GS pdfwrite 字体替换后度量不匹配 → 排版挤在一起/出框
			//   2) pdf.js 无法渲染 GS 嵌入的假 CJK 字体 → 预览空白/错位
			//   输出是每页一张 300DPI JPEG 的 PDF，预览和打印完全一致。
			if r.FormValue("format") == "imagepdf" {
				res, normErr := normalizePDFToImagePDF(ctx, inPath)
				if normErr != nil {
					err = normErr
				} else {
					outPath = res.OutputPath
					if res.Cleanup != nil {
						outCleanup = res.Cleanup
					} else {
						outCleanup = func() {}
					}
				}
			} else {
				res, normErr := normalizePDF(ctx, inPath)
				if normErr != nil {
					err = normErr
				} else {
					outPath = res.OutputPath
					if res.Cleanup != nil {
						outCleanup = res.Cleanup
					} else {
						outCleanup = func() {}
					}
				}
			}
		} else {
			outPath = inPath
			outCleanup = func() {}
		}
	default:
		outPath, outCleanup, err = convertOfficeToPDF(ctx, inPath)
	}
	if err != nil {
		http.Error(w, "conversion failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer outCleanup()

	// 预览专用：format=png 时把最终 PDF 逐页渲染成 PNG，以 JSON 数组返回，
	// 前端逐页展示并提供翻页；彻底绕过 pdf.js。
	if r.FormValue("format") == "png" {
		pages, pngCleanup, perr := renderPDFToPreviewPages(ctx, outPath)
		if perr != nil {
			http.Error(w, "preview render failed: "+perr.Error(), http.StatusInternalServerError)
			return
		}
		defer pngCleanup()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"total": len(pages),
			"pages": pages,
		})
		return
	}

	base := filepath.Base(fh.Filename)
	ext := filepath.Ext(base)
	name := base[0 : len(base)-len(ext)]
	outFilename = name + ".pdf"

	streamPDF(w, outPath, outFilename)
}

// streamPDF 以 application/pdf 的 Content-Type 把 PDF 文件流式写回响应
func streamPDF(w http.ResponseWriter, path string, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	pdfFile, err := os.Open(path)
	if err != nil {
		http.Error(w, "failed to open converted file", http.StatusInternalServerError)
		return
	}
	defer pdfFile.Close()
	if _, err := io.Copy(w, pdfFile); err != nil {
		// nothing more we can do
		return
	}
}
