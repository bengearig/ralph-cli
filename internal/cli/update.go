package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/release"
	"github.com/bengearig/ralph/internal/selfupdate"
	"github.com/bengearig/ralph/internal/version"
)

const (
	maxUpdateChecksums = 1 << 20
	maxUpdateArchive   = 200 << 20
	updateRequestWait  = 30 * time.Second
)

var (
	releaseAPIURL    = ""
	updateExecutable = os.Executable
	updateVersion    = func() string { return version.Get().Version }
	updateHTTPClient = newUpdateHTTPClient()
)

// newUpdateHTTPClient bounds the wait for each request's response without
// capping how long a large body may take to stream.
func newUpdateHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = updateRequestWait
	return &http.Client{Transport: transport}
}

type updateOptions struct {
	check   bool
	version string
	pre     bool
	force   bool
}

func (a *app) newUpdateCommand() *cobra.Command {
	var opts updateOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ralph to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := a.runUpdate(cmd.Context(), opts)
			if err != nil && cmd.Context().Err() != nil {
				return errs.ErrInterrupted
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.check, "check", false, "report the latest version without installing")
	f.StringVar(&opts.version, "version", "", "install this release tag, a downgrade or pre-release included")
	f.BoolVar(&opts.pre, "pre", false, "consider pre-releases when picking the newest release")
	f.BoolVar(&opts.force, "force", false, "reinstall the same version, or replace a non-release build")
	return cmd
}

func (a *app) runUpdate(ctx context.Context, opts updateOptions) error {
	if opts.version != "" && opts.pre {
		return errors.New("--version and --pre cannot be used together")
	}
	if opts.version != "" {
		if _, err := release.ParseVersion(opts.version); err != nil {
			return fmt.Errorf("invalid version %q: expected a tag such as v1.2.3", opts.version)
		}
	}

	cur := updateVersion()
	curVer, curErr := release.ParseVersion(cur)
	releaseBuild := curErr == nil

	client := release.NewClient(releaseAPIURL, updateHTTPClient)
	var rel release.Release
	var err error
	switch {
	case opts.version != "":
		rel, err = client.ByTag(ctx, opts.version)
	case opts.pre:
		rel, err = client.Newest(ctx, true)
	default:
		rel, err = client.Latest(ctx)
	}
	if err != nil {
		if errors.Is(err, release.ErrNotFound) {
			msg := "no release found"
			if opts.version != "" {
				msg += " " + opts.version
			}
			return errors.New(msg)
		}
		return err
	}
	tag := rel.Tag
	tagVer, err := release.ParseVersion(tag)
	if err != nil {
		return fmt.Errorf("release has an unusable tag: %w", err)
	}
	cmp := 0
	if releaseBuild {
		cmp = release.Compare(curVer, tagVer)
	}

	if opts.check {
		available := releaseBuild && cmp < 0
		var notes []release.Changelog
		var notesErr error
		if available {
			notes, notesErr = changelogBetween(ctx, client, curVer, rel, opts.pre || rel.Prerelease)
		}
		if a.jsonOut {
			return a.writeJSON(struct {
				Current         string              `json:"current"`
				Latest          string              `json:"latest"`
				UpdateAvailable bool                `json:"update_available"`
				ReleaseBuild    bool                `json:"release_build"`
				Changelog       []release.Changelog `json:"changelog,omitempty"`
				ChangelogError  string              `json:"changelog_error,omitempty"`
			}{cur, tag, available, releaseBuild, notes, errorText(notesErr)})
		}
		out := a.streams.Out
		fmt.Fprintf(out, "ralph %s\nlatest %s\n", cur, tag)
		if available {
			fmt.Fprintln(out, "an update is available")
			a.printChangelog(notes, notesErr)
		} else {
			fmt.Fprintln(out, "ralph is up to date")
		}
		if !releaseBuild {
			fmt.Fprintln(out, "this build is not a release build")
		}
		return nil
	}

	if !releaseBuild && !opts.force {
		return fmt.Errorf("this ralph is not a release build (version %q); install a release with the install script, or pass --force", cur)
	}
	if releaseBuild && cmp == 0 && !opts.force {
		if opts.version != "" {
			return a.say("already at %s\n", tag)
		}
		return a.say("ralph %s is already the latest version\n", tag)
	}
	if releaseBuild && cmp > 0 && opts.version == "" {
		return a.say("ralph %s is newer than the latest release %s; nothing to do\n", cur, tag)
	}
	verb := "updating"
	if releaseBuild && cmp > 0 {
		verb = "downgrading"
	}

	path, err := a.installRelease(ctx, rel, func() error {
		return a.say("%s ralph %s -> %s\n", verb, cur, tag)
	})
	if err != nil {
		return err
	}

	var notes []release.Changelog
	var notesErr error
	switch {
	case !releaseBuild:
		notes = []release.Changelog{release.NewChangelog(rel)}
	case cmp < 0:
		notes, notesErr = changelogBetween(ctx, client, curVer, rel, opts.pre || rel.Prerelease)
	}
	if a.jsonOut {
		return a.writeJSON(struct {
			Previous       string              `json:"previous"`
			Installed      string              `json:"installed"`
			Path           string              `json:"path"`
			Changelog      []release.Changelog `json:"changelog,omitempty"`
			ChangelogError string              `json:"changelog_error,omitempty"`
		}{cur, tag, path, notes, errorText(notesErr)})
	}
	if err := a.say("installed %s\n", path); err != nil {
		return err
	}
	if err := a.printChangelog(notes, notesErr); err != nil {
		return err
	}
	hint := "projects refresh their integration files with ralph init\n"
	if len(notes) > 0 && notesErr == nil {
		hint = "\n" + hint
	}
	return a.say("%s", hint)
}

