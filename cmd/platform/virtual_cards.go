package main

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// setCardBalanceLink is used by both directory create and edit transactions.
// The database trigger protects topology and used cards, including direct SQL.
func setCardBalanceLink(tx *sql.Tx, actor, cardID, physicalID string) error {
	if physicalID == cardID {
		return errors.New("карта не может быть привязана к себе")
	}
	if physicalID != "" && !validID(physicalID) {
		return errors.New("выберите пластиковую карту")
	}
	var previous sql.NullString
	if err := tx.QueryRow("SELECT physical_card_id FROM cards WHERE id=$1 FOR UPDATE", cardID).Scan(&previous); err != nil {
		return err
	}
	if previous.String == physicalID {
		return nil
	}
	if physicalID != "" {
		var status string
		if err := tx.QueryRow("SELECT status FROM cards WHERE id=$1 AND physical_card_id IS NULL FOR UPDATE", physicalID).Scan(&status); err != nil || status == "retired" {
			return errors.New("выберите пластиковую карту вне архива")
		}
	}
	if _, err := tx.Exec("UPDATE cards SET physical_card_id=$2 WHERE id=$1", cardID, nilID(physicalID)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "P0001" {
			switch pgErr.Message {
			case "Привязать можно только к пластиковой карте", "У карты уже есть виртуальные карты", "Привязку нельзя менять: карта уже использована":
				return errors.New(pgErr.Message)
			}
		}
		return err
	}
	return txAudit(tx, actor, "web", "card_balance_link_change", "card", cardID, "success", "", M{"before": nilID(previous.String), "after": nilID(physicalID)})
}
