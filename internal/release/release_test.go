package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func mustVersion(t *testing.T, tag string) Version {
	t.Helper()
	v, err := ParseVersion(tag)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", tag, err)
	}
	return v
}

func TestParseVersion(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "v0.0.1", "v1.2.3-rc.1", "v1.2.3-alpha"} {
		v, err := ParseVersion(tag)
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", tag, err)
		} else if v.String() != tag {
			t.Errorf("String() = %q, want %q", v.String(), tag)
		}
	}
	for _, tag := range []string{"1.2.3", "v1.2", "v1.2.3+b", "v1.2.3-rc.1+b", "dev", "", "v1.2.3-", "v1.2.3 ", "v99999999999999999999.0.0"} {
		if _, err := ParseVersion(tag); err == nil {
			t.Errorf("ParseVersion(%q) succeeded", tag)
		}
	}
}

func TestCompareOrder(t *testing.T) {
	order := []string{
		"v1.2.3-alpha", "v1.2.3-alpha.1", "v1.2.3-alpha.beta", "v1.2.3-beta", "v1.2.3-beta.2",
		"v1.2.3-beta.11", "v1.2.3-rc.1", "v1.2.3", "v1.2.4", "v1.10.0", "v2.0.0",
	}
	for i, a := range order {
		va := mustVersion(t, a)
		if c := Compare(va, va); c != 0 {
			t.Errorf("Compare(%s, %s) = %d", a, a, c)
		}
		for _, b := range order[i+1:] {
			vb := mustVersion(t, b)
			if Compare(va, vb) != -1 || Compare(vb, va) != 1 {
				t.Errorf("want %s < %s", a, b)
			}
		}
	}
	if Compare(mustVersion(t, "v1.0.0-1"), mustVersion(t, "v1.0.0-01")) != 0 {
		t.Error("numeric identifiers with leading zeros should compare equal")
	}
}

func TestArchiveName(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "ralph_v1.2.3_linux_amd64.tar.gz",
		{"darwin", "arm64"}:  "ralph_v1.2.3_darwin_arm64.tar.gz",
		{"windows", "arm64"}: "ralph_v1.2.3_windows_arm64.zip",
	}
	for p, want := range cases {
		got, err := ArchiveName("v1.2.3", p[0], p[1])
		if err != nil || got != want {
			t.Errorf("ArchiveName(%v) = %q, %v; want %q", p, got, err, want)
		}
	}
	for _, p := range [][2]string{{"freebsd", "amd64"}, {"linux", "386"}} {
		_, err := ArchiveName("v1.2.3", p[0], p[1])
		if err == nil || !strings.Contains(err.Error(), p[0]+"/"+p[1]) {
			t.Errorf("ArchiveName(%v) error = %v", p, err)
		}
	}
	if _, err := ArchiveName("dev", "linux", "amd64"); err == nil {
		t.Error("ArchiveName accepted a non-release tag")
	}
}

const releaseFixture = `{"tag_name":"v1.2.3","draft":false,"prerelease":false,"assets":[
 {"name":"ralph_v1.2.3_linux_amd64.tar.gz","browser_download_url":"https://example.test/a.tar.gz","size":42},
 {"name":"checksums.txt","browser_download_url":"https://example.test/checksums.txt","size":7}]}`

func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	for k, want := range map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "ralph",
	} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("request carries credentials")
	}
}

func TestLatestAndByTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		switch r.URL.Path {
		case "/repos/bengearig/ralph-cli/releases/latest", "/repos/bengearig/ralph-cli/releases/tags/v1.2.3":
			fmt.Fprint(w, releaseFixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL+"/", nil)

	for name, get := range map[string]func() (Release, error){
		"Latest": func() (Release, error) { return c.Latest(context.Background()) },
		"ByTag":  func() (Release, error) { return c.ByTag(context.Background(), "v1.2.3") },
	} {
		rel, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rel.Tag != "v1.2.3" || rel.Prerelease || len(rel.Assets) != 2 {
			t.Fatalf("%s: unexpected release %+v", name, rel)
		}
		a, ok := rel.Asset("ralph_v1.2.3_linux_amd64.tar.gz")
		if !ok || a.URL != "https://example.test/a.tar.gz" || a.Size != 42 {
			t.Errorf("%s: asset = %+v, %v", name, a, ok)
		}
		if _, ok := rel.Asset("nope"); ok {
			t.Errorf("%s: found a missing asset", name)
		}
	}

	if _, err := c.ByTag(context.Background(), "v9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByTag missing = %v, want ErrNotFound", err)
	}
}

