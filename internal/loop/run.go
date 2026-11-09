package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// Stop reasons reported in an Outcome.
const (
	StopCompleted      = "completed"
	StopNeedsHuman     = "needs_human"
	StopIterationLimit = "iteration_limit"
	StopInterrupted    = "interrupted"
	StopError          = "error"
)

const (
	reasonRunes  = 300
	dirtyListMax = 10
)

// Runner executes loop plans.
type Runner struct {
	Service *service.Service
	Config  *config.Config
	Root    string
	Harness harness.Harness
	// Out receives the live progress feed; nil discards it.
	Out io.Writer
	// Now is the clock used to name run logs; nil means time.Now.
	Now func() time.Time
}

// TaskRef names a task that needs attention after a run.
type TaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
}

// Commit is a commit made for a completed task.
type Commit struct {
	Task string `json:"task"`
	Hash string `json:"hash"`
}

// Outcome summarizes a loop run.
type Outcome struct {
	Milestone  string    `json:"milestone"`
	Branch     string    `json:"branch"`
	StopReason string    `json:"stop_reason"`
	Iterations int       `json:"iterations"`
	Done       []string  `json:"done"`
	Commits    []Commit  `json:"commits"`
	Blocked    []TaskRef `json:"blocked"`
	Proposed   []TaskRef `json:"proposed"`
	// Stashes names git stash entries holding unfinished work set aside
	// before moving to another task.
	Stashes []string `json:"stashes"`
	CostUSD float64  `json:"cost_usd"`
	Logs    string   `json:"logs"`
}

type commitData struct {
	Task       service.TaskView
	Initiative Ref
	Milestone  Ref
}

