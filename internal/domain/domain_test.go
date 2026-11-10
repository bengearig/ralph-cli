package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseID(t *testing.T) {
	good := map[string]int64{"T-12": 12, "t-12": 12, "12": 12, " T-3 ": 3}
	for in, want := range good {
		got, err := ParseID(KindTask, in)
		if err != nil || got != want {
			t.Errorf("ParseID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"M-12", "T-", "T-0", "-1", "abc", "", "T-1.5"} {
		if _, err := ParseID(KindTask, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseID(%q) err = %v, want ErrInvalid", in, err)
		}
	}
	for in, want := range map[string]int64{"N-42": 42, "n-42": 42, "42": 42} {
		if got, err := ParseID(KindNote, in); err != nil || got != want {
			t.Errorf("ParseID(KindNote, %q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseID(KindNote, "T-42"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseID(KindNote, T-42) err = %v, want ErrInvalid", err)
	}
	if FormatID(KindNote, 42) != "N-42" {
		t.Error("FormatID(KindNote) wrong")
	}
	if _, err := ParseKind("note"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseKind(note) err = %v, want ErrInvalid", err)
	}
	if FormatID(KindMilestone, 3) != "M-3" || FormatID(KindDocument, 1) != "D-1" || FormatID(KindInitiative, 9) != "I-9" {
		t.Error("FormatID prefixes wrong")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Add OAuth login":       "add-oauth-login",
		"  --Hello,  World!!  ": "hello-world",
		"Café résumé":           "caf-r-sum",
		"":                      "",
		"!!!":                   "",
		"v2.0 API (beta)":       "v2-0-api-beta",
		"a very long title that goes on and on and on and on and on forever": "a-very-long-title-that-goes-on-and-on-and-on-and-on-and-on-f",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if err := ValidateSlug("ok-slug-2"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "Bad", "a--b", "-a", "a b"} {
		if err := ValidateSlug(bad); err == nil {
			t.Errorf("ValidateSlug(%q) should fail", bad)
		}
	}
}

func TestParsers(t *testing.T) {
	if s, err := ParseStatus("in-progress"); err != nil || s != StatusInProgress {
		t.Errorf("ParseStatus = %v, %v", s, err)
	}
	if s, err := ParseStatus("in progress"); err != nil || s != StatusInProgress {
		t.Errorf("ParseStatus = %v, %v", s, err)
	}
	if _, err := ParseStatus("WAITING"); !errors.Is(err, ErrInvalid) {
		t.Error("unknown status should fail")
	}
	if k, err := ParseDocKind("Architecture"); err != nil || k != DocArchitecture {
		t.Errorf("ParseDocKind = %v, %v", k, err)
	}
	if _, err := ParseDocKind("memo"); !errors.Is(err, ErrInvalid) {
		t.Error("unknown doc kind should fail")
	}
	if k, err := ParseNoteKind("kickback"); err != nil || k != NoteKickback {
		t.Errorf("ParseNoteKind = %v, %v", k, err)
	}
	if k, err := ParseKind("TASK"); err != nil || k != KindTask {
		t.Errorf("ParseKind = %v, %v", k, err)
	}
}

func TestTransitionMatrix(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusProposed, StatusTodo}:       true,
		{StatusProposed, StatusCancelled}:  true,
		{StatusTodo, StatusInProgress}:     true,
		{StatusTodo, StatusProposed}:       true,
		{StatusTodo, StatusCancelled}:      true,
		{StatusInProgress, StatusDone}:     true,
		{StatusInProgress, StatusTodo}:     true,
		{StatusInProgress, StatusBlocked}:  true,
		{StatusInProgress, StatusProposed}: true,
		{StatusBlocked, StatusTodo}:        true,
		{StatusBlocked, StatusProposed}:    true,
		{StatusBlocked, StatusCancelled}:   true,
	}
	for _, from := range Statuses() {
		for _, to := range Statuses() {
			if got := CanTransition(from, to); got != allowed[[2]Status{from, to}] {
				t.Errorf("CanTransition(%s, %s) = %v", from, to, got)
			}
		}
	}
	if !StatusDone.Terminal() || !StatusCancelled.Terminal() || StatusBlocked.Terminal() {
		t.Error("Terminal() wrong")
	}
}

func TestCheckTransitionGuards(t *testing.T) {
	task := Task{ID: 4, Status: StatusProposed, Title: "x"}
	err := task.CheckTransition(StatusTodo, "")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unarticulated task should not reach TODO: %v", err)
	}
	task.Description = "do it"
	if err := task.CheckTransition(StatusTodo, ""); err == nil {
		t.Error("missing acceptance criteria should block TODO")
	}
	task.AcceptanceCriteria = "it is done"
	if err := task.CheckTransition(StatusTodo, ""); err != nil {
		t.Errorf("articulated task: %v", err)
	}

	task.Status = StatusInProgress
	if err := task.CheckTransition(StatusBlocked, "  "); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("BLOCKED without reason: %v", err)
	}
	if err := task.CheckTransition(StatusBlocked, "need creds"); err != nil {
		t.Errorf("BLOCKED with reason: %v", err)
	}

	task.Status = StatusDone
	if err := task.CheckTransition(StatusTodo, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("DONE is terminal: %v", err)
	}
}

