// Package claudecode drives Claude Code (the `claude` CLI) as a ralph
// harness and wires ralph into Claude Code projects.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
)

// Name is the harness's config identifier.
const Name = config.HarnessClaudeCode

func init() {
	harness.Register(Name, func(cfg *config.Config) (harness.Harness, error) {
		return New(cfg.Harness.ClaudeCode), nil
	})
}

// Harness runs `claude -p` headlessly.
type Harness struct {
	cfg config.ClaudeCodeConfig
}

var (
	_ harness.Harness   = (*Harness)(nil)
	_ harness.Installer = (*Harness)(nil)
)

// New returns a Claude Code harness.
func New(cfg config.ClaudeCodeConfig) *Harness { return &Harness{cfg: cfg} }

func (h *Harness) Name() string { return Name }

// Command builds the headless invocation. The ralph MCP server comes from the
// project's .mcp.json, which `ralph init` maintains; nobody can answer
// permission prompts, so they are denied.
func (h *Harness) Command(req harness.Request) []string {
	argv := []string{h.cfg.Binary, "--print", "--output-format", "stream-json", "--verbose", "--permission-prompts", "none"}
	model := req.Model
	if model == "" {
		model = h.cfg.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if h.cfg.PermissionMode != "" {
		argv = append(argv, "--permission-mode", h.cfg.PermissionMode)
	}
	if h.cfg.MaxBudgetUSD > 0 {
		argv = append(argv, "--max-budget-usd", strconv.FormatFloat(h.cfg.MaxBudgetUSD, 'f', -1, 64))
	}
	return append(argv, h.cfg.ExtraArgs...)
}

// stopGrace is how long a stopped session gets to exit before it is killed.
var stopGrace = 10 * time.Second

// drainTimeout bounds reading buffered output after the session exits.
const drainTimeout = 2 * time.Second

const stderrTail = 8 << 10

// Run executes one headless session, streaming its output to req.Transcript
// and progress to req.OnEvent.
func (h *Harness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	var res harness.Result
	argv := h.Command(req)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = req.WorkDir
	exited := configureProcess(cmd, stopGrace)
	cmd.WaitDelay = stopGrace + time.Second
	// Pipes of our own, rather than exec's copying goroutines, let Wait
	// return when the session exits even if something it started still holds
	// one of its standard streams open.
	var pipes [3][2]*os.File
	for i := range pipes {
		r, w, err := os.Pipe()
		if err != nil {
			closeAll(pipes[:i])
			return res, err
		}
		pipes[i] = [2]*os.File{r, w}
	}
	defer closeAll(pipes[:])
	inR, inW := pipes[0][0], pipes[0][1]
	outR, outW := pipes[1][0], pipes[1][1]
	errR, errW := pipes[2][0], pipes[2][1]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	started := time.Now()
	err := cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		return res, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	go func() {
		_, _ = io.WriteString(inW, req.Prompt)
		inW.Close()
	}()

	p := &streamParser{res: &res, emit: req.OnEvent}
	stderr := &tailBuffer{max: stderrTail}
	outDone := make(chan error, 1)
	errDone := make(chan error, 1)
	go func() { outDone <- readStream(outR, req.Transcript, p, stop) }()
	go func() { _, err := io.Copy(stderr, errR); errDone <- err }()

	waitErr := cmd.Wait()
	exited()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainTimeout)
	defer cancelDrain()
	readErr := awaitReader(outDone, outR, drainCtx.Done())
	_ = awaitReader(errDone, errR, drainCtx.Done())

	if err := ctx.Err(); err != nil {
		return res, err
	}
	if p.toolsProblem != "" {
		return res, fmt.Errorf("%w: %s; run `claude` in %s once and approve the %q server, then rerun the loop",
			harness.ErrToolsUnavailable, p.toolsProblem, req.WorkDir, ServerName)
	}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return res, fmt.Errorf("waiting for %s: %w", argv[0], waitErr)
	}
	if readErr != nil {
		return res, fmt.Errorf("reading %s output: %w", argv[0], readErr)
	}
	if res.Duration == 0 {
		res.Duration = time.Since(started)
	}
	if !res.Completed && res.Summary == "" {
		res.Summary = stderr.String()
	}
	return res, nil
}

// readStream copies each output line to transcript and feeds it to p,
// calling stop when p says the session must end.
func readStream(r io.Reader, transcript io.Writer, p *streamParser, stop func()) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if transcript != nil {
				if !bytes.HasSuffix(line, []byte("\n")) {
					line = append(line, '\n')
				}
				if _, werr := transcript.Write(line); werr != nil {
					transcript = nil
					p.send(harness.EventInfo, "transcript write failed; continuing without it: "+werr.Error())
				}
			}
			if p.handle(bytes.TrimSpace(line)) {
				stop()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func closeAll(pipes [][2]*os.File) {
	for _, p := range pipes {
		for _, f := range p {
			if f != nil {
				f.Close()
			}
		}
	}
}

// awaitReader waits for a reader goroutine, closing its pipe to unblock it if
// expired is closed first.
func awaitReader(done <-chan error, pipe *os.File, expired <-chan struct{}) error {
	select {
	case err := <-done:
		return err
	case <-expired:
		pipe.Close()
		err := <-done
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		return err
	}
}
