package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/release"
)

var fakeBinary = []byte("#!/bin/sh\necho fake ralph\n")

const oldBinary = "old ralph"

func buildArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("ralph.exe")
		if err != nil {
			t.Fatal(err)
		}
		w.Write(content)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ralph", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(content)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeRelease struct {
	tag        string
	body       string
	prerelease bool
	noArchive  bool
	noSums     bool
	badSum     bool
	sumsOther  bool
	archive    func(w http.ResponseWriter, r *http.Request, body []byte)
}

type fakeAPI struct {
	srv          *httptest.Server
	listHits     atomic.Int32
	listStatus   int
	releases     []fakeRelease
	latest       string
	assetHits    atomic.Int32
	archiveBytes []byte
}

func newFakeAPI(t *testing.T, latest string, releases ...fakeRelease) *fakeAPI {
	t.Helper()
	f := &fakeAPI{latest: latest, releases: releases, archiveBytes: buildArchive(t, fakeBinary)}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/bengearig/ralph-cli/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if f.latest == "" {
			http.NotFound(w, r)
			return
		}
		f.serveRelease(w, r, f.latest)
	})
	mux.HandleFunc("/repos/bengearig/ralph-cli/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		f.serveRelease(w, r, strings.TrimPrefix(r.URL.Path, "/repos/bengearig/ralph-cli/releases/tags/"))
	})
	mux.HandleFunc("/repos/bengearig/ralph-cli/releases", func(w http.ResponseWriter, r *http.Request) {
		f.listHits.Add(1)
		if f.listStatus != 0 {
			http.Error(w, "boom", f.listStatus)
			return
		}
		var list []any
		for _, fr := range f.releases {
			list = append(list, f.releaseJSON(fr))
		}
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		f.assetHits.Add(1)
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/assets/"), "/")
		fr, ok := f.find(parts[0])
		if !ok {
			http.NotFound(w, r)
			return
		}
		if parts[1] == "checksums.txt" {
			name := f.archiveName(fr)
			if fr.sumsOther {
				name = "something_else.tar.gz"
			}
			sum := sha256.Sum256(f.archiveBytes)
			h := hex.EncodeToString(sum[:])
			if fr.badSum {
				h = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  %s\n", h, name)
			return
		}
		if fr.archive != nil {
			fr.archive(w, r, f.archiveBytes)
			return
		}
		w.Write(f.archiveBytes)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	prev := releaseAPIURL
	releaseAPIURL = f.srv.URL
	t.Cleanup(func() { releaseAPIURL = prev })
	return f
}

func (f *fakeAPI) find(tag string) (fakeRelease, bool) {
	for _, fr := range f.releases {
		if fr.tag == tag {
			return fr, true
		}
	}
	return fakeRelease{}, false
}

func (f *fakeAPI) archiveName(fr fakeRelease) string {
	n, _ := release.ArchiveName(fr.tag, runtime.GOOS, runtime.GOARCH)
	return n
}

func (f *fakeAPI) releaseJSON(fr fakeRelease) map[string]any {
	var assets []map[string]any
	if !fr.noArchive {
		assets = append(assets, map[string]any{"name": f.archiveName(fr), "browser_download_url": f.srv.URL + "/assets/" + fr.tag + "/" + f.archiveName(fr)})
	}
	if !fr.noSums {
		assets = append(assets, map[string]any{"name": "checksums.txt", "browser_download_url": f.srv.URL + "/assets/" + fr.tag + "/checksums.txt"})
	}
	return map[string]any{"tag_name": fr.tag, "prerelease": fr.prerelease, "body": fr.body, "assets": assets}
}

func (f *fakeAPI) serveRelease(w http.ResponseWriter, r *http.Request, tag string) {
	fr, ok := f.find(tag)
	if !ok {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(f.releaseJSON(fr))
}

func stubUpdate(t *testing.T, running string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ralph")
	if err := os.WriteFile(exe, []byte(oldBinary), 0o750); err != nil {
		t.Fatal(err)
	}
	prevExe, prevVer, prevClient := updateExecutable, updateVersion, updateHTTPClient
	updateExecutable = func() (string, error) { return exe, nil }
	updateVersion = func() string { return running }
	updateHTTPClient = &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(func() { updateExecutable, updateVersion, updateHTTPClient = prevExe, prevVer, prevClient })
	return exe
}

func assertUntouched(t *testing.T, exe string) {
	t.Helper()
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != oldBinary {
		t.Errorf("executable changed: %q", got)
	}
	assertNoStaged(t, exe)
}

func assertNoStaged(t *testing.T, exe string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".update-") {
			t.Errorf("staged file left behind: %s", e.Name())
		}
	}
}

func assertInstalled(t *testing.T, exe string) {
	t.Helper()
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fakeBinary) {
		t.Errorf("executable = %q, want the fake binary", got)
	}
	assertNoStaged(t, exe)
}

