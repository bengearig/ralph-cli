// Package drift compares a milestone's recorded git state with its branch
// and explains, per task, where the recorded work is now.
package drift

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/service"
)

// TrailerKey is the commit trailer naming the task a loop commit belongs to.
const TrailerKey = "Ralph-Task"

// CardTrailerKey is the optional commit trailer holding the task's card key.
const CardTrailerKey = "Card"

// State is how a milestone's branch relates to its record.
type State string

const (
	StateBranchMissing State = "branch_missing"
	StateUnrecorded    State = "unrecorded"
	StateMerged        State = "merged"
	StateUnchanged     State = "unchanged"
	StateAhead         State = "ahead"
	StateBehind        State = "behind"
	StateDiverged      State = "diverged"
)

// Match is the matcher that found a DONE task's work on the branch.
type Match string

const (
	MatchPresent Match = "present"
	MatchTrailer Match = "trailer"
	MatchPatchID Match = "patch_id"
	MatchSubject Match = "subject"
	MatchMissing Match = "missing"
)

// Input is what Check needs beyond the store.
type Input struct {
	Service *service.Service
	Repo    git.Repo
	// MilestoneID is the milestone to check.
	MilestoneID int64
	// BaseBranch is pr.base_branch.
	BaseBranch string
	// TemplateBranch is the template-derived branch name, used when the
	// milestone has no recorded branch.
	TemplateBranch string
	// Subjects renders the commit subjects the loop may have given a task;
	// a commit matching any is the task's. Nil disables the subject matcher.
	Subjects func(service.TaskView) []string
}

// Recorded is the milestone's git state as stored.
type Recorded struct {
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	HeadCommit string `json:"head_commit"`
}

// Commit is a commit listed in a report.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// TaskFinding says where a DONE task's work is on the branch.
type TaskFinding struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	RecordedCommit string `json:"recorded_commit"`
	Match          Match  `json:"match"`
	// Commit is the commit holding the task's work, or "" when missing.
	Commit string `json:"commit"`
}

// Report is the result of Check.
type Report struct {
	Milestone string   `json:"milestone"`
	Branch    string   `json:"branch"`
	BaseRef   string   `json:"base_ref"`
	Recorded  Recorded `json:"recorded"`
	// Tip is the branch's current commit, or "" when the branch is missing.
	Tip string `json:"tip"`
	// BranchExists reports whether the local branch exists.
	BranchExists bool  `json:"branch_exists"`
	State        State `json:"state"`
	// RemoteBranchExists reports, for branch_missing, whether
	// origin/<branch> exists.
	RemoteBranchExists bool `json:"remote_branch_exists"`
	// ExtraCommits are the commits beyond the recorded head, for ahead.
	ExtraCommits []Commit `json:"extra_commits,omitempty"`
	// LostCommits are the recorded commits no longer on the branch, for
	// behind.
	LostCommits         []Commit      `json:"lost_commits,omitempty"`
	Tasks               []TaskFinding `json:"tasks"`
	UnattributedCommits []Commit      `json:"unattributed_commits"`
	// ProposedBase is the base reconcile --accept would record.
	ProposedBase string `json:"proposed_base,omitempty"`
}

// Clean reports whether the branch is exactly as recorded.
func (r Report) Clean() bool { return r.State == StateUnchanged }

