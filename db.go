package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// TableColumn represents a database column definition
type TableColumn struct {
	Name         string
	Type         string
	NotNull      bool
	DefaultValue string
	PrimaryKey   bool
}

// TableSchema represents the expected schema for a table
type TableSchema struct {
	Name    string
	Columns []TableColumn
}

// getUserReputation gets user reputation from database
func getUserReputation(db *sql.DB, userID int) (int, error) {
	var reputation int
	err := db.QueryRow("SELECT reputation FROM exchangers WHERE userid = ?", userID).Scan(&reputation)
	if err == sql.ErrNoRows {
		// User doesn't exist, create with 0 reputation
		_, err = db.Exec("INSERT INTO exchangers (userid, reputation, name) VALUES (?, 0, '')", userID)
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	return reputation, err
}

// saveOffer saves an offer to the database
func saveOffer(db *sql.DB, userID int, username string, offer *ParsedOffer, channelID int64, messageID int) error {
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

	_, err := db.Exec(`
		INSERT INTO offers (userid, username, have_amount, have_currency, want_amount, want_currency, channel_id, message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, username, haveAmount, haveCurrency, wantAmount, wantCurrency, channelID, messageID)
	return err
}

// findMatchingOffers finds offers that match the current offer
func findMatchingOffers(db *sql.DB, offer *ParsedOffer) ([]map[string]interface{}, error) {
	var query string
	if offer.Type == OfferTypeBuy {
		// User wants to buy, find sellers
		query = `
			SELECT o.userid, o.username, o.have_amount, o.have_currency, e.reputation
			FROM offers o
			LEFT JOIN exchangers e ON o.userid = e.userid
			WHERE o.have_currency = ? AND o.have_amount > 0
			ORDER BY o.posted_at DESC
			LIMIT 5`
	} else {
		// User wants to sell, find buyers
		query = `
			SELECT o.userid, o.username, o.want_amount, o.want_currency, e.reputation
			FROM offers o
			LEFT JOIN exchangers e ON o.userid = e.userid
			WHERE o.want_currency = ? AND o.want_amount > 0
			ORDER BY o.posted_at DESC
			LIMIT 5`
	}

	rows, err := db.Query(query, offer.Currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []map[string]interface{}
	for rows.Next() {
		var userID int
		var username string
		var amount float64
		var currency string
		var reputation sql.NullInt64

		err := rows.Scan(&userID, &username, &amount, &currency, &reputation)
		if err != nil {
			continue
		}

		rep := 0
		if reputation.Valid {
			rep = int(reputation.Int64)
		}

		matches = append(matches, map[string]interface{}{
			"userid":     userID,
			"username":   username,
			"amount":     amount,
			"currency":   currency,
			"reputation": rep,
		})
	}

	return matches, nil
}

// getExpectedSchemas returns the expected database schemas
func getExpectedSchemas() []TableSchema {
	return []TableSchema{
		{
			Name: "exchangers",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "userid", Type: "INTEGER", NotNull: true},
				{Name: "reputation", Type: "INTEGER", NotNull: true},
				{Name: "name", Type: "TEXT", NotNull: true},
			},
		},
		{
			Name: "offers",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "userid", Type: "INTEGER", NotNull: true},
				{Name: "username", Type: "TEXT", NotNull: true},
				{Name: "have_amount", Type: "REAL", NotNull: true},
				{Name: "have_currency", Type: "TEXT", NotNull: true},
				{Name: "want_amount", Type: "REAL", NotNull: true},
				{Name: "want_currency", Type: "TEXT", NotNull: true},
				{Name: "channel_id", Type: "INTEGER", NotNull: true},
				{Name: "message_id", Type: "INTEGER", NotNull: true},
				{Name: "posted_at", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
		},
		{
			Name: "reviews",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "offer_id", Type: "INTEGER", NotNull: true},
				{Name: "reviewer_id", Type: "INTEGER", NotNull: true},
				{Name: "reviewer_name", Type: "TEXT", NotNull: true},
				{Name: "reviewee_id", Type: "INTEGER", NotNull: true},
				{Name: "reviewee_name", Type: "TEXT", NotNull: true},
				{Name: "comment", Type: "TEXT"},
				{Name: "rating", Type: "INTEGER", NotNull: true},
				{Name: "posted_at", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
		},
	}
}

// getCurrentTableColumns gets the current columns for a table
func getCurrentTableColumns(db *sql.DB, tableName string) ([]TableColumn, error) {
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []TableColumn
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey bool
		var defaultValue sql.NullString

		err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey)
		if err != nil {
			return nil, err
		}

		column := TableColumn{
			Name:       name,
			Type:       dataType,
			NotNull:    notNull,
			PrimaryKey: primaryKey,
		}

		if defaultValue.Valid {
			column.DefaultValue = defaultValue.String
		}

		columns = append(columns, column)
	}

	return columns, nil
}

// tableExists checks if a table exists in the database
func tableExists(db *sql.DB, tableName string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// columnExists checks if a column exists in a table
func columnExists(columns []TableColumn, columnName string) bool {
	for _, col := range columns {
		if col.Name == columnName {
			return true
		}
	}
	return false
}

// updateTableSchema updates a table schema to match the expected schema
func updateTableSchema(db *sql.DB, expectedSchema TableSchema) error {
	log.Printf("Checking schema for table: %s", expectedSchema.Name)

	// Check if table exists
	exists, err := tableExists(db, expectedSchema.Name)
	if err != nil {
		return fmt.Errorf("error checking if table %s exists: %w", expectedSchema.Name, err)
	}

	if !exists {
		log.Printf("Table %s does not exist, will be created", expectedSchema.Name)
		return nil // Table will be created by CREATE TABLE IF NOT EXISTS
	}

	// Get current columns
	currentColumns, err := getCurrentTableColumns(db, expectedSchema.Name)
	if err != nil {
		return fmt.Errorf("error getting current columns for table %s: %w", expectedSchema.Name, err)
	}

	// Check for missing columns and add them
	for _, expectedCol := range expectedSchema.Columns {
		if !columnExists(currentColumns, expectedCol.Name) {
			log.Printf("Adding missing column %s to table %s", expectedCol.Name, expectedSchema.Name)

			var alterQuery string
			if expectedCol.DefaultValue != "" {
				alterQuery = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s DEFAULT %s",
					expectedSchema.Name, expectedCol.Name, expectedCol.Type, expectedCol.DefaultValue)
			} else if expectedCol.NotNull {
				// For NOT NULL columns without default, we need to provide a default
				defaultVal := getDefaultValueForType(expectedCol.Type)
				alterQuery = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s NOT NULL DEFAULT %s",
					expectedSchema.Name, expectedCol.Name, expectedCol.Type, defaultVal)
			} else {
				alterQuery = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
					expectedSchema.Name, expectedCol.Name, expectedCol.Type)
			}

			_, err = db.Exec(alterQuery)
			if err != nil {
				return fmt.Errorf("error adding column %s to table %s: %w", expectedCol.Name, expectedSchema.Name, err)
			}
			log.Printf("Successfully added column %s to table %s", expectedCol.Name, expectedSchema.Name)
		}
	}

	return nil
}

// buildCreateTableQuery builds a CREATE TABLE query from a TableSchema
func buildCreateTableQuery(schema TableSchema) string {
	var query strings.Builder
	query.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n", schema.Name))

	for i, col := range schema.Columns {
		query.WriteString("\t" + col.Name + " " + col.Type)

		if col.PrimaryKey {
			query.WriteString(" PRIMARY KEY")
			if col.Type == "INTEGER" {
				query.WriteString(" AUTOINCREMENT")
			}
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
		query.WriteString("\n")
	}

	// Add foreign key constraints
	switch schema.Name {
	case "offers":
		query.WriteString(",\tFOREIGN KEY (userid) REFERENCES exchangers(userid)\n")
	case "reviews":
		query.WriteString(",\tFOREIGN KEY (offer_id) REFERENCES offers(id),\n")
		query.WriteString("\tFOREIGN KEY (reviewer_id) REFERENCES exchangers(userid),\n")
		query.WriteString("\tFOREIGN KEY (reviewee_id) REFERENCES exchangers(userid)\n")
	}

	query.WriteString(");")
	return query.String()
}

// getDefaultValueForType returns an appropriate default value for a given SQL type
func getDefaultValueForType(sqlType string) string {
	switch sqlType {
	case "INTEGER":
		return "0"
	case "REAL":
		return "0.0"
	case "TEXT":
		return "''"
	case "TIMESTAMP":
		return "CURRENT_TIMESTAMP"
	default:
		return "''"
	}
}

func initDB(dbPath string) *sql.DB {
	log.Printf("Initializing database at %s", dbPath)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		panic(fmt.Errorf("Error opening database: %w", err))
	}

	// Get expected schemas
	expectedSchemas := getExpectedSchemas()

	// Update schemas before creating tables
	for _, schema := range expectedSchemas {
		err = updateTableSchema(db, schema)
		if err != nil {
			db.Close()
			panic(fmt.Errorf("Error updating schema for table %s: %w", schema.Name, err))
		}
	}

	// Create tables with the expected schema
	for _, schema := range expectedSchemas {
		query := buildCreateTableQuery(schema)
		log.Printf("Executing table creation query for: %s", schema.Name)
		_, err = db.Exec(query)
		if err != nil {
			db.Close()
			panic(fmt.Errorf("Error creating table %s: %v", schema.Name, err))
		}
	}

	// Verify schemas after creation
	for _, schema := range expectedSchemas {
		err = verifyTableSchema(db, schema)
		if err != nil {
			log.Printf("Warning: Schema verification failed for table %s: %v", schema.Name, err)
		} else {
			log.Printf("Schema verified for table: %s", schema.Name)
		}
	}

	// Set foreign key constraints
	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		db.Close()
		panic(fmt.Errorf("Error enabling foreign keys: %w", err))
	}

	log.Println("Database initialized successfully with schema validation")

	return db
}

// // getTableNameFromQuery extracts table name from CREATE TABLE query
// func getTableNameFromQuery(query string) string {
// 	parts := strings.Fields(query)
// 	for i, part := range parts {
// 		if strings.ToUpper(part) == "TABLE" && i+2 < len(parts) {
// 			tableName := parts[i+2]
// 			return strings.Trim(tableName, "(")
// 		}
// 	}
// 	return "unknown"
// }

// verifyTableSchema verifies that a table has the expected schema
func verifyTableSchema(db *sql.DB, expectedSchema TableSchema) error {
	currentColumns, err := getCurrentTableColumns(db, expectedSchema.Name)
	if err != nil {
		return fmt.Errorf("error getting current columns: %w", err)
	}

	// Check that all expected columns exist
	for _, expectedCol := range expectedSchema.Columns {
		if !columnExists(currentColumns, expectedCol.Name) {
			return fmt.Errorf("missing column: %s", expectedCol.Name)
		}
	}

	return nil
}