func TestApplyTransition(t *testing.T) {
	task := Task{Status: StatusInProgress}
	task.ApplyTransition(StatusTodo, "tests failed")
	task.Status = StatusInProgress
	task.ApplyTransition(StatusTodo, "tests failed again")
	if task.FailureCount != 2 {
		t.Errorf("FailureCount = %d", task.FailureCount)
	}
	task.Status = StatusInProgress
	task.ApplyTransition(StatusBlocked, " stuck ")
	if task.BlockedReason != "stuck" {
		t.Errorf("BlockedReason = %q", task.BlockedReason)
	}
	task.ApplyTransition(StatusTodo, "")
	if task.BlockedReason != "" {
		t.Error("leaving BLOCKED should clear the reason")
	}
	task.Status = StatusInProgress
	task.ApplyTransition(StatusDone, "")
	if task.FailureCount != 0 {
		t.Error("DONE should reset the failure count")
	}
}

func TestTransitionNoteKind(t *testing.T) {
	cases := []struct {
		from, to Status
		want     NoteKind
	}{
		{StatusInProgress, StatusBlocked, NoteKickback},
		{StatusInProgress, StatusProposed, NoteKickback},
		{StatusBlocked, StatusProposed, NoteKickback},
		{StatusTodo, StatusProposed, NoteGeneral},
		{StatusInProgress, StatusTodo, NoteIteration},
		{StatusInProgress, StatusDone, NoteIteration},
		{StatusProposed, StatusTodo, NoteGeneral},
	}
	for _, c := range cases {
		if got := TransitionNoteKind(c.from, c.to); got != c.want {
			t.Errorf("TransitionNoteKind(%s, %s) = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestValidCommit(t *testing.T) {
	sha1 := strings.Repeat("0123456789", 4)
	sha256 := strings.Repeat("abcdef0123456789", 4)
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty", "", true},
		{"sha1", sha1, true},
		{"sha256", sha256, true},
		{"short", "abc1234", false},
		{"uppercase", strings.ToUpper(sha256[:40]), false},
		{"41 chars", sha1 + "a", false},
		{"non-hex", "g" + sha1[1:], false},
		{"padded", " " + sha1[1:], false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidCommit(c.in)
			if c.ok && err != nil {
				t.Errorf("ValidCommit(%q) = %v, want nil", c.in, err)
			}
			if !c.ok {
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("ValidCommit(%q) = %v, want ErrInvalid", c.in, err)
				} else if !strings.Contains(err.Error(), c.in) {
					t.Errorf("ValidCommit(%q) error %q does not name the value", c.in, err)
				}
			}
		})
	}
}
