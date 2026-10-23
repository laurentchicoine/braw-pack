package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// FFmpeg writes larger attachments but its Matroska reader refuses binary
// elements over 256 MiB. Leave ample headroom for every restore attachment.
const restorePartSize int64 = 128 << 20
const restorePartsMagic = "BRAWPACK-PARTS\x01\n"
const maxRestoreParts = 4096

type restoreParts struct {
	Parts  int    `json:"parts"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Small archives retain the original single-attachment format. Large archives
// carry a manifest followed by ordered pieces of the unchanged restore stream.
func restoreAttachments(src string) (args []string, cleanup func(), err error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, func() {}, err
	}
	attach := func(file, name string, index int) {
		spec := "-metadata:s:t:" + strconv.Itoa(index)
		args = append(args, "-attach", file, spec, "mimetype=application/octet-stream", spec, "filename="+name)
	}
	if st.Size() <= restorePartSize {
		attach(src, AttachmentName, 0)
		return args, func() {}, nil
	}
	dir, err := os.MkdirTemp(filepath.Dir(src), "braw-pack-parts-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	in, err := os.Open(src)
	if err != nil {
		return nil, cleanup, err
	}
	defer in.Close()
	count := (st.Size()-1)/restorePartSize + 1
	if count > maxRestoreParts {
		return nil, cleanup, fmt.Errorf("restore data needs too many attachments: %d", count)
	}
	manifest := filepath.Join(dir, "manifest.bin")
	attach(manifest, "braw-pack-restore-parts.bin", 0)
	hash := sha256.New()
	for i := 0; i < int(count); i++ {
		name := fmt.Sprintf("braw-pack-restore-%04d.bin", i+1)
		file := filepath.Join(dir, name)
		out, e := os.Create(file)
		if e != nil {
			return nil, cleanup, e
		}
		n := restorePartBytes(st.Size(), i)
		_, e = io.CopyN(io.MultiWriter(out, hash), in, n)
		closeErr := out.Close()
		if e != nil {
			return nil, cleanup, e
		}
		if closeErr != nil {
			return nil, cleanup, closeErr
		}
		attach(file, name, i+1)
	}
	m := restoreParts{int(count), st.Size(), hex.EncodeToString(hash.Sum(nil))}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, cleanup, err
	}
	err = os.WriteFile(manifest, append([]byte(restorePartsMagic), b...), 0600)
	return args, cleanup, err
}

// FFmpeg's attachment dump exits nonzero because there is no media output.
// Callers validate the extracted bytes instead of trusting that exit status.
func dumpAttachments(ffmpeg, mkv string, paths []string, first int) string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	for i, p := range paths {
		args = append(args, "-dump_attachment:t:"+strconv.Itoa(first+i), p)
	}
	cmd := command(ffmpeg, append(args, "-i", mkv)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return stderr.String()
}

func joinRestoreParts(ffmpeg, mkv, first, dir string) (string, error) {
	f, err := os.Open(first)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	f.Close()
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(b, []byte(restorePartsMagic)) {
		return first, nil
	}
	var m restoreParts
	if len(b) > 16384 {
		return "", fmt.Errorf("restore attachment manifest is too large")
	}
	if err := json.Unmarshal(b[len(restorePartsMagic):], &m); err != nil {
		return "", fmt.Errorf("invalid restore attachment manifest: %w", err)
	}
	if m.Parts < 2 || m.Parts > maxRestoreParts || m.Size <= restorePartSize ||
		m.Size > int64(m.Parts)*restorePartSize || m.Size <= int64(m.Parts-1)*restorePartSize {
		return "", fmt.Errorf("invalid restore attachment count or size")
	}
	if sum, err := hex.DecodeString(m.SHA256); err != nil || len(sum) != sha256.Size {
		return "", fmt.Errorf("invalid restore attachment checksum")
	}
	paths := make([]string, m.Parts)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("part-%04d.bin", i))
	}
	diagnostics := dumpAttachments(ffmpeg, mkv, paths, 1)
	joined := filepath.Join(dir, "restore.bin")
	out, err := os.Create(joined)
	if err != nil {
		return "", err
	}
	defer out.Close()
	hash := sha256.New()
	for i, p := range paths {
		in, err := os.Open(p)
		if err != nil {
			return "", fmt.Errorf("missing restore attachment %d: %w; %s", i+1, err, diagnostics)
		}
		st, err := in.Stat()
		want := restorePartBytes(m.Size, i)
		if err != nil || st.Size() != want {
			in.Close()
			return "", fmt.Errorf("restore attachment %d has an incorrect size", i+1)
		}
		_, err = io.Copy(io.MultiWriter(out, hash), in)
		in.Close()
		if err != nil {
			return "", err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.SHA256 {
		return "", fmt.Errorf("restore attachment checksum mismatch")
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return joined, nil
}

func restorePartBytes(size int64, index int) int64 {
	n := size - int64(index)*restorePartSize
	if n > restorePartSize {
		return restorePartSize
	}
	return n
}