func TestStatusErrors(t *testing.T) {
	reset := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    []string
		notFnd  bool
	}{
		{"404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, nil, true},
		{"403 rate limit", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
			w.WriteHeader(http.StatusForbidden)
		}, []string{"rate limit", "403", "2030-01-02T03:04:05Z"}, false},
		{"429", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		}, []string{"rate limit", "429", "resets at"}, false},
		{"403 other", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "10")
			w.WriteHeader(http.StatusForbidden)
		}, []string{"403"}, false},
		{"500", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "boom")
		}, []string{"500", "boom"}, false},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"tag_name":"`+strings.Repeat("a", maxJSONBody)+`"}`)
		}, []string{"exceeds"}, false},
		{"bad json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "{") }, []string{"decode"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := NewClient(srv.URL, nil).Latest(context.Background())
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrNotFound) != tc.notFnd {
				t.Errorf("ErrNotFound match = %v, error %v", !tc.notFnd, err)
			}
			if tc.name == "403 other" && strings.Contains(err.Error(), "rate limit") {
				t.Errorf("non-rate-limit 403 reported as rate limit: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewClient(srv.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Latest(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Latest error = %v, want deadline exceeded", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := c.Download(ctx2, srv.URL+"/x", &bytes.Buffer{}, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Download error = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancellation was not prompt")
	}
}

func TestNewest(t *testing.T) {
	const list = `[
 {"tag_name":"v3.0.0","draft":true,"prerelease":false},
 {"tag_name":"v2.0.0-rc.1","draft":false,"prerelease":true},
 {"tag_name":"v1.9.0","draft":false,"prerelease":false},
 {"tag_name":"v1.10.0","draft":false,"prerelease":false},
 {"tag_name":"nightly","draft":false,"prerelease":false}]`
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		query = r.URL.RawQuery
		if r.URL.Path != "/repos/bengearig/ralph-cli/releases" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, list)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	if r, err := c.Newest(context.Background(), false); err != nil || r.Tag != "v1.10.0" {
		t.Errorf("Newest(false) = %q, %v", r.Tag, err)
	}
	if query != "per_page=100" {
		t.Errorf("query = %q", query)
	}
	if r, err := c.Newest(context.Background(), true); err != nil || r.Tag != "v2.0.0-rc.1" {
		t.Errorf("Newest(true) = %q, %v", r.Tag, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.0.0","draft":true},{"tag_name":"nightly"},{"tag_name":"v1.1.0-rc.1","prerelease":true}]`)
	}))
	defer empty.Close()
	if _, err := NewClient(empty.URL, nil).Newest(context.Background(), false); !errors.Is(err, ErrNotFound) {
		t.Errorf("Newest with nothing eligible = %v, want ErrNotFound", err)
	}
}