// Summary describes the state in one line.
func (r Report) Summary() string {
	head := short(r.Recorded.HeadCommit)
	switch r.State {
	case StateBranchMissing:
		s := fmt.Sprintf("%s branch %s does not exist locally", r.Milestone, r.Branch)
		if r.RemoteBranchExists {
			s += fmt.Sprintf(" (origin/%s exists)", r.Branch)
		}
		return s
	case StateUnrecorded:
		return fmt.Sprintf("%s has no recorded head for branch %s (tip %s)", r.Milestone, r.Branch, short(r.Tip))
	case StateMerged:
		s := fmt.Sprintf("%s branch %s is already merged into %s", r.Milestone, r.Branch, shortRef(r.BaseRef))
		if !r.BranchExists {
			s += " (branch deleted)"
		}
		return s
	case StateUnchanged:
		return fmt.Sprintf("%s branch %s is at its recorded head %s", r.Milestone, r.Branch, head)
	case StateAhead:
		return fmt.Sprintf("%s branch %s is %s ahead of its recorded head %s",
			r.Milestone, r.Branch, plural(len(r.ExtraCommits), "commit"), head)
	case StateBehind:
		return fmt.Sprintf("%s branch %s is %s behind its recorded head %s",
			r.Milestone, r.Branch, plural(len(r.LostCommits), "commit"), head)
	case StateDiverged:
		return fmt.Sprintf("%s has diverged from its recorded head %s", r.Milestone, head)
	}
	return fmt.Sprintf("%s is in state %s", r.Milestone, r.State)
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func shortRef(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return s
	}
	return strings.TrimPrefix(ref, "refs/remotes/")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Check builds the drift report for a milestone. It writes nothing.
func Check(ctx context.Context, in Input) (Report, error) {
	return check(ctx, in, "")
}

// check is Check with the recorded branch replaced by branch when it is
// non-empty, as if the record already pointed there.
func check(ctx context.Context, in Input, branch string) (Report, error) {
	if in.Service == nil {
		return Report{}, errors.New("drift: no service")
	}
	m, err := in.Service.GetMilestone(ctx, in.MilestoneID)
	if err != nil {
		return Report{}, err
	}
	if branch != "" {
		m.Branch = branch
	}
	tasks, err := in.Service.ListTasks(ctx, service.TaskFilter{MilestoneID: in.MilestoneID, IncludeArchived: true, Statuses: []domain.Status{domain.StatusDone}})
	if err != nil {
		return Report{}, err
	}
	r := Report{
		Milestone:           m.ID,
		Recorded:            Recorded{Branch: m.Branch, BaseCommit: m.BaseCommit, HeadCommit: m.HeadCommit},
		Tasks:               []TaskFinding{},
		UnattributedCommits: []Commit{},
	}
	r.Branch = m.Branch
	if r.Branch == "" {
		r.Branch = in.TemplateBranch
	}
	if r.Branch == "" {
		return Report{}, fmt.Errorf("%w: %s has no recorded branch and no template branch was given", domain.ErrInvalid, m.ID)
	}
	if r.BaseRef, err = baseRef(ctx, in.Repo, in.BaseBranch); err != nil {
		return Report{}, err
	}

	exists, err := in.Repo.BranchExists(ctx, r.Branch)
	if err != nil {
		return Report{}, err
	}
	r.BranchExists = exists
	if !exists {
		if r.RemoteBranchExists, err = in.Repo.RefExists(ctx, "refs/remotes/origin/"+r.Branch); err != nil {
			return Report{}, err
		}
		wasMerged, err := merged(ctx, in.Repo, r.Recorded.HeadCommit, r.Recorded.BaseCommit, r.BaseRef)
		if err != nil {
			return Report{}, err
		}
		if wasMerged {
			r.State = StateMerged
			if err := findTasks(ctx, in, &r, r.Recorded.HeadCommit, tasks); err != nil {
				return Report{}, err
			}
			return r, nil
		}
		r.State = StateBranchMissing
		for _, t := range tasks {
			r.Tasks = append(r.Tasks, TaskFinding{ID: t.ID, Title: t.Title, RecordedCommit: t.Commit, Match: MatchMissing})
		}
		return r, nil
	}
	if r.Tip, err = in.Repo.RevParse(ctx, "refs/heads/"+r.Branch); err != nil {
		return Report{}, err
	}
	if r.ProposedBase, err = proposedBase(ctx, in.Repo, r.Recorded.BaseCommit, r.Tip, r.BaseRef); err != nil {
		return Report{}, err
	}
	if r.State, err = state(ctx, in.Repo, &r); err != nil {
		return Report{}, err
	}
	switch r.State {
	case StateAhead:
		if r.ExtraCommits, err = commits(ctx, in.Repo, r.Recorded.HeadCommit+".."+r.Tip); err != nil {
			return Report{}, err
		}
	case StateBehind:
		if r.LostCommits, err = commits(ctx, in.Repo, r.Tip+".."+r.Recorded.HeadCommit); err != nil {
			return Report{}, err
		}
	}
	if err := findTasks(ctx, in, &r, r.Tip, tasks); err != nil {
		return Report{}, err
	}
	return r, nil
}

// baseRef returns the first of the local and origin refs for the base branch
// that exists.
func baseRef(ctx context.Context, repo git.Repo, branch string) (string, error) {
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("%w: pr.base_branch is not set", domain.ErrInvalid)
	}
	candidates := []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch}
	for _, ref := range candidates {
		ok, err := repo.RefExists(ctx, ref)
		if err != nil {
			return "", err
		}
		if ok {
			return ref, nil
		}
	}
	return "", fmt.Errorf("%w: base branch %q (pr.base_branch) exists neither locally nor as origin/%s",
		domain.ErrInvalid, branch, branch)
}

// proposedBase is the recorded base while it is still an ancestor of the tip,
// otherwise the merge-base of tip and base ref ("" without shared history).
func proposedBase(ctx context.Context, repo git.Repo, recorded, tip, baseRef string) (string, error) {
	if recorded != "" {
		ok, err := isAncestor(ctx, repo, recorded, tip)
		if err != nil || ok {
			return recorded, err
		}
	}
	mb, _, err := repo.MergeBase(ctx, tip, baseRef)
	return mb, err
}

// isAncestor is git.Repo.IsAncestor that treats a missing ancestor object as
// not an ancestor.
func isAncestor(ctx context.Context, repo git.Repo, ancestor, descendant string) (bool, error) {
	ok, err := repo.CommitExists(ctx, ancestor)
	if err != nil || !ok {
		return false, err
	}
	return repo.IsAncestor(ctx, ancestor, descendant)
}

// merged reports whether head is already in baseRef: it exists, is not the
// recorded base (a branch with no commits of its own), and is an ancestor of
// baseRef. A missing head is not merged.
func merged(ctx context.Context, repo git.Repo, head, base, baseRef string) (bool, error) {
	if head == "" || (base != "" && head == base) {
		return false, nil
	}
	return isAncestor(ctx, repo, head, baseRef)
}