// Run works the plan's milestone until nothing loopable remains or the
// iteration budget is spent. It returns errs.ErrNeedsHuman (wrapped) when it
// stops with work a person must handle. Interrupted or failed sessions leave
// their task IN_PROGRESS so the next run resumes it.
func (r Runner) Run(ctx context.Context, plan Plan) (Outcome, error) {
	out := Outcome{
		Milestone: plan.Milestone.ID,
		Branch:    plan.Branch,
		Done:      []string{},
		Commits:   []Commit{},
		Blocked:   []TaskRef{},
		Proposed:  []TaskRef{},
		Stashes:   []string{},
	}
	if len(plan.Tasks) == 0 {
		out.StopReason = StopCompleted
		return out, fmt.Errorf("%w: %s has no IN_PROGRESS or TODO tasks", ErrNothingToLoop, plan.Milestone.ID)
	}
	out.StopReason = StopError
	f := feed{w: r.Out}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	milestoneID, err := domain.ParseID(domain.KindMilestone, plan.Milestone.ID)
	if err != nil {
		return out, err
	}

	promptTmpl, err := promptTemplate(r.Config, r.Root)
	if err != nil {
		return out, err
	}
	if _, err := template.New("prompt").Option("missingkey=error").Parse(promptTmpl); err != nil {
		return out, fmt.Errorf("parsing prompt template: %w", err)
	}
	commitTmpl, err := template.New("commit").Option("missingkey=error").Parse(r.Config.Loop.CommitTemplate)
	if err != nil {
		return out, fmt.Errorf("loop.commit_template: %w", err)
	}

	ralphDir := filepath.Join(r.Root, project.DirName)
	lock, err := acquireLock(filepath.Join(ralphDir, "loop.lock"), "milestone "+plan.Milestone.ID)
	if err != nil {
		return out, err
	}
	defer lock.release()

	repo, branch, err := r.prepareBranch(ctx, plan, milestoneID, f)
	if branch != "" {
		plan.Branch, out.Branch = branch, branch
	}
	if err != nil {
		return out, err
	}

	runDir := filepath.Join(ralphDir, "runs", plan.Milestone.ID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return out, fmt.Errorf("creating run log directory: %w", err)
	}
	out.Logs = relTo(r.Root, runDir)

	// leftover is the unfinished task whose changes may be in the working
	// tree; they are stashed before another task starts so commits stay
	// per task. Changes present when the run starts are left alone.
	var leftover *service.TaskView
	limitHit := true
	for iter := 1; iter <= plan.MaxIterations; iter++ {
		if ctx.Err() != nil {
			return out, interrupted(ctx, &out, f, "")
		}
		next, err := r.Service.NextTask(ctx, milestoneID)
		if err != nil {
			return out, err
		}
		if next == nil {
			limitHit = false
			break
		}
		taskID, err := domain.ParseID(domain.KindTask, next.ID)
		if err != nil {
			return out, err
		}
		if leftover != nil && leftover.ID != next.ID {
			if err := r.stashLeftover(ctx, repo, *leftover, &out, f); err != nil {
				return out, err
			}
			leftover = nil
		}
		if next.Status == string(domain.StatusTodo) {
			if _, err := r.Service.SetTaskStatus(ctx, taskID, domain.StatusInProgress, ""); err != nil {
				return out, err
			}
		}
		task, err := r.Service.GetTask(ctx, taskID)
		if err != nil {
			return out, err
		}
		prompt, err := RenderPrompt(promptTmpl, PromptData{
			Project:        plan.Project,
			Initiative:     plan.Initiative,
			Milestone:      plan.Milestone,
			Branch:         plan.Branch,
			Task:           task,
			Iteration:      iter,
			MaxIterations:  plan.MaxIterations,
			StallThreshold: plan.StallThreshold,
			CommitPerTask:  plan.CommitPerTask,
		})
		if err != nil {
			return out, err
		}

		out.Iterations = iter
		f.printf("▶ [%d/%d] %s %s\n", iter, plan.MaxIterations, task.ID, task.Title)
		res, logPath, err := r.runSession(ctx, plan, task, iter, prompt, runDir, now(), f)
		out.CostUSD += res.CostUSD
		if err != nil {
			r.recordHeadBestEffort(ctx, repo, milestoneID, f)
			if ctx.Err() != nil {
				return out, interrupted(ctx, &out, f, task.ID+" left IN_PROGRESS, ")
			}
			f.printf("■ %s left IN_PROGRESS: %v\n", task.ID, err)
			return out, err
		}

		after, err := r.Service.GetTask(ctx, taskID)
		if err != nil {
			return out, err
		}
		if after.Status == string(domain.StatusInProgress) {
			if after, err = r.Service.SetTaskStatus(ctx, taskID, domain.StatusTodo, sessionReason(res)); err != nil {
				return out, err
			}
		}
		leftover = nil
		if after.Status != string(domain.StatusDone) {
			leftover = &after
		}
		stats := fmt.Sprintf("%d turns · %s · %s", res.Turns, FormatCost(res.CostUSD), relTo(r.Root, logPath))
		switch domain.Status(after.Status) {
		case domain.StatusDone:
			f.printf("✓ %s DONE · %s\n", after.ID, stats)
			out.Done = append(out.Done, after.ID)
			if plan.CommitPerTask {
				c, err := r.commit(ctx, repo, commitTmpl, plan, after, f)
				if err != nil {
					r.recordHeadBestEffort(ctx, repo, milestoneID, f)
					if ctx.Err() != nil {
						return out, interrupted(ctx, &out, f, after.ID+" is DONE but not committed; commit by hand, then ")
					}
					return out, err
				}
				if c != nil {
					out.Commits = append(out.Commits, *c)
					if err := r.Service.RecordTaskCommit(ctx, taskID, c.Hash); err != nil {
						return out, fmt.Errorf("recording %s's commit: %w", after.ID, err)
					}
				}
			}
		case domain.StatusTodo:
			f.printf("↺ %s not finished (failure %d of %d) · %s\n", after.ID, after.FailureCount, plan.StallThreshold, stats)
		case domain.StatusBlocked:
			f.printf("✗ %s BLOCKED: %s · %s\n", after.ID, after.BlockedReason, stats)
		case domain.StatusProposed:
			f.printf("⤺ %s sent back to PROPOSED · %s\n", after.ID, stats)
		default:
			f.printf("• %s is %s · %s\n", after.ID, after.Status, stats)
		}
		if err := r.recordHead(ctx, repo, milestoneID); err != nil {
			return out, fmt.Errorf("recording the milestone head: %w", err)
		}
	}

	if leftover != nil {
		if dirty, err := repo.Dirty(ctx); err == nil && len(dirty) > 0 {
			f.printf("■ uncommitted changes from %s (%s) are left in the working tree\n", leftover.ID, leftover.Status)
		}
	}
	if err := r.collectRemaining(ctx, milestoneID, &out); err != nil {
		return out, err
	}
	if limitHit {
		next, err := r.Service.NextTask(ctx, milestoneID)
		if err != nil {
			return out, err
		}
		if next != nil {
			out.StopReason = StopIterationLimit
			f.printf("■ loop.max_iterations (%d) reached; %s is still loopable\n", plan.MaxIterations, next.ID)
			return out, fmt.Errorf("%w: loop.max_iterations (%d) reached with %s still loopable; rerun to continue",
				errs.ErrNeedsHuman, plan.MaxIterations, next.ID)
		}
	}

	m, err := r.Service.GetMilestone(ctx, milestoneID)
	if err != nil {
		return out, err
	}
	if m.Status == service.MilestoneDone || m.Status == service.MilestoneCancelled {
		out.StopReason = StopCompleted
		f.printf("■ %s is %s; next: ralph pr create --milestone %s\n", m.ID, m.Status, m.ID)
		return out, nil
	}
	out.StopReason = StopNeedsHuman
	for _, t := range out.Blocked {
		f.printf("■ %s BLOCKED: %s\n", t.ID, t.Reason)
	}
	for _, t := range out.Proposed {
		f.printf("■ %s needs articulation (PROPOSED)\n", t.ID)
	}
	return out, fmt.Errorf("%w: %s stopped with %d BLOCKED and %d PROPOSED task(s); see `ralph task list --milestone %s`",
		errs.ErrNeedsHuman, m.ID, len(out.Blocked), len(out.Proposed), m.ID)
}

