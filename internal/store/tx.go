package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) WithTx(
	ctx context.Context,
	fn func(tx pgx.Tx) error,
)error{
	tx,err:=s.DB.Begin(ctx)
	if err!=nil{
		return err
	}

	defer tx.Rollback(ctx)

	if err:=fn(tx);err!=nil{
		return err
	}
	return tx.Commit(ctx)
}