func TestUpdateInstalls(t *testing.T) {
	newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
	exe := stubUpdate(t, "v1.0.0")
	h := newTestEnv(t, false)
	out := h.ok("update")
	assertInstalled(t, exe)
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(exe)
		if info.Mode().Perm() != 0o750 {
			t.Errorf("mode = %v, want 0750", info.Mode().Perm())
		}
	}
	for _, want := range []string{"updating ralph v1.0.0 -> v1.1.0", "installed " + exe, "ralph init"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dir, ".ralph")); !os.IsNotExist(err) {
		t.Errorf(".ralph created or unexpected error: %v", err)
	}
}

func TestUpdateJSON(t *testing.T) {
	newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
	exe := stubUpdate(t, "v1.0.0")
	h := newTestEnv(t, false)
	out := h.ok("--json", "update")
	assertInstalled(t, exe)
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not only JSON: %q: %v", out, err)
	}
	want := map[string]string{"previous": "v1.0.0", "installed": "v1.1.0", "path": exe}
	if len(got) != len(want)+1 {
		t.Errorf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %q", k, got[k], v)
		}
	}
	if fmt.Sprint(got["changelog"]) != "[map[changes:[] version:v1.1.0]]" {
		t.Errorf("changelog = %v", got["changelog"])
	}
}

func TestUpdateCheck(t *testing.T) {
	type result struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		UpdateAvailable bool   `json:"update_available"`
		ReleaseBuild    bool   `json:"release_build"`
	}
	cases := []struct {
		running string
		want    result
		text    string
	}{
		{"v1.0.0", result{"v1.0.0", "v1.1.0", true, true}, "an update is available"},
		{"v1.1.0", result{"v1.1.0", "v1.1.0", false, true}, "ralph is up to date"},
		{"v2.0.0", result{"v2.0.0", "v1.1.0", false, true}, "ralph is up to date"},
		{"dev", result{"dev", "v1.1.0", false, false}, "this build is not a release build"},
	}
	for _, tc := range cases {
		t.Run(tc.running, func(t *testing.T) {
			api := newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
			exe := stubUpdate(t, tc.running)
			h := newTestEnv(t, false)
			var got result
			h.json(&got, "update", "--check")
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if out := h.ok("update", "--check"); !strings.Contains(out, tc.text) || !strings.Contains(out, "latest v1.1.0") {
				t.Errorf("text output:\n%s", out)
			}
			assertUntouched(t, exe)
			if api.assetHits.Load() != 0 {
				t.Error("--check downloaded assets")
			}
		})
	}
}

