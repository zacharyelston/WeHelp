// Package ledger implements the internal credit system as a double-entry,
// append-only ledger. Balances are never stored or mutated — every movement
// is a transaction with balanced debit/credit postings, and the
// ledger_transactions and ledger_entries tables are made immutable at the
// database level.
package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// Posting is one side of a ledger transaction. Amount is in minor units of
// the account's credit currency and must be > 0.
type Posting struct {
	AccountID uuid.UUID
	Direction Direction
	Amount    int64
}

var (
	ErrTooFewPostings = errors.New("ledger: a transaction requires at least two postings")
	ErrUnbalanced     = errors.New("ledger: debits must equal credits")
	ErrInvalidAmount  = errors.New("ledger: posting amount must be > 0")
	ErrBadDirection   = errors.New("ledger: direction must be debit or credit")
)

// Post validates that the postings balance, then records the transaction and
// its entries inside the caller's database transaction.
func Post(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, memo string, postings []Posting) (uuid.UUID, error) {
	if err := validate(postings); err != nil {
		return uuid.Nil, err
	}

	var txID uuid.UUID
	err := tx.QueryRow(ctx,
		`insert into ledger_transactions (tenant_id, memo) values ($1, $2) returning id`,
		tenantID, memo,
	).Scan(&txID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: insert transaction: %w", err)
	}

	for _, p := range postings {
		_, err := tx.Exec(ctx,
			`insert into ledger_entries (transaction_id, account_id, direction, amount) values ($1, $2, $3, $4)`,
			txID, p.AccountID, string(p.Direction), p.Amount,
		)
		if err != nil {
			return uuid.Nil, fmt.Errorf("ledger: insert entry: %w", err)
		}
	}
	return txID, nil
}

// Balance returns the net balance of an account: debits minus credits.
// Sign conventions are defined per account type by the caller.
func Balance(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (int64, error) {
	var bal int64
	err := tx.QueryRow(ctx, `
		select coalesce(sum(case direction when 'debit' then amount else -amount end), 0)
		from ledger_entries where account_id = $1`, accountID).Scan(&bal)
	return bal, err
}

func validate(postings []Posting) error {
	if len(postings) < 2 {
		return ErrTooFewPostings
	}
	var debits, credits int64
	for _, p := range postings {
		if p.Amount <= 0 {
			return ErrInvalidAmount
		}
		switch p.Direction {
		case Debit:
			debits += p.Amount
		case Credit:
			credits += p.Amount
		default:
			return ErrBadDirection
		}
	}
	if debits != credits {
		return fmt.Errorf("%w (debits=%d credits=%d)", ErrUnbalanced, debits, credits)
	}
	return nil
}