// prepareBranch checks out the milestone branch, creating it from the base
// branch when needed, and records the branch when the milestone has no
// record yet. It returns the branch actually checked out. Uncommitted changes
// are allowed only when already on the milestone branch, where they are the
// previous run's unfinished work.
func (r Runner) prepareBranch(ctx context.Context, plan Plan, milestoneID int64, f feed) (git.Repo, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return git.Repo{}, "", errors.New("the loop needs git, which was not found on PATH")
	}
	gitRoot, err := project.GitRoot(ctx, r.Root)
	if err != nil {
		return git.Repo{}, "", fmt.Errorf("the loop needs a git repository: %w", err)
	}
	root := r.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(gitRoot); err == nil {
		gitRoot = resolved
	}
	exclude, err := filepath.Rel(gitRoot, filepath.Join(root, project.DirName))
	if err != nil {
		return git.Repo{}, "", err
	}
	repo := git.Repo{Dir: gitRoot, Exclude: filepath.ToSlash(exclude)}

	m, err := r.Service.GetMilestone(ctx, milestoneID)
	if err != nil {
		return repo, "", err
	}
	recorded, branch := m.Branch, plan.Branch
	if recorded != "" && recorded != branch {
		return repo, "", fmt.Errorf("%s's recorded branch %s differs from the planned %s; rerun to plan again", m.ID, recorded, branch)
	}

	current, err := repo.CurrentBranch(ctx)
	if err != nil {
		return repo, "", err
	}
	if recorded != "" && current != recorded {
		exists, err := repo.BranchExists(ctx, recorded)
		if err != nil {
			return repo, "", err
		}
		if !exists {
			if branch, err = BranchName(r.Config.Loop.BranchTemplate, plan.Initiative, plan.Milestone); err != nil {
				return repo, "", err
			}
			f.printf("! recorded branch %s not found; using %s from loop.branch_template\n", recorded, branch)
			recorded = ""
		}
	}

	start, err := r.checkout(ctx, repo, plan, branch, current, f)
	if err != nil {
		return repo, branch, err
	}
	switch {
	case start != "":
		base, err := repo.RevParse(ctx, start)
		if err != nil {
			return repo, branch, err
		}
		return repo, branch, r.recordBranch(ctx, milestoneID, domain.MilestoneGit{Branch: branch, BaseCommit: base, HeadCommit: base})
	case recorded == "":
		g, err := r.adoptBranch(ctx, repo, plan, branch, f)
		if err != nil {
			return repo, branch, err
		}
		return repo, branch, r.recordBranch(ctx, milestoneID, g)
	}
	return repo, branch, nil
}

