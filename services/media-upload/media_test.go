package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type fakeMeta struct {
	files map[string]MediaFile
	fail  bool
}

func (f *fakeMeta) Create(_ context.Context, m MediaFile) (MediaFile, error) {
	if f.fail {
		return MediaFile{}, errors.New("db down")
	}
	m.ID = "m" + strconv.Itoa(len(f.files)+1)
	m.UploadedAt = time.Now()
	f.files[m.ID] = m
	return m, nil
}

func (f *fakeMeta) Get(_ context.Context, id string) (MediaFile, error) {
	m, ok := f.files[id]
	if !ok {
		return MediaFile{}, ErrNotFound
	}
	return m, nil
}

func (f *fakeMeta) Delete(_ context.Context, id string) error {
	delete(f.files, id)
	return nil
}

type fakeObjects struct{ data map[string][]byte }

func (f *fakeObjects) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	f.data[key] = b
	return err
}

func (f *fakeObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data[key])), nil
}

func (f *fakeObjects) Remove(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 100)...)

func newServer() (*Server, *fakeMeta, *fakeObjects) {
	m := &fakeMeta{files: map[string]MediaFile{}}
	o := &fakeObjects{data: map[string][]byte{}}
	return &Server{meta: m, objects: o}, m, o
}

func multipartBody(t *testing.T, field string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, "upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(content)
	w.Close()
	return &buf, w.FormDataContentType()
}

func upload(t *testing.T, s *Server, user string, content []byte) (int, map[string]any) {
	t.Helper()
	body, ct := multipartBody(t, "file", content)
	req := httptest.NewRequest("POST", "/media/upload", body)
	req.Header.Set("Content-Type", ct)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	return serve(t, s, req)
}

func serve(t *testing.T, s *Server, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestUploadAndFetch(t *testing.T) {
	s, _, objects := newServer()

	code, out := upload(t, s, "u1", pngBytes)
	if code != 201 {
		t.Fatalf("upload: %d %v", code, out)
	}
	d := out["data"].(map[string]any)
	if d["contentType"] != "image/png" || d["type"] != "image" || d["ownerId"] != "u1" || d["url"] != "/api/v1/media/m1/content" {
		t.Fatalf("unexpected metadata: %v", d)
	}
	if len(objects.data) != 1 {
		t.Fatalf("object not stored")
	}

	if code, out := serve(t, s, httptest.NewRequest("GET", "/media/m1", nil)); code != 200 || out["data"].(map[string]any)["id"] != "m1" {
		t.Fatalf("get: %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/media/m1/content", nil))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes) || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if code, _ := serve(t, s, httptest.NewRequest("GET", "/media/nope", nil)); code != 404 {
		t.Fatalf("missing: %d", code)
	}
}

func TestUploadRejects(t *testing.T) {
	s, _, objects := newServer()

	if code, _ := upload(t, s, "", pngBytes); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, out := upload(t, s, "u1", []byte("<html><script>alert(1)</script></html>")); code != 415 || errCode(out) != "UNSUPPORTED_TYPE" {
		t.Fatalf("html: %d %v", code, out)
	}
	if code, _ := upload(t, s, "u1", []byte{}); code != 400 {
		t.Fatalf("empty: %d", code)
	}
	big := append(append([]byte{}, pngBytes...), make([]byte, maxUploadBytes)...)
	if code, out := upload(t, s, "u1", big); code != 413 {
		t.Fatalf("too big: %d %v", code, out)
	}

	body, ct := multipartBody(t, "wrong-field", pngBytes)
	req := httptest.NewRequest("POST", "/media/upload", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-User-Id", "u1")
	if code, _ := serve(t, s, req); code != 400 {
		t.Fatalf("wrong field: %d", code)
	}
	if len(objects.data) != 0 {
		t.Fatalf("rejected uploads must not be stored: %d objects", len(objects.data))
	}
}

func TestFailedMetadataRemovesObject(t *testing.T) {
	s, meta, objects := newServer()
	meta.fail = true
	if code, _ := upload(t, s, "u1", pngBytes); code != 500 {
		t.Fatalf("expected 500, got %d", code)
	}
	if len(objects.data) != 0 {
		t.Fatal("object left behind after metadata failure")
	}
}

func TestDeleteOwnerOnly(t *testing.T) {
	s, _, objects := newServer()
	upload(t, s, "u1", pngBytes)

	del := func(user string) int {
		req := httptest.NewRequest("DELETE", "/media/m1", nil)
		if user != "" {
			req.Header.Set("X-User-Id", user)
		}
		code, _ := serve(t, s, req)
		return code
	}
	if code := del(""); code != 401 {
		t.Fatalf("anonymous delete: %d", code)
	}
	if code := del("u2"); code != 403 {
		t.Fatalf("non-owner delete: %d", code)
	}
	if code := del("u1"); code != 200 {
		t.Fatalf("owner delete: %d", code)
	}
	if len(objects.data) != 0 {
		t.Fatal("object not removed")
	}
	if code := del("u1"); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}
