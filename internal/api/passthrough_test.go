package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRewriteMultipartModel 验证 multipart 表单的 model 重写：
// 已有 model 字段被替换、文件与其它字段原样保留、缺失时自动补上。
func TestRewriteMultipartModel(t *testing.T) {
	build := func(withModel bool) (*httptest.ResponseRecorder, *http.Request) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		_ = mw.WriteField("purpose", "batch")
		if withModel {
			_ = mw.WriteField("model", "glm-4.7-flash")
		}
		_ = mw.WriteField("model", "glm-4.7-flash") // 重复字段也应收敛为一个
		part, _ := mw.CreateFormFile("file", "input.jsonl")
		_, _ = part.Write([]byte(`{"a":1}` + "\n"))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/v1/files", body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		return rec, req
	}

	// ---- 场景 1：model 已存在 → 被替换 ----
	_, req := build(true)
	payload, ct, err := rewriteMultipartModel(req, "upstream-model-x")
	if err != nil {
		t.Fatalf("重写失败：%v", err)
	}
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Fatalf("Content-Type 不对：%s", ct)
	}
	check := func(payload []byte) {
		sub := multipart.NewReader(bytes.NewReader(payload), ct[strings.Index(ct, "boundary=")+len("boundary="):])
		form, err := sub.ReadForm(1 << 20)
		if err != nil {
			t.Fatalf("解析重写后的表单失败：%v", err)
		}
		if got := form.Value["model"]; len(got) != 1 || got[0] != "upstream-model-x" {
			t.Fatalf("model 应为单个 upstream-model-x，实际 %v", got)
		}
		if got := form.Value["purpose"]; len(got) != 1 || got[0] != "batch" {
			t.Fatalf("purpose 应原样保留，实际 %v", got)
		}
		fds := form.File["file"]
		if len(fds) != 1 || fds[0].Filename != "input.jsonl" {
			t.Fatalf("文件应原样保留，实际 %v", fds)
		}
		fh, _ := fds[0].Open()
		data, _ := io.ReadAll(fh)
		fh.Close()
		if string(data) != `{"a":1}`+"\n" {
			t.Fatalf("文件内容不一致：%q", data)
		}
	}
	check(payload)

	// ---- 场景 2：model 缺失 → 自动补上 ----
	body2 := &bytes.Buffer{}
	mw2 := multipart.NewWriter(body2)
	_ = mw2.WriteField("purpose", "batch")
	_ = mw2.Close()
	req2 := httptest.NewRequest("POST", "/v1/files", body2)
	req2.Header.Set("Content-Type", mw2.FormDataContentType())
	payload2, ct2, err := rewriteMultipartModel(req2, "upstream-model-y")
	if err != nil {
		t.Fatalf("重写失败：%v", err)
	}
	sub2 := multipart.NewReader(bytes.NewReader(payload2), ct2[strings.Index(ct2, "boundary=")+len("boundary="):])
	form2, err := sub2.ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := form2.Value["model"]; len(got) != 1 || got[0] != "upstream-model-y" {
		t.Fatalf("缺失 model 应自动补上，实际 %v", got)
	}
}
