package main

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strconv"
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
		marginMM := defaultComposeMarginMM
		if mv := r.FormValue("margin"); mv != "" {
			if f, err := strconv.ParseFloat(mv, 64); err == nil && f >= 0 {
				marginMM = f
			}
		}
		outPath, cleanup, err = composeInvoice(ctx, headers, layout, marginMM)
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

	// 预览专用：format=png 时把合成结果逐页渲染成 PNG，以 JSON 数组返回，
	// 前端逐页展示并提供翻页（左右箭头 / 1/N 计数），彻底绕过 pdf.js。
	if r.FormValue("format") == "png" {
		pages, pngCleanup, perr := renderPDFToPreviewPages(ctx, outPath)
		if perr != nil {
			writeJSONError(w, http.StatusInternalServerError, "preview render failed: "+perr.Error())
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

	streamPDF(w, outPath, "composed.pdf")
}
