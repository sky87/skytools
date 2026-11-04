package skytools

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBtopUpdateChecksDigestBeforeReplacingBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary installer")
	}
	m := testManager(t)
	body := "#!/bin/sh\necho btop version: 1.2.3\n"
	archive := tarData(t, []*tar.Header{{Name: "btop/bin/btop", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(body))}}, []string{body})
	hash := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	asset := "btop-" + arch + "-unknown-linux-musl.tar.gz"
	downloads := 0
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		var data []byte
		switch req.URL.String() {
		case "https://api.github.com/repos/aristocratos/btop/releases/latest":
			data = []byte(fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"digest":%q,"browser_download_url":%q}]}`, asset, digest, "https://github.com/aristocratos/btop/releases/download/v1.2.3/"+asset))
		case "https://github.com/aristocratos/btop/releases/download/v1.2.3/" + asset:
			downloads++
			data = archive
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if err := m.installBtop(); err != nil {
		t.Fatal(err)
	}
	binary := m.at(".local/bin/btop")
	if data, err := os.ReadFile(binary); err != nil || string(data) != body || !executable(binary) {
		t.Fatalf("binary=%q err=%v", data, err)
	}
	for _, invalid := range []string{"", "sha256:" + strings.Repeat("0", 64)} {
		digest = invalid
		before := downloads
		if err := m.installBtop(); err == nil {
			t.Fatal("accepted missing or incorrect checksum")
		}
		if invalid == "" && downloads != before {
			t.Fatal("downloaded without a publisher digest")
		}
		data, err := os.ReadFile(binary)
		if err != nil || string(data) != body {
			t.Fatal("failed update replaced working binary")
		}
	}
	staging, _ := filepath.Glob(m.at(".local/state/skytools/.download-*"))
	if len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
}
