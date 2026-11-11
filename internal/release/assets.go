package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const maxChecksumsFile = 1 << 20

// ArchiveName returns the release archive name for a platform.
func ArchiveName(tag, goos, goarch string) (string, error) {
	if _, err := ParseVersion(tag); err != nil {
		return "", err
	}
	switch goos {
	case "linux", "darwin", "windows":
	default:
		return "", fmt.Errorf("no release archive for %s/%s", goos, goarch)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("no release archive for %s/%s", goos, goarch)
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "ralph_" + tag + "_" + goos + "_" + goarch + ext, nil
}

// ParseChecksums parses a sha256sum-format file strictly into a map from file
// name to lowercase hex digest.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxChecksumsFile+1))
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	if len(data) > maxChecksumsFile {
		return nil, fmt.Errorf("checksums file exceeds %d bytes", maxChecksumsFile)
	}
	sums := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		n := i + 1
		if len(line) < 67 || line[64:66] != "  " {
			return nil, fmt.Errorf("checksums line %d: want a 64-character hash, two spaces, and a file name", n)
		}
		sum, name := line[:64], line[66:]
		if !isLowerHex(sum) {
			return nil, fmt.Errorf("checksums line %d: hash is not lowercase hex", n)
		}
		if name[0] == ' ' || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return nil, fmt.Errorf("checksums line %d: invalid file name %q", n, name)
		}
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("checksums line %d: duplicate entry for %s", n, name)
		}
		sums[name] = sum
	}
	if len(sums) == 0 {
		return nil, errors.New("checksums file lists no files")
	}
	return sums, nil
}

func isLowerHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	return s == strings.ToLower(s)
}

// Verify checks that got is the digest listed for name.
func Verify(sums map[string]string, name, got string) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("no checksum listed for %s", name)
	}
	if want != got {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

// ExtractBinary copies the single member named ralph (ralph.exe for windows)
// out of a .tar.gz (.zip for windows) archive into dst.
func ExtractBinary(archive io.ReaderAt, size int64, goos string, dst io.Writer, max int64) error {
	member := "ralph"
	if goos == "windows" {
		member = "ralph.exe"
		return extractZip(archive, size, member, dst, max)
	}
	return extractTarGz(archive, size, member, dst, max)
}

func checkMemberName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return fmt.Errorf("unsafe archive member name %q", name)
	}
	return nil
}

func copyMember(dst io.Writer, src io.Reader, name string, max int64) error {
	if _, err := io.Copy(dst, io.LimitReader(src, max)); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	var probe [1]byte
	n, err := src.Read(probe[:])
	if n > 0 {
		return fmt.Errorf("archive member %s exceeds %d bytes", name, max)
	}
	if err != nil && err != io.EOF {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	return nil
}

func extractTarGz(archive io.ReaderAt, size int64, member string, dst io.Writer, max int64) error {
	gz, err := gzip.NewReader(io.NewSectionReader(archive, 0, size))
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if err := checkMemberName(hdr.Name); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive entry %s is a link", hdr.Name)
		case tar.TypeReg:
		default:
			continue
		}
		if hdr.Name != member {
			return fmt.Errorf("unexpected file %s in archive", hdr.Name)
		}
		if found {
			return fmt.Errorf("archive contains %s more than once", member)
		}
		if hdr.Size > max {
			return fmt.Errorf("archive member %s exceeds %d bytes", member, max)
		}
		if err := copyMember(dst, tr, member, max); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("archive has no %s", member)
	}
	return nil
}

func extractZip(archive io.ReaderAt, size int64, member string, dst io.Writer, max int64) error {
	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	found := false
	for _, f := range zr.File {
		if err := checkMemberName(f.Name); err != nil {
			return err
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %s is a link", f.Name)
		}
		if !mode.IsRegular() {
			continue
		}
		if f.Name != member {
			return fmt.Errorf("unexpected file %s in archive", f.Name)
		}
		if found {
			return fmt.Errorf("archive contains %s more than once", member)
		}
		if f.UncompressedSize64 > uint64(max) {
			return fmt.Errorf("archive member %s exceeds %d bytes", member, max)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", member, err)
		}
		err = copyMember(dst, rc, member, max)
		rc.Close()
		if err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("archive has no %s", member)
	}
	return nil
}