// checkout puts the work tree on branch. It returns the start ref when it
// created the branch, or "" when the branch already existed.
func (r Runner) checkout(ctx context.Context, repo git.Repo, plan Plan, branch, current string, f feed) (string, error) {
	if current == branch {
		return "", nil
	}
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		return "", err
	}
	if len(dirty) > 0 {
		shown := dirty
		if len(shown) > dirtyListMax {
			shown = append(shown[:dirtyListMax:dirtyListMax], fmt.Sprintf("… and %d more", len(dirty)-dirtyListMax))
		}
		return "", fmt.Errorf("the working tree has uncommitted changes; commit or stash them, or switch to %s to resume:\n  %s",
			branch, strings.Join(shown, "\n  "))
	}
	exists, err := repo.BranchExists(ctx, branch)
	if err != nil {
		return "", err
	}
	if exists {
		f.printf("switching to %s\n", branch)
		return "", repo.Switch(ctx, branch)
	}
	start, err := baseRef(ctx, repo, plan.BaseBranch)
	if err != nil {
		return "", err
	}
	if start == "" {
		return "", fmt.Errorf("base branch %q not found locally or on origin; set pr.base_branch", plan.BaseBranch)
	}
	f.printf("creating %s from %s\n", branch, strings.TrimPrefix(strings.TrimPrefix(start, "refs/heads/"), "refs/remotes/"))
	if err := repo.CreateBranch(ctx, branch, start); err != nil {
		return "", err
	}
	return start, nil
}

// adoptBranch builds the first record for an existing branch the loop did
// not create, trusting it as the milestone's branch.
func (r Runner) adoptBranch(ctx context.Context, repo git.Repo, plan Plan, branch string, f feed) (domain.MilestoneGit, error) {
	g := domain.MilestoneGit{Branch: branch}
	head, err := repo.RevParse(ctx, "HEAD")
	if err != nil {
		return g, err
	}
	g.HeadCommit = head
	base, err := baseRef(ctx, repo, plan.BaseBranch)
	if err != nil {
		return g, err
	}
	if base == "" {
		f.printf("! base branch %q not found locally or on origin; recording %s without a base commit\n", plan.BaseBranch, branch)
		return g, nil
	}
	hash, found, err := repo.MergeBase(ctx, "HEAD", base)
	if err != nil {
		return g, err
	}
	if !found {
		f.printf("! %s shares no history with %s; recording it without a base commit\n", branch, plan.BaseBranch)
		return g, nil
	}
	g.BaseCommit = hash
	return g, nil
}

func (r Runner) recordBranch(ctx context.Context, milestoneID int64, g domain.MilestoneGit) error {
	if err := r.Service.RecordMilestoneGit(ctx, milestoneID, g); err != nil {
		return fmt.Errorf("recording branch %s: %w", g.Branch, err)
	}
	return nil
}

// baseRef returns the first of the local and origin base branch refs that
// exists, or "" when neither does.
func baseRef(ctx context.Context, repo git.Repo, base string) (string, error) {
	for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
		ok, err := repo.RefExists(ctx, ref)
		if err != nil {
			return "", err
		}
		if ok {
			return ref, nil
		}
	}
	return "", nil
}

// runSession runs one harness session, saving its prompt and transcript.
func (r Runner) runSession(ctx context.Context, plan Plan, task service.TaskView, iter int, prompt, runDir string, started time.Time, f feed) (harness.Result, string, error) {
	base := fmt.Sprintf("%s-%03d-%s", started.Format("20060102-150405"), iter, task.ID)
	logPath := filepath.Join(runDir, base+".jsonl")
	if err := os.WriteFile(filepath.Join(runDir, base+".prompt.md"), []byte(prompt), 0o644); err != nil {
		return harness.Result{}, logPath, fmt.Errorf("saving prompt: %w", err)
	}
	transcript, err := os.Create(logPath)
	if err != nil {
		return harness.Result{}, logPath, fmt.Errorf("creating transcript: %w", err)
	}
	res, runErr := r.Harness.Run(ctx, harness.Request{
		Step:       config.StepLoop,
		Model:      plan.Model,
		Prompt:     prompt,
		WorkDir:    r.Root,
		Transcript: transcript,
		OnEvent:    f.event,
	})
	return res, logPath, errors.Join(runErr, transcript.Close())
}

