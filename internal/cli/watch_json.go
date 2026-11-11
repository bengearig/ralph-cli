package cli

import (
	"bytes"
	"encoding/json"
	"time"
)

type jsonLinesRenderer struct {
	a *app
}

func (a *app) newJSONLinesRenderer() *jsonLinesRenderer {
	return &jsonLinesRenderer{a: a}
}

// newWatchRenderer picks the renderer commands hand to runWatch.
func (a *app) newWatchRenderer(interval time.Duration) watchRenderer {
	if a.jsonOut || !a.streams.OutIsTerminal {
		return a.newJSONLinesRenderer()
	}
	return a.newTerminalRenderer(interval, time.Now)
}

// snapshotJSON is the compact form of what a command's --json output encodes.
func snapshotJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (r *jsonLinesRenderer) Start() error { return nil }

func (r *jsonLinesRenderer) Update(_, cur *snapshot) error {
	line := cur.JSON
	if bytes.ContainsAny(line, "\r\n") {
		var buf bytes.Buffer
		if err := json.Compact(&buf, line); err != nil {
			return err
		}
		line = buf.Bytes()
	}
	out := make([]byte, 0, len(line)+1)
	out = append(append(out, line...), '\n')
	if _, err := r.a.streams.Out.Write(out); err != nil {
		return err
	}
	if f, ok := r.a.streams.Out.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

func (r *jsonLinesRenderer) Tick(time.Time) error { return nil }

func (r *jsonLinesRenderer) Notice(message string) error {
	if message == "" {
		return nil
	}
	_, err := r.a.streams.Err.Write([]byte("ralph: " + message + "\n"))
	return err
}

func (r *jsonLinesRenderer) Stop() error { return nil }