func TestDownload(t *testing.T) {
	body := bytes.Repeat([]byte("ralph"), 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/nocontent" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	want := sha256.Sum256(body)
	var buf bytes.Buffer
	sum, err := c.Download(context.Background(), srv.URL+"/a", &buf, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if sum != hex.EncodeToString(want[:]) || !bytes.Equal(buf.Bytes(), body) {
		t.Errorf("sum = %s, copied %d bytes", sum, buf.Len())
	}

	buf.Reset()
	if _, err := c.Download(context.Background(), srv.URL+"/a", &buf, int64(len(body))-1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("one byte over max: %v", err)
	}
	if int64(buf.Len()) > int64(len(body))-1 {
		t.Errorf("wrote %d bytes past the cap", buf.Len())
	}
	if _, err := c.Download(context.Background(), srv.URL+"/missing", &buf, 1<<20); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 = %v", err)
	}
	if _, err := c.Download(context.Background(), srv.URL+"/nocontent", &buf, 1<<20); err == nil {
		t.Error("204 accepted")
	}
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestParseChecksums(t *testing.T) {
	a, b := hashOf("a"), hashOf("b")
	good := a + "  ralph_v1.2.3_linux_amd64.tar.gz\n" + b + "  ralph_v1.2.3_windows_amd64.zip\n"
	sums, err := ParseChecksums(strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums["ralph_v1.2.3_linux_amd64.tar.gz"] != a || sums["ralph_v1.2.3_windows_amd64.zip"] != b {
		t.Errorf("sums = %v", sums)
	}

	bad := map[string]string{
		"one space":   a + " file\n",
		"three space": a + "   file\n",
		"uppercase":   strings.ToUpper(a) + "  file\n",
		"short hash":  a[:63] + "  file\n",
		"path":        a + "  dir/file\n",
		"backslash":   a + "  dir\\file\n",
		"duplicate":   a + "  file\n" + b + "  file\n",
		"empty":       "",
		"blank only":  "\n\n",
		"no name":     a + "  \n",
		"crlf":        a + "  file\r\n",
		"non-hex":     strings.Repeat("g", 64) + "  file\n",
	}
	for name, in := range bad {
		if _, err := ParseChecksums(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerify(t *testing.T) {
	sums := map[string]string{"f": hashOf("x")}
	if err := Verify(sums, "f", hashOf("x")); err != nil {
		t.Errorf("good: %v", err)
	}
	if err := Verify(sums, "f", hashOf("y")); err == nil || err.Error() != "checksum mismatch for f" {
		t.Errorf("mismatch: %v", err)
	}
	if err := Verify(sums, "g", hashOf("x")); err == nil || err.Error() != "no checksum listed for g" {
		t.Errorf("missing: %v", err)
	}
}

type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

func makeTarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: flag, Mode: 0o755, Linkname: e.link}
		if flag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.typeflag == tar.TypeSymlink {
			h.SetMode(os.ModeSymlink | 0o777)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func extract(data []byte, goos string, max int64) (string, error) {
	var out bytes.Buffer
	err := ExtractBinary(bytes.NewReader(data), int64(len(data)), goos, &out, max)
	return out.String(), err
}

func TestExtractBinary(t *testing.T) {
	payload := strings.Repeat("\x00binary\xff", 500)

	if got, err := extract(makeTarGz(t, entry{name: "ralph", body: payload}), "linux", 1<<20); err != nil || got != payload {
		t.Errorf("tar.gz: err=%v, match=%v", err, got == payload)
	}
	if got, err := extract(makeZip(t, entry{name: "ralph.exe", body: payload}), "windows", 1<<20); err != nil || got != payload {
		t.Errorf("zip: err=%v, match=%v", err, got == payload)
	}
	if got, err := extract(makeTarGz(t, entry{name: "ralph", body: payload}), "darwin", int64(len(payload))); err != nil || got != payload {
		t.Errorf("exact cap: err=%v", err)
	}

	tarBad := map[string][]entry{
		"extra file":     {{name: "ralph", body: "x"}, {name: "README", body: "y"}},
		"missing":        {{name: "other", body: "x"}},
		"empty archive":  {},
		"dotdot":         {{name: "../ralph", body: "x"}},
		"dotdot only":    {{name: "..", body: "x"}},
		"subdirectory":   {{name: "dir/ralph", body: "x"}},
		"dot prefix":     {{name: "./ralph", body: "x"}},
		"symlink member": {{name: "ralph", typeflag: tar.TypeSymlink, link: "/bin/sh"}},
		"symlink extra":  {{name: "ralph", body: "x"}, {name: "other", typeflag: tar.TypeSymlink, link: "ralph"}},
		"hardlink":       {{name: "ralph", typeflag: tar.TypeLink, link: "other"}},
		"duplicate":      {{name: "ralph", body: "x"}, {name: "ralph", body: "y"}},
		"wrong os name":  {{name: "ralph.exe", body: "x"}},
	}
	for name, es := range tarBad {
		if _, err := extract(makeTarGz(t, es...), "linux", 1<<20); err == nil {
			t.Errorf("tar.gz %s: accepted", name)
		}
	}
	if _, err := extract(makeTarGz(t, entry{name: "ralph", body: payload}), "linux", int64(len(payload))-1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("tar.gz over cap: %v", err)
	}
	if _, err := extract([]byte("not an archive"), "linux", 10); err == nil {
		t.Error("garbage tar.gz accepted")
	}

	zipBad := map[string][]entry{
		"extra file":     {{name: "ralph.exe", body: "x"}, {name: "README", body: "y"}},
		"missing":        {{name: "other.exe", body: "x"}},
		"dotdot":         {{name: "../ralph.exe", body: "x"}},
		"subdirectory":   {{name: "dir/ralph.exe", body: "x"}},
		"backslash":      {{name: "dir\\ralph.exe", body: "x"}},
		"symlink member": {{name: "ralph.exe", body: "target", typeflag: tar.TypeSymlink}},
		"duplicate":      {{name: "ralph.exe", body: "x"}, {name: "ralph.exe", body: "y"}},
	}
	for name, es := range zipBad {
		if _, err := extract(makeZip(t, es...), "windows", 1<<20); err == nil {
			t.Errorf("zip %s: accepted", name)
		}
	}
	if _, err := extract(makeZip(t, entry{name: "ralph.exe", body: payload}), "windows", int64(len(payload))-1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("zip over cap: %v", err)
	}
	if _, err := extract([]byte("not an archive"), "windows", 10); err == nil {
		t.Error("garbage zip accepted")
	}
}