// changelogBetween builds the changelog of the releases after curVer up to rel.
func changelogBetween(ctx context.Context, client *release.Client, curVer release.Version, rel release.Release, includePre bool) ([]release.Changelog, error) {
	releases, err := client.Between(ctx, curVer, rel, includePre)
	if err != nil {
		return nil, err
	}
	notes := make([]release.Changelog, len(releases))
	for i, r := range releases {
		notes[i] = release.NewChangelog(r)
	}
	return notes, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// printChangelog writes a blank line and the formatted notes, or a warning on
// standard error when they could not be read. It prints nothing in JSON mode.
func (a *app) printChangelog(notes []release.Changelog, notesErr error) error {
	if a.jsonOut {
		return nil
	}
	if notesErr != nil {
		_, err := fmt.Fprintf(a.streams.Err, "warning: changelog unavailable: %v\n", notesErr)
		return err
	}
	if len(notes) == 0 {
		return nil
	}
	return a.say("\n%s", release.FormatChangelog(notes))
}

// say prints a status line unless JSON output is selected.
func (a *app) say(format string, args ...any) error {
	if a.jsonOut {
		return nil
	}
	_, err := fmt.Fprintf(a.streams.Out, format, args...)
	return err
}

// installRelease replaces the running executable with the release's binary.
// announce runs once staging has succeeded, before any download.
func (a *app) installRelease(ctx context.Context, rel release.Release, announce func() error) (path string, err error) {
	exe, err := updateExecutable()
	if err != nil {
		return "", fmt.Errorf("locate the running executable: %w", err)
	}
	target, err := selfupdate.Resolve(exe)
	if err != nil {
		return "", err
	}
	selfupdate.CleanupOld(target)
	staged, err := selfupdate.Stage(target)
	if err != nil {
		if errors.Is(err, selfupdate.ErrNotWritable) {
			return "", fmt.Errorf("cannot update %s: %s is not writable; reinstall with the install script and --dir, or run ralph with permission to write there", target, filepath.Dir(target))
		}
		return "", err
	}
	defer func() {
		if err != nil {
			staged.Abort()
		}
	}()

	archiveName, err := release.ArchiveName(rel.Tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	archiveAsset, ok := rel.Asset(archiveName)
	if !ok {
		return "", fmt.Errorf("no release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	sumsAsset, ok := rel.Asset("checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}
	if err := announce(); err != nil {
		return "", err
	}

	client := release.NewClient(releaseAPIURL, updateHTTPClient)
	var sumsBuf bytes.Buffer
	if _, err := client.Download(ctx, sumsAsset.URL, &sumsBuf, maxUpdateChecksums); err != nil {
		return "", err
	}
	sums, err := release.ParseChecksums(&sumsBuf)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp("", "ralph-update-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	sum, err := client.Download(ctx, archiveAsset.URL, tmp, maxUpdateArchive)
	if err != nil {
		return "", err
	}
	if err := release.Verify(sums, archiveName, sum); err != nil {
		return "", err
	}
	info, err := tmp.Stat()
	if err != nil {
		return "", fmt.Errorf("stat downloaded archive: %w", err)
	}
	if err := release.ExtractBinary(tmp, info.Size(), runtime.GOOS, staged.Writer(), maxUpdateArchive); err != nil {
		return "", err
	}
	if err := staged.Commit(); err != nil {
		return "", err
	}
	return target, nil
}
