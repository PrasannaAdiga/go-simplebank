package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Store provides all functions to execute db queries and transactions
// It extentds functionality of Queries by composing it
// And provides transactions functionalities
type Stroe struct {
	*Queries
	db *sql.DB
}

func NewStore(db *sql.DB) *Stroe {
	return &Stroe{
		db:      db,
		Queries: New(db), // here we are passing *sql.Db
	}
}

// ExecTx executes a function within a database transaction
func (s *Stroe) execTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	q := New(tx) // here we are passing *sql.Tx

	err = fn(q)

	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("tx err: %v, rb err: %v", err, rbErr)
		}
		return err
	}

	return tx.Commit()
}

// TransferTx performs a money transfer from one account to the other.
// It performs  the below steps
// 1. create the transfer (in transfer table)
// 2. add an account entries (for both from and to in entries table)
// 3. update accounts' balance  (for both from and to in accounts table)
// within a database transaction
func (s *Stroe) TransferTx(ctx context.Context, arg TransferTxParams) (TransferTxResult, error) {
	var result TransferTxResult

	err := s.execTx(ctx, func(q *Queries) error {
		var err error

		result.Transfer, err = q.CreateTransfer(ctx, CreateTransferParams{
			FromAccountID: arg.FromAccountID,
			ToAccountID:   arg.ToAccountID,
			Amount:        arg.Amount,
		})
		if err != nil {
			return err
		}

		result.FromEntry, err = q.CreateEntry(ctx, CreateEntryParams{
			AccountID: arg.FromAccountID,
			Amount:    -arg.Amount,
		})
		if err != nil {
			return err
		}

		result.ToEntry, err = q.CreateEntry(ctx, CreateEntryParams{
			AccountID: arg.ToAccountID,
			Amount:    arg.Amount,
		})
		if err != nil {
			return err
		}

		// One option is to get the account from DB, update it and then save it back
		// However the above one often done incorrectly without a proper locking mechanism
		// For example if we are running multiple go routines where each of them follows the above steps
		// Then multiple get the account DB will result in old values before the update operation takes place.
		// That is why we need use the below SQL query(with begin and commit tx), so that all the get queries will be blocked until we commit
		// The other update queries
		/*
			BEGIN;
			SELECT * FROM accounts where id = 1 FOR update;
			COMMIT;
		*/
		// The above sql query also throws deadlock and hence we should use below one
		/*
			/*
			BEGIN;
			SELECT * FROM accounts where id = 1 FOR NO KEY update;
			COMMIT;
		*/

		// Also, instead of running 2 queries first to get the account by id and then add balance
		// to that account by running update query,
		// we can directly run update query by adding balance(AddAccountBalance function)

		// We should take care of order of the update query.
		// If one goroutine trying to transfer from account id 1 to 2
		// And another one is trying to tansfer from account id 2 to 1
		// Then if we do the query like below, that will results in deadlock
		/*
			routine 1
				update account id 1 // holds the id 1 lock
				update account id 2 // needs id 2 lock
			routine 2
				update account id 2 // holds the id 2 lock
				update account id 1// needs id 1 lock
		*/
		// Intead we should have the order of the update like below
		/*
			routine 1
				BEGIN transaction
				update account id 1 // holds the id 1 lock
				update account id 2 // needs id 2 lock
				COMMIT transaction
			routine 2
				BEGIN transaction
				update account id 1 // waits for id 1 lock and gets it when the first routine commits
				update account id 2 // updtaes it since it will get the id 2 lock immediately
				COMMIT transaction
		*/

		// Update account's balance
		if arg.FromAccountID < arg.ToAccountID {
			result.FromAccount, result.ToAccount, err = addMoney(ctx, q, arg.FromAccountID, -arg.Amount, arg.ToAccountID, arg.Amount)
		} else {
			result.ToAccount, result.FromAccount, err = addMoney(ctx, q, arg.ToAccountID, arg.Amount, arg.FromAccountID, -arg.Amount)
		}

		return err
	})

	return result, err
}

func addMoney(
	ctx context.Context,
	q *Queries,
	accountID1 int64,
	amount1 int64,
	accountID2 int64,
	amount2 int64,
) (account1 Account, account2 Account, err error) {
	account1, err = q.AddAccountBalance(ctx, AddAccountBalanceParams{
		ID:     accountID1,
		Amount: amount1,
	})
	if err != nil {
		return
	}

	account2, err = q.AddAccountBalance(ctx, AddAccountBalanceParams{
		ID:     accountID2,
		Amount: amount2,
	})
	return
}

// TransferTxParams contains the input parameters of the transfer transaction
type TransferTxParams struct {
	FromAccountID int64 `json:"from_account_id"`
	ToAccountID   int64 `json:"to_account_id"`
	Amount        int64 `json:"amount"`
}

// TransferTxResult is the result of the transfer transaction
type TransferTxResult struct {
	Transfer    Transfer `json:"transfer"`
	FromAccount Account  `json:"from_account"`
	ToAccount   Account  `json:"to_account"`
	FromEntry   Entry    `json:"from_entry"`
	ToEntry     Entry    `json:"to_entry"`
}
