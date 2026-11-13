package progress

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

const clr = "\r\x1b[2K"

func TestPlain(t *testing.T) {
	tests := []struct {
		name  string
		total int
		items []string
		want  string
	}{
		{"three", 3, []string{"a", "b", "c"}, "syncing: 3 items\n[1/3] a\n[2/3] b\n[3/3] c\n"},
		{"one", 1, []string{"a"}, "syncing: 1 item\n[1/1] a\n"},
		{"beyond total", 1, []string{"a", "b"}, "syncing: 1 item\n[1/1] a\n[1/1] b\n"},
		{"zero total", 0, []string{"a"}, ""},
		{"control chars", 1, []string{"a\nb\rc\x1bd"}, "syncing: 1 item\n[1/1] a b c d\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := New(&buf, Plain, 80)
			r.Start("syncing", tt.total)
			for _, it := range tt.items {
				r.Step(it)
			}
			r.Finish()
			if buf.String() != tt.want {
				t.Errorf("got %q, want %q", buf.String(), tt.want)
			}
			if strings.ContainsAny(buf.String(), "\r\x1b") {
				t.Errorf("plain output has control sequences: %q", buf.String())
			}
		})
	}
}

func bar(filled int) string {
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", 16-filled) + "]"
}

func TestLiveFrames(t *testing.T) {
	tests := []struct {
		n, total, filled int
	}{
		{1, 48, 0},
		{12, 48, 4},
		{48, 48, 16},
		{1, 16, 1},
		{1, 32, 1},
		{15, 16, 15},
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		r := New(&buf, Live, 80)
		r.Start("x", tt.total)
		for i := 1; i < tt.n; i++ {
			r.Step("skip")
		}
		buf.Reset()
		r.Step("item")
		got := buf.String()
		want := clr + bar(tt.filled) + " " + itoa(tt.n) + "/" + itoa(tt.total) + "  item"
		if got != want {
			t.Errorf("%d/%d: got %q, want %q", tt.n, tt.total, got, want)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("%d/%d: newline in %q", tt.n, tt.total, got)
		}
	}
}

func itoa(n int) string {
	var b strings.Builder
	if n >= 10 {
		b.WriteString(itoa(n / 10))
	}
	b.WriteByte(byte('0' + n%10))
	return b.String()
}

func TestLiveStartDrawsZero(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 48)
	got := buf.String()
	if !strings.HasPrefix(got, clr+bar(0)+" 0/48") || strings.Contains(got, "\n") {
		t.Errorf("got %q", got)
	}
}

func TestLiveFinish(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 2)
	r.Step("a")
	buf.Reset()
	r.Finish()
	if buf.String() != clr {
		t.Errorf("finish wrote %q", buf.String())
	}
	buf.Reset()
	r.Finish()
	r.Step("b")
	if buf.Len() != 0 {
		t.Errorf("wrote after finish: %q", buf.String())
	}
}

func TestLiveFinishWithoutDraw(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 0)
	r.Step("a")
	r.Finish()
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
}

func visible(s string) string {
	return strings.TrimPrefix(s, clr)
}

func TestTruncation(t *testing.T) {
	tests := []struct {
		name  string
		width int
		item  string
	}{
		{"ascii", 40, strings.Repeat("x", 100)},
		{"multibyte", 40, strings.Repeat("é世", 50)},
		{"default width", 0, strings.Repeat("y", 200)},
		{"tiny width", 5, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := New(&buf, Live, tt.width)
			r.Start("x", 5)
			buf.Reset()
			r.Step(tt.item)
			line := []rune(visible(buf.String()))
			limit := tt.width
			if limit <= 0 {
				limit = 80
			}
			if tt.name == "tiny width" {
				if strings.Contains(string(line), "abc") {
					t.Errorf("item not dropped: %q", string(line))
				}
				return
			}
			if len(line) != limit {
				t.Errorf("got %d columns, want %d: %q", len(line), limit, string(line))
			}
			if line[len(line)-1] != '…' {
				t.Errorf("no ellipsis: %q", string(line))
			}
		})
	}
}

func TestShortItemNotTruncated(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 40)
	r.Start("x", 5)
	buf.Reset()
	r.Step("short")
	if strings.Contains(buf.String(), "…") {
		t.Errorf("truncated: %q", buf.String())
	}
}

func TestLiveControlChars(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 1)
	buf.Reset()
	r.Step("a\nb\rc\x1bd")
	if got, want := buf.String(), clr+bar(16)+" 1/1  a b c d"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOff(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Off, 80)
	r.Start("x", 3)
	r.Step("a")
	r.Finish()
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
}

func TestLiveZeroTotal(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 0)
	r.Step("a")
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
}

func TestStepsBeyondTotal(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Live, 80)
	r.Start("x", 2)
	for i := 0; i < 5; i++ {
		r.Step("a")
	}
	if !strings.Contains(buf.String(), "2/2") || strings.Contains(buf.String(), "3/2") {
		t.Errorf("count passed total: %q", buf.String())
	}
}

func TestWidthFromEnv(t *testing.T) {
	tests := []struct {
		val  string
		want int
	}{
		{"120", 120}, {"", 80}, {"0", 80}, {"-5", 80}, {"abc", 80}, {"12x", 80},
	}
	for _, tt := range tests {
		got := WidthFromEnv(func(k string) string {
			if k != "COLUMNS" {
				t.Errorf("read %q", k)
			}
			return tt.val
		})
		if got != tt.want {
			t.Errorf("COLUMNS=%q: got %d, want %d", tt.val, got, tt.want)
		}
	}
}

func TestConcurrentSteps(t *testing.T) {
	for _, mode := range []Mode{Plain, Live} {
		var buf bytes.Buffer
		r := New(&buf, mode, 80)
		r.Start("x", 100)
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					r.Step("item")
				}
			}()
		}
		wg.Wait()
		r.Finish()
	}
}
