package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Store struct {
	BaseDir string
}

func New(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{BaseDir: baseDir}, nil
}

var allowedExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
	".pdf": true, ".heic": true, ".heif": true,
}

const maxBytes = 15 << 20 // 15 MB

func rand8() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Save writes the multipart file under <base>/<subdir>/<rand>_<basename>.
// Returns the RELATIVE path (not absolute), suitable for storing in DB.
func (s *Store) Save(fh *multipart.FileHeader, subdir string) (string, error) {
	if fh.Size <= 0 || fh.Size > maxBytes {
		return "", errors.New("file size out of range")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedExt[ext] {
		return "", errors.New("unsupported file type")
	}
	// Keep subdir simple: no path traversal.
	subdir = strings.Trim(path.Clean("/"+subdir), "/")
	dir := filepath.Join(s.BaseDir, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safeName := rand8() + ext
	rel := path.Join(subdir, safeName)
	full := filepath.Join(s.BaseDir, filepath.FromSlash(rel))

	in, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(full)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	return rel, nil
}

// AbsPath resolves a stored relative path to an absolute path for serving.
func (s *Store) AbsPath(rel string) (string, error) {
	rel = strings.Trim(path.Clean("/"+rel), "/")
	full := filepath.Join(s.BaseDir, filepath.FromSlash(rel))
	// Prevent escape.
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	base, _ := filepath.Abs(s.BaseDir)
	if !strings.HasPrefix(abs, base) {
		return "", fmt.Errorf("invalid path")
	}
	return abs, nil
}
