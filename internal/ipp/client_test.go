package ipp

import (
	"bytes"
	"strings"
	"testing"

	goipp "github.com/OpenPrinting/goipp"
)

// TestNormalizePageSet 覆盖 CUPS page-set 合法值（all / odd / even）以及
// 非法值、大小写、前后空白的规范化逻辑。空串必须映射为 "all"，未知值
// 必须返回空串，让调用方完全跳过 IPP 属性，避免被打印机拒绝。
func TestNormalizePageSet(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "all"},
		{"all", "all"},
		{"ALL", "all"},
		{"  odd ", "odd"},
		{"Odd", "odd"},
		{"even", "even"},
		{"EVEN", "even"},
		{"bogus", ""},
		{"1-5", ""},
	}
	for _, c := range cases {
		if got := normalizePageSet(c.input); got != c.want {
			t.Errorf("normalizePageSet(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestParsePageRange 为现有的 parsePageRange 补一组回归用例，防止未来对
// page-ranges 逻辑的调整误伤（当前仓库此前没有 ipp 包的测试）。
func TestParsePageRange(t *testing.T) {
	got := parsePageRange("1-5 8 10-12")
	want := [][2]int{{1, 5}, {8, 8}, {10, 12}}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// 非法片段（0、空、倒序）应被静默丢弃，而不是 panic
	if r := parsePageRange("0 -3 5-3 "); len(r) != 0 {
		t.Errorf("bad ranges should be dropped, got %v", r)
	}
}

// buildJobMessage 复刻 SendPrintJob 的请求构造流程（只取 Job 组），
// 用于离线校验 PrintJobOptions → IPP 属性的映射正确性。之所以不直接
// 跑 SendPrintJob，是因为那个函数内部需要真实的 HTTP 连接，测试中
// 没法方便地 mock。
func buildJobMessage(opts PrintJobOptions) *goipp.Message {
	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpPrintJob, 1)
	if set := normalizePageSet(opts.PageSet); set != "" && set != "all" {
		req.Job.Add(goipp.MakeAttribute("page-set", goipp.TagKeyword, goipp.String(set)))
	}
	return req
}

// findJobAttr 在 Job 组中按名称查找属性，返回其第一个字符串值。
// 未命中时返回空串，供测试用 "应当/不应当存在" 两路断言。
func findJobAttr(msg *goipp.Message, name string) string {
	for _, a := range msg.Job {
		if a.Name == name && len(a.Values) > 0 {
			return a.Values[0].V.String()
		}
	}
	return ""
}

// TestSendPrintJob_PageSetEncoding 验证 page-set 在不同 PageSet 入参下
// 是否按预期进入 IPP Job 组（all 不发、odd/even 落地、非法值丢弃）。
func TestSendPrintJob_PageSetEncoding(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantHas bool
		wantVal string
	}{
		{"default empty -> omitted", "", false, ""},
		{"explicit all -> omitted", "all", false, ""},
		{"odd injected", "odd", true, "odd"},
		{"even injected", "even", true, "even"},
		{"mixed case normalized", "EVEN", true, "even"},
		{"bogus dropped", "first-page", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := buildJobMessage(PrintJobOptions{PageSet: c.input})
			got := findJobAttr(msg, "page-set")
			if c.wantHas {
				if got != c.wantVal {
					t.Errorf("page-set = %q, want %q", got, c.wantVal)
				}
			} else if got != "" {
				t.Errorf("page-set should be omitted, got %q", got)
			}

			// 同时通过编解码验证属性能在 wire format 上 round-trip，
			// 避免 goipp 对 keyword 类型的编码路径出现意外回归。
			payload, err := msg.EncodeBytes()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var decoded goipp.Message
			if err := decoded.Decode(bytes.NewReader(payload)); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got2 := findJobAttr(&decoded, "page-set"); got2 != got {
				t.Errorf("round-trip mismatch: encoded %q, decoded %q", got, got2)
			}
		})
	}
}

// TestSendPrintJob_PageSetNotLeakingIntoOperation 额外确认 page-set 不会被
// 误写到 Operation 组（CUPS 对组别敏感，写错位置会被直接忽略或报错）。
func TestSendPrintJob_PageSetNotLeakingIntoOperation(t *testing.T) {
	msg := buildJobMessage(PrintJobOptions{PageSet: "odd"})
	for _, a := range msg.Operation {
		if strings.EqualFold(a.Name, "page-set") {
			t.Fatalf("page-set leaked into Operation group: %+v", a)
		}
	}
}