func TestUpdateAlreadyCurrentAndNewer(t *testing.T) {
	t.Run("same", func(t *testing.T) {
		newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		exe := stubUpdate(t, "v1.1.0")
		h := newTestEnv(t, false)
		if out := h.ok("update"); !strings.Contains(out, "ralph v1.1.0 is already the latest version") {
			t.Errorf("output: %s", out)
		}
		assertUntouched(t, exe)
		if out := h.ok("update", "--version", "v1.1.0"); !strings.Contains(out, "already at v1.1.0") {
			t.Errorf("output: %s", out)
		}
		assertUntouched(t, exe)
	})
	t.Run("force", func(t *testing.T) {
		newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		exe := stubUpdate(t, "v1.1.0")
		newTestEnv(t, false).ok("update", "--force")
		assertInstalled(t, exe)
	})
	t.Run("newer", func(t *testing.T) {
		newFakeAPI(t, "v1.9.0", fakeRelease{tag: "v1.9.0"})
		exe := stubUpdate(t, "v2.0.0")
		h := newTestEnv(t, false)
		if out := h.ok("update"); !strings.Contains(out, "ralph v2.0.0 is newer than the latest release v1.9.0; nothing to do") {
			t.Errorf("output: %s", out)
		}
		assertUntouched(t, exe)
		out := h.ok("update", "--version", "v1.9.0")
		if !strings.Contains(out, "downgrading ralph v2.0.0 -> v1.9.0") {
			t.Errorf("output: %s", out)
		}
		assertInstalled(t, exe)
	})
}

func TestUpdateVersionAndPre(t *testing.T) {
	releases := []fakeRelease{
		{tag: "v1.0.0"},
		{tag: "v1.1.0-rc.1", prerelease: true},
		{tag: "v1.1.0-rc.2", prerelease: true},
	}
	t.Run("version pre-release", func(t *testing.T) {
		newFakeAPI(t, "v1.0.0", releases...)
		exe := stubUpdate(t, "v0.9.0")
		out := newTestEnv(t, false).ok("update", "--version", "v1.1.0-rc.1")
		if !strings.Contains(out, "v0.9.0 -> v1.1.0-rc.1") {
			t.Errorf("output: %s", out)
		}
		assertInstalled(t, exe)
	})
	t.Run("pre", func(t *testing.T) {
		newFakeAPI(t, "v1.0.0", releases...)
		exe := stubUpdate(t, "v0.9.0")
		out := newTestEnv(t, false).ok("update", "--pre")
		if !strings.Contains(out, "v0.9.0 -> v1.1.0-rc.2") {
			t.Errorf("output: %s", out)
		}
		assertInstalled(t, exe)
	})
	t.Run("both", func(t *testing.T) {
		newFakeAPI(t, "v1.0.0", releases...)
		exe := stubUpdate(t, "v0.9.0")
		_, _, err := newTestEnv(t, false).run("", "update", "--version", "v1.0.0", "--pre")
		if err == nil || err.Error() != "--version and --pre cannot be used together" {
			t.Errorf("err = %v", err)
		}
		assertUntouched(t, exe)
	})
	t.Run("invalid", func(t *testing.T) {
		newFakeAPI(t, "v1.0.0", releases...)
		exe := stubUpdate(t, "v0.9.0")
		_, _, err := newTestEnv(t, false).run("", "update", "--version", "1.2.3")
		want := `invalid version "1.2.3": expected a tag such as v1.2.3`
		if err == nil || err.Error() != want {
			t.Errorf("err = %v, want %s", err, want)
		}
		assertUntouched(t, exe)
	})
}

func TestUpdateDevBuild(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		api := newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		exe := stubUpdate(t, "dev")
		_, _, err := newTestEnv(t, false).run("", "update")
		want := `this ralph is not a release build (version "dev"); install a release with the install script, or pass --force`
		if err == nil || err.Error() != want {
			t.Errorf("err = %v", err)
		}
		if api.assetHits.Load() != 0 {
			t.Error("assets were requested")
		}
		assertUntouched(t, exe)
	})
	t.Run("force", func(t *testing.T) {
		newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		exe := stubUpdate(t, "dev")
		out := newTestEnv(t, false).ok("update", "--force")
		if !strings.Contains(out, "updating ralph dev -> v1.1.0") {
			t.Errorf("output: %s", out)
		}
		assertInstalled(t, exe)
	})
}