func (r Runner) commit(ctx context.Context, repo git.Repo, tmpl *template.Template, plan Plan, task service.TaskView, f feed) (*Commit, error) {
	var b strings.Builder
	if err := tmpl.Execute(&b, commitData{Task: task, Initiative: plan.Initiative, Milestone: plan.Milestone}); err != nil {
		return nil, fmt.Errorf("loop.commit_template: %w", err)
	}
	msg := strings.TrimSpace(b.String())
	if msg == "" {
		msg = task.ID + ": " + task.Title
	}
	hash, committed, err := repo.CommitAll(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("%s is DONE but the commit failed; commit by hand, then rerun: %w", task.ID, err)
	}
	if !committed {
		f.printf("  nothing to commit for %s\n", task.ID)
		return nil, nil
	}
	f.printf("  committed %s %q\n", shortHash(hash), msg)
	return &Commit{Task: task.ID, Hash: hash}, nil
}

// recordHead records the branch tip as the milestone's head.
func (r Runner) recordHead(ctx context.Context, repo git.Repo, milestoneID int64) error {
	head, err := repo.RevParse(ctx, "HEAD")
	if err != nil {
		return err
	}
	return r.Service.RecordMilestoneHead(ctx, milestoneID, head)
}

// recordHeadBestEffort records the head on the way out of a failed or
// interrupted iteration, even when ctx is cancelled, without masking the
// error that ends the run.
func (r Runner) recordHeadBestEffort(ctx context.Context, repo git.Repo, milestoneID int64, f feed) {
	if err := r.recordHead(context.WithoutCancel(ctx), repo, milestoneID); err != nil {
		f.printf("■ could not record head: %v\n", err)
	}
}

func shortHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// stashLeftover sets aside an unfinished task's changes before another task
// starts.
func (r Runner) stashLeftover(ctx context.Context, repo git.Repo, task service.TaskView, out *Outcome, f feed) error {
	msg := fmt.Sprintf("ralph: %s %s, unfinished work", task.ID, task.Status)
	saved, err := repo.Stash(ctx, msg)
	if err != nil {
		return fmt.Errorf("setting aside unfinished work from %s: %w", task.ID, err)
	}
	if saved {
		out.Stashes = append(out.Stashes, msg)
		f.printf("  stashed unfinished work from %s as %q (see `git stash list`)\n", task.ID, msg)
	}
	return nil
}

func (r Runner) collectRemaining(ctx context.Context, milestoneID int64, out *Outcome) error {
	tasks, err := r.Service.ListTasks(ctx, service.TaskFilter{
		MilestoneID: milestoneID,
		Statuses:    []domain.Status{domain.StatusBlocked, domain.StatusProposed},
	})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		ref := TaskRef{ID: t.ID, Title: t.Title, Reason: t.BlockedReason}
		if t.Status == string(domain.StatusBlocked) {
			out.Blocked = append(out.Blocked, ref)
		} else {
			out.Proposed = append(out.Proposed, ref)
		}
	}
	return nil
}

// interrupted records an interruption; detail precedes "rerun to resume".
func interrupted(ctx context.Context, out *Outcome, f feed, detail string) error {
	out.StopReason = StopInterrupted
	f.printf("■ interrupted; %srerun to resume\n", detail)
	return fmt.Errorf("loop interrupted: %w", ctx.Err())
}

// sessionReason explains why a session that never set a status failed.
func sessionReason(res harness.Result) string {
	var why string
	switch {
	case !res.Completed:
		why = fmt.Sprintf("session ended without a final result (exit code %d)", res.ExitCode)
	case res.IsError:
		why = fmt.Sprintf("session ended with %s", res.Subtype)
	default:
		why = "session finished without setting a task status"
	}
	if s := strings.Join(strings.Fields(res.Summary), " "); s != "" {
		if r := []rune(s); len(r) > reasonRunes {
			s = string(r[:reasonRunes-1]) + "…"
		}
		why += ": " + s
	}
	return why
}

func relTo(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
