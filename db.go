package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
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

// saveOffer saves an offer to the database and returns the new offer ID
func saveOffer(db *sql.DB, userID int, username string, offer *ParsedOffer, channelID int64, messageID int) (int64, error) {
	var haveAmount, wantAmount float64
	var haveCurrency, wantCurrency string

	if offer.Type == OfferTypeSell {
		// User has currency and wants to sell it
		haveAmount = offer.Amount
		haveCurrency = offer.Currency
		wantAmount = 0
		wantCurrency = ""
	} else {
		// User wants to buy currency
		haveAmount = 0
		haveCurrency = ""
		wantAmount = offer.Amount
		wantCurrency = offer.Currency
	}
	// Ensure the exchangers table has the user
	if _, err := db.Exec(`
		INSERT OR IGNORE INTO exchangers (userid, reputation, name)
		VALUES (?, 0, ?)`, userID, username); err != nil {
		return 0, fmt.Errorf("error inserting into exchangers: %w", err)
	}

	// Insert the offer
	res, err := db.Exec(`
		INSERT INTO offers (userid, username, have_amount, have_currency, want_amount, want_currency, channel_id, message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, username, haveAmount, haveCurrency, wantAmount, wantCurrency, channelID, messageID)
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
func findMatchingOffers(db *sql.DB, offer *ParsedOffer) ([]StoredOffer, error) {
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
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&count)
	if err != nil {
		log.Panicf("error checking if table %s exists: %v", tableName, err)
	}
	return count > 0
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

// updateTableSchema updates a table schema to match the expected schema
func updateTableSchema(db *sql.DB, expectedSchema TableSchema) {
	if !tableExists(db, expectedSchema.Name) {
		log.Printf("Table %s does not exist", expectedSchema.Name)
		return // Table will be created by CREATE TABLE IF NOT EXISTS
	}

	log.Printf("Checking schema for table: %s", expectedSchema.Name)
	// Get current columns
	currentColumns := getCurrentTableColumns(db, expectedSchema.Name)

	// Check for missing columns and add them
	for _, expectedCol := range expectedSchema.Columns {
		if columnExists(currentColumns, expectedCol.Name) {
			continue
		}
		if expectedCol.PrimaryKey {
			log.Panicf("Primary key column %s is missing in table %s, cannot add it", expectedCol.Name, expectedSchema.Name)
		}
		log.Printf("Adding missing column %s to table %s", expectedCol.Name, expectedSchema.Name)

		var alterQuery string
		sqlNotNullSuffix := ""
		if expectedCol.NotNull {
			sqlNotNullSuffix = " NOT NULL"
		}
		sqlDefaultSuffix := ""
		if expectedCol.DefaultValue != "" {
			sqlDefaultSuffix = fmt.Sprintf(" DEFAULT %s", expectedCol.DefaultValue)
		} else if expectedCol.NotNull {
			// For NOT NULL columns without default, we provide a default
			// so if the column is not used anymore someday, it does not cause failures
			sqlDefaultSuffix = fmt.Sprintf(" DEFAULT %s", getDefaultValueForType(expectedCol.Type))
		}
		alterQuery = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s %s %s",
			expectedSchema.Name, expectedCol.Name, expectedCol.Type, sqlNotNullSuffix, sqlDefaultSuffix)

		if _, err := db.Exec(alterQuery); err != nil {
			log.Panicf("error adding column %s to table %s: %v", expectedCol.Name, expectedSchema.Name, err)
		}
		log.Printf("Successfully added column %s to table %s", expectedCol.Name, expectedSchema.Name)
	}
}

// buildCreateTableQuery builds a CREATE TABLE query from a TableSchema
func buildCreateTableQuery(schema TableSchema) string {
	var query strings.Builder
	query.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n", schema.Name))

	for i, col := range schema.Columns {
		query.WriteString("\t" + col.Name + " " + col.Type)

		if col.PrimaryKey {
			query.WriteString(" PRIMARY KEY")
		}

		if col.NotNull && !col.PrimaryKey {
			query.WriteString(" NOT NULL")
		}

		if col.DefaultValue != "" {
			query.WriteString(" DEFAULT " + col.DefaultValue)
		}

		// Add special constraints for specific columns
		if col.Name == "rating" {
			query.WriteString(" CHECK (rating >= -1 AND rating <= 1)")
		}

		// Add comma if not the last column
		if i < len(schema.Columns)-1 {
			query.WriteString(",")
		}
	}

	// Add foreign key constraints
	if len(schema.ForeignKeys) > 0 {
		for _, fk := range schema.ForeignKeys {
			query.WriteString(",\n\tFOREIGN KEY (" + fk.ColumnName + ") REFERENCES " + fk.RefTable + "(" + fk.RefColumn + ")")
		}
	}

	query.WriteString("\n);")
	return query.String()
}

func initDB(dbPath string) *sql.DB {
	log.Printf("Initializing database at %s", dbPath)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Panicf("Error opening database: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			db.Close()
			log.Printf("Database initialization failed: %v", r)
			panic(r) // Re-throw the panic after closing the database
		}
	}()

	// Get expected schemas
	expectedSchemas := getExpectedSchemas()

	// Update schemas before creating tables
	for _, schema := range expectedSchemas {
		updateTableSchema(db, schema)
	}

	// Create tables with the expected schema
	for _, schema := range expectedSchemas {
		query := buildCreateTableQuery(schema)
		log.Printf("Executing table creation query for: %s", schema.Name)
		_, err = db.Exec(query)
		if err != nil {
			db.Close()
			log.Panicf("Error creating table %s: %v", schema.Name, err)
		}
	}

	// Verify schemas after creation
	for _, schema := range expectedSchemas {
		err = verifyTableSchema(db, schema)
		if err != nil {
			log.Panicf("Warning: Schema verification failed for table %s: %v", schema.Name, err)
		} else {
			log.Printf("Schema verified for table: %s", schema.Name)
		}
	}

	// Set foreign key constraints
	if _, err = db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		log.Panicf("Error enabling foreign keys: %v", err)
	}

	// Ensure the bot_settings row exists
	_, err = db.Exec("INSERT OR IGNORE INTO bot_settings (id, schema_version, last_update_id) VALUES (1, ?, 0)", dbSchemaVersion)

	log.Println("Database initialized successfully with schema validation")

	return db
}

// updateOfferReplyMessageID updates reply_message_id for an offer
func updateOfferReplyMessageID(db *sql.DB, offerID int64, replyMessageID int) error {
	_, err := db.Exec("UPDATE offers SET reply_message_id = ? WHERE id = ?", replyMessageID, offerID)
	return err
}
