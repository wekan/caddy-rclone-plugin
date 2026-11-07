package caddyrclone

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// serve runs a real Caddy with one rclone handler and returns its address.
func serve(t *testing.T, handler map[string]any) string {
	t.Helper()
	site := freeAddr(t)
	handler["handler"] = "rclone"
	cfg := map[string]any{
		"admin": map[string]any{"disabled": true},
		"apps": map[string]any{"http": map[string]any{"servers": map[string]any{"s": map[string]any{
			"listen": []string{site}, "automatic_https": map[string]any{"disable": true},
			"routes": []any{map[string]any{"handle": []any{handler}}},
		}}}},
	}
	raw, _ := json.Marshal(cfg)
	if err := caddy.Load(raw, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caddy.Stop() })
	return "http://" + site
}

func do(t *testing.T, method, url, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func remote(t *testing.T) string {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "hello.txt"), []byte("hello from rclone"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadOnlyFiles(t *testing.T) {
	dir := remote(t)
	base := serve(t, map[string]any{"remote": dir})
	if code, body := do(t, "GET", base+"/docs/hello.txt", "", nil); code != 200 || body != "hello from rclone" {
		t.Fatalf("download: %d %q", code, body)
	}
	if code, body := do(t, "GET", base+"/docs/hello.txt", "", map[string]string{"Range": "bytes=6-9"}); code != 206 || body != "from" {
		t.Fatalf("range: %d %q", code, body)
	}
	if code, body := do(t, "GET", base+"/docs/", "", nil); code != 200 || !strings.Contains(body, "hello.txt") {
		t.Fatalf("listing: %d %q", code, body)
	}
	if code, _ := do(t, "PUT", base+"/docs/new.txt", "x", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("negative: without webdav a PUT must be refused, got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "new.txt")); err == nil {
		t.Fatal("negative: the refused PUT wrote a file")
	}
	if code, _ := do(t, "GET", base+"/../../etc/passwd", "", nil); code == 200 {
		t.Fatal("negative: a path outside the remote must not be served")
	}
}

func TestWebDAV(t *testing.T) {
	dir := remote(t)
	base := serve(t, map[string]any{"remote": ":local:" + dir, "webdav": true, "prefix": "/files"})
	if code, _ := do(t, "MKCOL", base+"/files/new", "", nil); code != http.StatusCreated {
		t.Fatalf("MKCOL: %d", code)
	}
	if code, _ := do(t, "PUT", base+"/files/new/upload.txt", "uploaded", nil); code != http.StatusCreated {
		t.Fatalf("PUT: %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(filepath.Join(dir, "new", "upload.txt")); string(got) == "uploaded" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if string(got) != "uploaded" {
		t.Fatalf("the upload did not reach the remote: %q", got)
	}
	code, body := do(t, "PROPFIND", base+"/files/new/", "", map[string]string{"Depth": "1"})
	if code != 207 || !strings.Contains(body, "/files/new/upload.txt") {
		t.Fatalf("PROPFIND (hrefs carry the prefix): %d %.300s", code, body)
	}
	if code, body := do(t, "GET", base+"/files/docs/hello.txt", "", nil); code != 200 || body != "hello from rclone" {
		t.Fatalf("GET with webdav: %d %q", code, body)
	}
	if code, _ := do(t, "DELETE", base+"/files/new/upload.txt", "", nil); code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", code)
	}
	if code, _ := do(t, "DELETE", base+"/files/", "", nil); code < 400 {
		t.Fatalf("negative: deleting the remote's root must be refused, got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "hello.txt")); err != nil {
		t.Fatal("negative: the remote's content is gone")
	}
}

func TestWebDAVReadOnly(t *testing.T) {
	dir := remote(t)
	base := serve(t, map[string]any{"remote": dir, "webdav": true, "read_only": true})
	if code, _ := do(t, "PROPFIND", base+"/docs/", "", map[string]string{"Depth": "1"}); code != 207 {
		t.Fatalf("PROPFIND read-only: %d", code)
	}
	for _, m := range []string{"PUT", "DELETE", "MKCOL", "MOVE"} {
		if code, _ := do(t, m, base+"/docs/hello.txt", "x", nil); code != http.StatusMethodNotAllowed {
			t.Errorf("negative: read_only must refuse %s, got %d", m, code)
		}
	}
}

func TestCaddyfile(t *testing.T) {
	adapter := caddyfile.Adapter{ServerType: httpcaddyfile.ServerType{}}
	out, _, err := adapter.Adapt([]byte("dav.example.org {\n\trclone mydrive:shared {\n\t\tconfig /etc/rclone/rclone.conf\n\t\twebdav\n\t\tprefix /dav\n\t\tcache_mode full\n\t}\n}\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"handler":"rclone"`, `"remote":"mydrive:shared"`, `"config":"/etc/rclone/rclone.conf"`, `"webdav":true`, `"prefix":"/dav"`, `"cache_mode":"full"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("adapted config lacks %s: %s", want, out)
		}
	}
	for name, bad := range map[string]string{
		"no remote":         "x.example.org {\n\trclone\n}\n",
		"an unknown option": "x.example.org {\n\trclone r: {\n\t\tdelete_all\n\t}\n}\n",
	} {
		if _, _, err := adapter.Adapt([]byte(bad), nil); err == nil {
			t.Errorf("negative: %s must be refused", name)
		}
	}
}
