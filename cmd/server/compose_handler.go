package main

import (
	"mime/multipart"
	"net/http"
	"os"
)

func composeHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	mode := r.FormValue("mode")
	if mode == "" {
		writeJSONError(w, http.StatusBadRequest, "missing mode field")
		return
	}

	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		if fhs, ok := r.MultipartForm.File["files"]; ok {
			headers = fhs
		}
	}
	if len(headers) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no files provided")
		return
	}

	ctx, cancel := convertTimeoutContext(r.Context())
	defer cancel()

	var outPath string
	var cleanup func()
	var err error

	switch mode {
	case "invoice":
		layout := r.FormValue("layout")
		outPath, cleanup, err = composeInvoice(ctx, headers, layout)
	case "id_card":
		paper := r.FormValue("paper")
		if paper == "" {
			paper = "A4"
		}
		outPath, cleanup, err = composeIdCard(ctx, headers, paper)
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported mode: "+mode)
		return
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "compose failed: "+err.Error())
		return
	}
	defer cleanup()

	// 预览专用：format=png 时把合成结果渲染成 PNG 返回，前端用 <img> 显示，
	// 彻底绕过 pdf.js（worker 在受限部署中可能无法加载）。
	if r.FormValue("format") == "png" {
		pngPath, pngCleanup, perr := renderPDFToPreviewPNG(ctx, outPath)
		if perr != nil {
			writeJSONError(w, http.StatusInternalServerError, "preview render failed: "+perr.Error())
			return
		}
		defer pngCleanup()
		data, rerr := os.ReadFile(pngPath)
		if rerr != nil {
			writeJSONError(w, http.StatusInternalServerError, "preview read failed")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", "inline; filename=\"preview.png\"")
		w.Write(data)
		return
	}

	streamPDF(w, outPath, "composed.pdf")
}
