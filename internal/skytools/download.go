package skytools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *manager) download(url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "skytools")
	var resp *http.Response
	for attempt := 0; attempt < 6; attempt++ {
		resp, err = m.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		retry := err != nil
		if resp != nil {
			err = fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
			retry = resp.StatusCode == 429 || resp.StatusCode >= 500
			resp.Body.Close()
		}
		if !retry || attempt == 5 {
			return err
		}
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-time.After(time.Second << attempt):
		}
	}
	defer resp.Body.Close()
	_, err = io.Copy(dst, resp.Body)
	return err
}

func (m *manager) fetch(url string) ([]byte, error) {
	var b strings.Builder
	err := m.download(url, &b)
	return []byte(b.String()), err
}

func (m *manager) installer(url, shell string, args ...string) error {
	dir, err := os.MkdirTemp("", "skytools-installer-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "install.sh")
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	err = m.download(url, f)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return m.run(append([]string{shell, name}, args...)...)
}

func validChecksum(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size
}

func checksumFor(data []byte, name string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name && validChecksum(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("release has no valid SHA-256 checksum for %s", name)
}

// Extract into a private staging directory. Never follow archive-created symlinks
// while writing another entry, and never permit a link to escape that directory.
func archivePath(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute archive path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	target := filepath.Join(root, clean)
	for parent := filepath.Dir(target); parent != root && parent != filepath.Dir(root); parent = filepath.Dir(parent) {
		if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("archive path traverses symlink: %q", name)
		}
	}
	return target, nil
}

func extractTar(reader io.Reader, root string) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := archivePath(root, h.Name)
		if err != nil {
			return err
		}
		if target == root && h.Typeflag == tar.TypeDir {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("directory is a symlink: %s", h.Name)
			}
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := extractFile(target, tr, os.FileMode(h.Mode)&0777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("absolute archive symlink %q", h.Linkname)
			}
			rel := filepath.Join(filepath.Dir(h.Name), h.Linkname)
			if _, err := archivePath(root, rel); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			source, err := archivePath(root, h.Linkname)
			if err != nil {
				return err
			}
			info, err := os.Lstat(source)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("hardlink source is not a regular file")
			}
			if err := os.Link(source, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry type %d: %s", h.Typeflag, h.Name)
		}
	}
}

func extractFile(name string, src io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func extractZip(name, root string) error {
	z, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, entry := range z.File {
		target, err := archivePath(root, entry.Name)
		if err != nil {
			return err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip symlinks are unsupported")
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		err = extractFile(target, reader, entry.Mode().Perm())
		reader.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *manager) archive(url, checksum, parent string) (string, error) {
	if !validChecksum(checksum) {
		return "", fmt.Errorf("invalid SHA-256 checksum")
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, ".download-*")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	name := filepath.Join(dir, "archive")
	f, err := os.Create(name)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	err = m.download(url, io.MultiWriter(f, hash))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksum) {
		return "", fmt.Errorf("SHA-256 mismatch for %s", url)
	}
	destination := filepath.Join(dir, "contents")
	if err := os.Mkdir(destination, 0755); err != nil {
		return "", err
	}
	if strings.HasSuffix(url, ".zip") {
		err = extractZip(name, destination)
	} else {
		reader, openErr := os.Open(name)
		if openErr != nil {
			return "", openErr
		}
		err = extractTar(reader, destination)
		reader.Close()
	}
	if err != nil {
		return "", err
	}
	ok = true
	return dir, nil
}

func (m *manager) json(url string, value any) error {
	data, err := m.fetch(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
