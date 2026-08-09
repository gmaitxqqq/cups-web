package main

import (
	"log"
	"net/http"

	"cups-web/internal/ipp"
)

// printerInfoHandler handles GET /api/printer-info?uri=<printer_uri>
// It queries the printer via IPP Get-Printer-Attributes and returns structured info.
func printerInfoHandler(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	log.Printf("[printer-info] request received, uri=%q", uri)

	if uri == "" {
		log.Printf("[printer-info] error: missing uri parameter")
		writeJSONError(w, http.StatusBadRequest, "missing uri parameter")
		return
	}

	log.Printf("[printer-info] calling GetPrinterAttributes for uri=%q", uri)
	info, err := ipp.GetPrinterAttributes(uri)
	if err != nil {
		// 不把底层错误（含 SSRF 策略细节 / 内网探测信息）回显给客户端，
		// 仅记服务端日志，对外返回泛化提示。
		log.Printf("[printer-info] GetPrinterAttributes error: %v", err)
		writeJSONError(w, http.StatusBadGateway, "failed to get printer info")
		return
	}

	log.Printf("[printer-info] success: name=%q state=%q jobs=%d", info.Name, info.State, info.QueuedJobs)
	writeJSON(w, info)
}