func TestUpdateFailures(t *testing.T) {
	hangUp := func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}
	cases := []struct {
		name    string
		rel     fakeRelease
		latest  string
		wantErr string
	}{
		{"checksum mismatch", fakeRelease{tag: "v1.1.0", badSum: true}, "v1.1.0", "checksum mismatch for ralph_v1.1.0_"},
		{"missing checksum line", fakeRelease{tag: "v1.1.0", sumsOther: true}, "v1.1.0", "no checksum listed for ralph_v1.1.0_"},
		{"no platform asset", fakeRelease{tag: "v1.1.0", noArchive: true}, "v1.1.0", "no release asset for " + runtime.GOOS + "/" + runtime.GOARCH},
		{"no checksums asset", fakeRelease{tag: "v1.1.0", noSums: true}, "v1.1.0", "release v1.1.0 has no checksums.txt"},
		{"no release", fakeRelease{tag: "v1.1.0"}, "", "no release found"},
		{"hang up", fakeRelease{tag: "v1.1.0", archive: hangUp}, "v1.1.0", "download"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFakeAPI(t, tc.latest, tc.rel)
			exe := stubUpdate(t, "v1.0.0")
			_, _, err := newTestEnv(t, false).run("", "update")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tc.wantErr)
			}
			assertUntouched(t, exe)
		})
	}

	t.Run("unknown tag", func(t *testing.T) {
		newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		stubUpdate(t, "v1.0.0")
		_, _, err := newTestEnv(t, false).run("", "update", "--version", "v9.9.9")
		if err == nil || err.Error() != "no release found v9.9.9" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("read-only directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: directory permissions are not enforced")
		}
		api := newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		exe := stubUpdate(t, "v1.0.0")
		dir := filepath.Dir(exe)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		_, _, err := newTestEnv(t, false).run("", "update")
		want := "cannot update " + exe + ": " + dir + " is not writable; reinstall with the install script and --dir, or run ralph with permission to write there"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v", err)
		}
		if strings.Contains(err.Error(), "sudo") {
			t.Error("message suggests sudo")
		}
		if api.assetHits.Load() != 0 {
			t.Error("assets were requested")
		}
		assertUntouched(t, exe)
	})
}

func TestUpdateInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	stall := func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}
	newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0", archive: stall})
	exe := stubUpdate(t, "v1.0.0")
	go func() {
		<-started
		cancel()
	}()
	h := newTestEnv(t, false)
	var out, errOut bytes.Buffer
	err := Execute(ctx, Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{"--project-dir", h.dir, "update"})
	if !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
	assertUntouched(t, exe)
}

func TestUpdateHelpAndRegistration(t *testing.T) {
	h := newTestEnv(t, false)
	out := h.ok("update", "--help")
	for _, flag := range []string{"--check", "--version", "--pre", "--force"} {
		if !strings.Contains(out, flag) {
			t.Errorf("update --help lacks %s", flag)
		}
	}
	if out := h.ok("--help"); !strings.Contains(out, "Update ralph to the latest release") {
		t.Errorf("root help lacks update:\n%s", out)
	}
}

func generatedNotes(titles ...string) string {
	var b strings.Builder
	b.WriteString("## What's Changed\n")
	for _, title := range titles {
		fmt.Fprintf(&b, "* %s by @someone in https://github.com/bengearig/ralph-cli/pull/1\n", title)
	}
	b.WriteString("\n## New Contributors\n* @newbie made their first contribution in https://github.com/bengearig/ralph-cli/pull/2\n\n")
	b.WriteString("**Full Changelog**: https://github.com/bengearig/ralph-cli/compare/v1.0.0...v1.1.0\n")
	return b.String()
}

