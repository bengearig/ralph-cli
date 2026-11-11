package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		code      int
		wantPrint bool
	}{
		{"nil", nil, 0, false},
		{"interrupted", errs.ErrInterrupted, 130, false},
		{"wrapped interrupted", fmt.Errorf("x: %w", errs.ErrInterrupted), 130, false},
		{"needs human", fmt.Errorf("stopped: %w", errs.ErrNeedsHuman), 3, true},
		{"not implemented", errs.ErrNotImplemented, 2, true},
		{"other", errors.New("boom"), 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, print := exitCode(tt.err)
			if code != tt.code || print != tt.wantPrint {
				t.Fatalf("exitCode = (%d, %v), want (%d, %v)", code, print, tt.code, tt.wantPrint)
			}
		})
	}
}
