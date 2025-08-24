package main

import (
	"database/sql"
	"fmt"
	"log"
)

func getNextUpdateId(db *sql.DB) int {
	var lastUpdateID int
	err := db.QueryRow("SELECT last_update_id FROM bot_settings WHERE id = 1").Scan(&lastUpdateID)
	if err != nil {
		log.Panicf("Error getting last update ID: %v", err)
	}
	return lastUpdateID + 1
}

// saveLastUpdateID saves the last processed update ID to the database
func saveLastUpdateID(db *sql.DB, updateID int) error {
	_, err := db.Exec("UPDATE bot_settings SET last_update_id = ? WHERE id = 1", updateID)
	if err != nil {
		log.Printf("Error saving last update ID: %v", err)
	}
	return err
}

// getUserReputation gets user reputation from database
func getUserReputation(db *sql.DB, userID int) (reputation int64, err error) {
	err = db.QueryRow("SELECT reputation FROM exchangers WHERE userid = ?", userID).Scan(&reputation)
	return reputation, err
}

type NewOffer struct {
	UserID       int
	Username     string
	HaveAmount   float64
	HaveCurrency string
	WantAmount   float64
	WantCurrency string
	ChannelID    int64
	MessageID    int
	ReplyID      int64
}

// saveOffer saves an offer to the database and returns the new offer ID
func saveOffer(db *sql.DB, offer NewOffer) (int64, error) {
	// Ensure the exchangers table has the user
	if _, err := db.Exec(`
		INSERT OR IGNORE INTO exchangers (userid, reputation, name)
		VALUES (?, 0, ?)`, offer.UserID, offer.Username); err != nil {
		return 0, fmt.Errorf("error inserting into exchangers: %w", err)
	}

	// Insert the offer
	res, err := db.Exec(`
		INSERT INTO offers (userid, username, have_amount, have_currency, want_amount, want_currency, channel_id, message_id, reply_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		offer.UserID, offer.Username,
		offer.HaveAmount, offer.HaveCurrency,
		offer.WantAmount, offer.WantCurrency,
		offer.ChannelID, offer.MessageID, offer.ReplyID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type StoredOffer struct {
	UserID       int
	Username     string
	HaveAmount   float64
	HaveCurrency string
	WantAmount   float64
	WantCurrency string
	ChannelID    int64
	MessageID    int
	PostedAt     string
	Reputation   int64
}

// getFilteredOffers retrieves offers from the database with optional filtering
func getFilteredOffers(db *sql.DB, limit int, querySuffix string, args ...any) ([]StoredOffer, error) {
	query := `
		SELECT o.userid, o.username, o.have_amount, o.have_currency, o.want_amount, o.want_currency, o.channel_id, o.message_id, o.posted_at, e.reputation
		FROM offers o
		LEFT JOIN exchangers e ON o.userid = e.userid
		` + querySuffix + `
		ORDER BY o.posted_at DESC LIMIT ?`
	rows, err := db.Query(query, append(args, limit)...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error querying recent offers: %v", err)
	}
	defer rows.Close()

	offers := make([]StoredOffer, 0, limit)

	for rows.Next() {
		var offer StoredOffer

		err := rows.Scan(&offer.UserID,
			&offer.Username,
			&offer.HaveAmount, &offer.HaveCurrency,
			&offer.WantAmount, &offer.WantCurrency,
			&offer.ChannelID, &offer.MessageID,
			&offer.PostedAt,
			&offer.Reputation)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

// findMatchingOffers finds offers that match the current offer
func findMatchingOffers(db *sql.DB, offer ParsedOffer) ([]StoredOffer, error) {
	var colPrefix string
	if offer.Type == OfferTypeBuy {
		// User wants to buy, find sellers
		colPrefix = "have"
	} else {
		colPrefix = "want"
		// User wants to sell, find buyers
	}
	amount := offer.Amount
	for i := 0; i < 2; i++ {
		r, err := getFilteredOffers(db, 5, "WHERE o."+colPrefix+"_currency = ? AND o."+colPrefix+"_amount >= ?", offer.Currency, amount)
		if err != nil {
			return nil, fmt.Errorf("error finding matching offers: %v", err)
		}
		if len(r) > 0 {
			return r, nil
		}
		if i == 0 {
			// Retry with >= 0 to find offers with 0 amount
			amount = 0
		}
	}

	return nil, nil
}

// tableExists checks if a table exists in the database
func tableExists(db *sql.DB, tableName string) bool {
	var one int
	err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		log.Panicf("error checking if table %s exists: %v", tableName, err)
	}
	return one == 1
}

// getDefaultValueForType returns an appropriate default value for a given SQL type
func getDefaultValueForType(sqlType string) string {
	switch sqlType {
	case "INTEGER":
		return "0"
	case "REAL":
		return "-1e999" // -inf
	case "TEXT":
		return "''"
	case "TIMESTAMP":
		return "CURRENT_TIMESTAMP"
	default:
		return "''"
	}
}

// saveReplyMessageID updates reply_message_id for an offer
func saveReplyMessageID(db *sql.DB, original MessageIndex, replyMessageID int) (int64, error) {
	r, err := db.Exec(`INSERT INTO command_replies (channel_id, message_id, reply_message_id)
						VALUES (?, ?, ?)
				ON CONFLICT(channel_id, message_id) DO
					UPDATE SET reply_message_id = ? WHERE channel_id = ? AND message_id = ?`,
		original.ChannelID, original.MessageID, replyMessageID,
		replyMessageID, original.ChannelID, original.MessageID)
	if err != nil {
		return 0, fmt.Errorf("error saving reply_message_id: %w", err)
	}
	return r.LastInsertId()
}

// findReplyMessageID finds the last reply message ID for a given original message ID
func findReplyMessageID(db *sql.DB, original MessageIndex) (MessageIndex, error) {
	var reply MessageIndex
	err := db.QueryRow(`SELECT reply_message_id FROM command_replies
	WHERE channel_id = ? AND message_id = ?
	ORDER BY posted_at DESC
	LIMIT 1`, original.ChannelID, original.MessageID).Scan(&reply.MessageID)
	if err == sql.ErrNoRows {
		return reply, nil // No reply found
	}
	if err != nil {
		return reply, err
	}
	reply.ChannelID = original.ChannelID
	return reply, nil
}

func deleteOfferByMessage(db *sql.DB, original MessageIndex) error {
	_, err := db.Exec("DELETE FROM offers WHERE channel_id = ? AND message_id = ?", original.ChannelID, original.MessageID)
	if err != nil {
		return fmt.Errorf("error deleting offer: %w", err)
	}
	return nil
}
