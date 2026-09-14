package ledger

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidate(t *testing.T) {
	a, b := uuid.New(), uuid.New()

	tests := []struct {
		name     string
		postings []Posting
		wantErr  error
	}{
		{
			name:     "balanced pair",
			postings: []Posting{{a, Debit, 100}, {b, Credit, 100}},
		},
		{
			name:     "balanced multi-posting",
			postings: []Posting{{a, Debit, 60}, {a, Debit, 40}, {b, Credit, 100}},
		},
		{
			name:     "single posting rejected",
			postings: []Posting{{a, Debit, 100}},
			wantErr:  ErrTooFewPostings,
		},
		{
			name:     "unbalanced rejected",
			postings: []Posting{{a, Debit, 100}, {b, Credit, 99}},
			wantErr:  ErrUnbalanced,
		},
		{
			name:     "zero amount rejected",
			postings: []Posting{{a, Debit, 0}, {b, Credit, 0}},
			wantErr:  ErrInvalidAmount,
		},
		{
			name:     "negative amount rejected",
			postings: []Posting{{a, Debit, -5}, {b, Credit, 5}},
			wantErr:  ErrInvalidAmount,
		},
		{
			name:     "bad direction rejected",
			postings: []Posting{{a, "sideways", 100}, {b, Credit, 100}},
			wantErr:  ErrBadDirection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.postings)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