func changelogReleases() []fakeRelease {
	return []fakeRelease{
		{tag: "v1.2.0", body: generatedNotes("[K-2] M-2: Third", "[K-2] M-3: Fourth")},
		{tag: "v1.1.0", body: generatedNotes("[K-1] M-1: First", "[K-1] M-2: Second")},
		{tag: "v1.0.0", body: generatedNotes("Old one")},
	}
}

const changelogBlocks = "v1.1.0\n  - [K-1] M-1: First\n  - [K-1] M-2: Second\nv1.2.0\n  - [K-2] M-2: Third\n  - [K-2] M-3: Fourth\n"

func TestUpdateChangelog(t *testing.T) {
	type entry struct {
		Version string   `json:"version"`
		Changes []string `json:"changes"`
	}
	wantJSON := []entry{
		{"v1.1.0", []string{"[K-1] M-1: First", "[K-1] M-2: Second"}},
		{"v1.2.0", []string{"[K-2] M-2: Third", "[K-2] M-3: Fourth"}},
	}

	t.Run("install text", func(t *testing.T) {
		newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.0.0")
		out, errOut, err := newTestEnv(t, false).run("", "update")
		if err != nil {
			t.Fatal(err)
		}
		want := "updating ralph v1.0.0 -> v1.2.0\ninstalled " + exe + "\n\n" + changelogBlocks +
			"\nprojects refresh their integration files with ralph init\n"
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		if errOut != "" {
			t.Errorf("stderr = %q", errOut)
		}
		assertInstalled(t, exe)
	})

	t.Run("install json", func(t *testing.T) {
		newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.0.0")
		var got struct {
			Previous       string  `json:"previous"`
			Installed      string  `json:"installed"`
			Path           string  `json:"path"`
			Changelog      []entry `json:"changelog"`
			ChangelogError *string `json:"changelog_error"`
		}
		newTestEnv(t, false).json(&got, "update")
		if got.Previous != "v1.0.0" || got.Installed != "v1.2.0" || got.Path != exe {
			t.Errorf("got %+v", got)
		}
		if fmt.Sprint(got.Changelog) != fmt.Sprint(wantJSON) {
			t.Errorf("changelog = %+v", got.Changelog)
		}
		if got.ChangelogError != nil {
			t.Errorf("changelog_error = %q", *got.ChangelogError)
		}
	})

	t.Run("check text", func(t *testing.T) {
		newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.0.0")
		out := newTestEnv(t, false).ok("update", "--check")
		want := "ralph v1.0.0\nlatest v1.2.0\nan update is available\n\n" + changelogBlocks
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		assertUntouched(t, exe)
	})

	t.Run("check json", func(t *testing.T) {
		newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.0.0")
		var got struct {
			UpdateAvailable bool    `json:"update_available"`
			Changelog       []entry `json:"changelog"`
			ChangelogError  *string `json:"changelog_error"`
		}
		newTestEnv(t, false).json(&got, "update", "--check")
		if !got.UpdateAvailable || fmt.Sprint(got.Changelog) != fmt.Sprint(wantJSON) || got.ChangelogError != nil {
			t.Errorf("got %+v", got)
		}
		assertUntouched(t, exe)
	})

	t.Run("no update available", func(t *testing.T) {
		api := newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.2.0")
		h := newTestEnv(t, false)
		out := h.ok("update", "--check")
		if out != "ralph v1.2.0\nlatest v1.2.0\nralph is up to date\n" {
			t.Errorf("check output:\n%s", out)
		}
		var got map[string]any
		h.json(&got, "update", "--check")
		if _, ok := got["changelog"]; ok {
			t.Errorf("changelog in %v", got)
		}
		if out := h.ok("update"); out != "ralph v1.2.0 is already the latest version\n" {
			t.Errorf("update output:\n%s", out)
		}
		if n := api.listHits.Load(); n != 0 {
			t.Errorf("%d releases-list requests", n)
		}
		assertUntouched(t, exe)
	})

	t.Run("empty body", func(t *testing.T) {
		newFakeAPI(t, "v1.1.0", fakeRelease{tag: "v1.1.0"})
		stubUpdate(t, "v1.0.0")
		out := newTestEnv(t, false).ok("update")
		if !strings.Contains(out, "\n\nv1.1.0\n  no release notes\n\nprojects refresh") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("force on non-release build", func(t *testing.T) {
		api := newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "dev")
		out := newTestEnv(t, false).ok("update", "--force")
		want := "updating ralph dev -> v1.2.0\ninstalled " + exe + "\n\nv1.2.0\n  - [K-2] M-2: Third\n  - [K-2] M-3: Fourth\n" +
			"\nprojects refresh their integration files with ralph init\n"
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		if n := api.listHits.Load(); n != 0 {
			t.Errorf("%d releases-list requests", n)
		}
	})

	t.Run("downgrade", func(t *testing.T) {
		api := newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.2.0")
		out := newTestEnv(t, false).ok("update", "--version", "v1.1.0")
		want := "downgrading ralph v1.2.0 -> v1.1.0\ninstalled " + exe + "\nprojects refresh their integration files with ralph init\n"
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		if n := api.listHits.Load(); n != 0 {
			t.Errorf("%d releases-list requests", n)
		}
	})

	t.Run("same version force", func(t *testing.T) {
		api := newFakeAPI(t, "v1.2.0", changelogReleases()...)
		exe := stubUpdate(t, "v1.2.0")
		out := newTestEnv(t, false).ok("update", "--force")
		want := "updating ralph v1.2.0 -> v1.2.0\ninstalled " + exe + "\nprojects refresh their integration files with ralph init\n"
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		if n := api.listHits.Load(); n != 0 {
			t.Errorf("%d releases-list requests", n)
		}
	})

	t.Run("pre-release", func(t *testing.T) {
		releases := append([]fakeRelease{{tag: "v1.1.0-rc.1", prerelease: true, body: generatedNotes("Candidate")}}, changelogReleases()...)
		newFakeAPI(t, "v1.2.0", releases...)
		stubUpdate(t, "v1.0.0")
		h := newTestEnv(t, false)
		if out := h.ok("update", "--check"); strings.Contains(out, "v1.1.0-rc.1") {
			t.Errorf("pre-release listed without --pre:\n%s", out)
		}
		out := h.ok("update", "--pre", "--check")
		if !strings.Contains(out, "v1.1.0-rc.1\n  - Candidate\nv1.1.0\n") {
			t.Errorf("pre-release missing with --pre:\n%s", out)
		}
	})

	t.Run("release list failure", func(t *testing.T) {
		api := newFakeAPI(t, "v1.2.0", changelogReleases()...)
		api.listStatus = http.StatusInternalServerError
		exe := stubUpdate(t, "v1.0.0")
		h := newTestEnv(t, false)
		out, errOut, err := h.run("", "update")
		if err != nil {
			t.Fatal(err)
		}
		want := "updating ralph v1.0.0 -> v1.2.0\ninstalled " + exe + "\nprojects refresh their integration files with ralph init\n"
		if out != want {
			t.Errorf("output:\n%s\nwant:\n%s", out, want)
		}
		if !strings.HasPrefix(errOut, "warning: changelog unavailable: ") || strings.Count(errOut, "\n") != 1 {
			t.Errorf("stderr = %q", errOut)
		}
		assertInstalled(t, exe)

		var got map[string]any
		h.json(&got, "update", "--force")
		if s, _ := got["changelog_error"].(string); s == "" {
			t.Errorf("changelog_error missing: %v", got)
		}
		if _, ok := got["changelog"]; ok {
			t.Errorf("changelog present: %v", got)
		}
		if got["installed"] != "v1.2.0" {
			t.Errorf("got %v", got)
		}
	})
}