func state(ctx context.Context, repo git.Repo, r *Report) (State, error) {
	head := r.Recorded.HeadCommit
	if head == "" {
		return StateUnrecorded, nil
	}
	base := r.Recorded.BaseCommit
	if base == "" {
		base = r.ProposedBase
	}
	wasMerged, err := merged(ctx, repo, r.Tip, base, r.BaseRef)
	if err != nil {
		return "", err
	}
	if wasMerged {
		return StateMerged, nil
	}
	if r.Tip == head {
		return StateUnchanged, nil
	}
	headExists, err := repo.CommitExists(ctx, head)
	if err != nil {
		return "", err
	}
	if !headExists {
		return StateDiverged, nil
	}
	if ok, err := repo.IsAncestor(ctx, head, r.Tip); err != nil || ok {
		return StateAhead, err
	}
	if ok, err := repo.IsAncestor(ctx, r.Tip, head); err != nil || ok {
		return StateBehind, err
	}
	return StateDiverged, nil
}

func commits(ctx context.Context, repo git.Repo, rangeSpec string) ([]Commit, error) {
	log, err := repo.Log(ctx, rangeSpec)
	if err != nil {
		return nil, err
	}
	out := make([]Commit, 0, len(log))
	for _, c := range log {
		out = append(out, Commit{Hash: c.Hash, Subject: c.Subject})
	}
	return out, nil
}

// findTasks runs the matchers for each DONE task against the candidates
// <base ref>..<head>, newest first, and lists the candidates no task claims.
func findTasks(ctx context.Context, in Input, r *Report, head string, tasks []service.TaskView) error {
	candidates, err := in.Repo.Log(ctx, r.BaseRef+".."+head, TrailerKey)
	if err != nil {
		return err
	}
	patchID := cachedPatchID(ctx, in.Repo.PatchID)

	claimed := map[string]bool{}
	for _, t := range tasks {
		f := TaskFinding{ID: t.ID, Title: t.Title, RecordedCommit: t.Commit, Match: MatchMissing}
		f.Match, f.Commit, err = match(ctx, in, head, t, candidates, patchID)
		if err != nil {
			return fmt.Errorf("matching %s: %w", t.ID, err)
		}
		if f.Commit != "" {
			claimed[f.Commit] = true
		}
		r.Tasks = append(r.Tasks, f)
	}
	for _, c := range candidates {
		if !claimed[c.Hash] {
			r.UnattributedCommits = append(r.UnattributedCommits, Commit{Hash: c.Hash, Subject: c.Subject})
		}
	}
	return nil
}

func match(ctx context.Context, in Input, head string, t service.TaskView, candidates []git.LogCommit,
	patchID func(string) (string, error)) (Match, string, error) {
	recordedExists := false
	if t.Commit != "" {
		ok, err := in.Repo.CommitExists(ctx, t.Commit)
		if err != nil {
			return "", "", err
		}
		recordedExists = ok
		if ok {
			present, err := in.Repo.IsAncestor(ctx, t.Commit, head)
			if err != nil {
				return "", "", err
			}
			if present {
				return MatchPresent, t.Commit, nil
			}
		}
	}

	if hash, err := trailerCommit(t.ID, candidates); err != nil || hash != "" {
		return MatchTrailer, hash, err
	}

	if recordedExists {
		want, err := patchID(t.Commit)
		if err != nil {
			return "", "", err
		}
		if want != "" {
			for _, c := range candidates {
				got, err := patchID(c.Hash)
				if err != nil {
					return "", "", err
				}
				if got == want {
					return MatchPatchID, c.Hash, nil
				}
			}
		}
	}

	if in.Subjects != nil {
		for _, want := range in.Subjects(t) {
			if want == "" {
				continue
			}
			for _, c := range candidates {
				if c.Subject == want {
					return MatchSubject, c.Hash, nil
				}
			}
		}
	}
	return MatchMissing, "", nil
}

// cachedPatchID memoises fn per commit.
func cachedPatchID(ctx context.Context, fn func(context.Context, string) (string, error)) func(string) (string, error) {
	cache := map[string]string{}
	return func(hash string) (string, error) {
		if id, ok := cache[hash]; ok {
			return id, nil
		}
		id, err := fn(ctx, hash)
		if err != nil {
			return "", err
		}
		cache[hash] = id
		return id, nil
	}
}

// trailerCommit returns the first of candidates whose Ralph-Task trailer
// names taskID, or "".
func trailerCommit(taskID string, candidates []git.LogCommit) (string, error) {
	id, err := domain.ParseID(domain.KindTask, taskID)
	if err != nil {
		return "", err
	}
	for _, c := range candidates {
		for _, v := range c.Trailers[TrailerKey] {
			if n, err := domain.ParseID(domain.KindTask, strings.TrimSpace(v)); err == nil && n == id {
				return c.Hash, nil
			}
		}
	}
	return "", nil
}
