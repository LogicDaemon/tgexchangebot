package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// getSavedTableConstraints returns the stored SQL constraints for a table from table_settings.
func getSavedTableConstraints(db *sql.DB, tableName string) (string, error) {
	var constraints string
	err := db.QueryRow("SELECT sql_constraints FROM table_settings WHERE table_name = ?",
		tableName).Scan(&constraints)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return constraints, nil
}

// upsertSavedTableConstraints inserts or updates the constraints for a table in table_settings.
func upsertSavedTableConstraints(db *sql.DB, tableName, constraints string) {
	// Use INSERT OR REPLACE to upsert by primary key (table_name)
	_, err := db.Exec(
		"INSERT OR REPLACE INTO table_settings (table_name, sql_constraints) VALUES (?, ?)",
		tableName, constraints,
	)
	if err != nil {
		log.Panicf("WARNING: Failed to upsert table_settings for %s: %v", tableName, err)
	}
}

// ensureParentUniqueIndexes ensures that referenced parent columns have a UNIQUE index,
// which satisfies SQLite's requirement that parent keys be PRIMARY KEY or UNIQUE.
func ensureParentUniqueIndexes(db *sql.DB, schemas []TableSchema) {
	seen := make(map[string]struct{})
	for _, schema := range schemas {
		for _, col := range schema.Columns {
			if col.RefTable == "" || col.RefColumn == "" {
				continue
			}
			key := col.RefTable + "." + col.RefColumn
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			idxName := fmt.Sprintf("uq_%s_%s", col.RefTable, col.RefColumn)
			stmt := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s(%s)", idxName, col.RefTable, col.RefColumn)
			if _, err := db.Exec(stmt); err != nil {
				log.Printf("WARNING: Could not ensure unique index %s on %s(%s): %v", idxName, col.RefTable, col.RefColumn, err)
			}
		}
	}
}

// updateTableSchema updates a table schema to match the expected schema
func updateTableSchema(db *sql.DB, schema TableSchema) {
	log.Printf("Checking schema for table: %s", schema.Name)
	// Get current columns
	currentColumns := getCurrentTableColumns(db, schema.Name)

	// Check for missing columns and add them
	for _, expectedCol := range schema.Columns {
		if columnExists(currentColumns, expectedCol.Name) {
			continue
		}
		if expectedCol.PrimaryKey {
			log.Panicf("Primary key column %s is missing in table %s, cannot add it", expectedCol.Name, schema.Name)
		}
		log.Printf("Adding missing column %s to table %s", expectedCol.Name, schema.Name)

		// Compose column definition respecting SQLite ALTER TABLE ADD COLUMN constraints.
		// If this column declares a foreign key, inline the REFERENCES clause here because
		// SQLite does not support "ALTER TABLE ... ADD FOREIGN KEY ..." later.
		fkClause := ""
		if expectedCol.RefTable != "" && expectedCol.RefColumn != "" {
			fkClause = fmt.Sprintf(" REFERENCES %s(%s)", expectedCol.RefTable, expectedCol.RefColumn)
		}

		colDef := fmt.Sprintf("%s %s", expectedCol.Name, expectedCol.Type)
		if fkClause != "" {
			colDef += fkClause
		}
		if expectedCol.NotNull {
			colDef += " NOT NULL"
		}
		if expectedCol.DefaultValue != "" {
			colDef += fmt.Sprintf(" DEFAULT %s", expectedCol.DefaultValue)
		} else if expectedCol.NotNull {
			// For NOT NULL columns without default, provide a safe default to satisfy existing rows
			colDef += fmt.Sprintf(" DEFAULT %s", getDefaultValueForType(expectedCol.Type))
		}

		alterQuery := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", schema.Name, colDef)

		if _, err := db.Exec(alterQuery); err != nil {
			log.Panicf("error adding column %s to table %s: %v", expectedCol.Name, schema.Name, err)
		}
		log.Printf("Successfully added column %s to table %s", expectedCol.Name, schema.Name)
	}

	// Check for missing foreign keys (cannot be added post-hoc in SQLite)
	for _, col := range schema.Columns {
		if col.RefTable == "" || col.RefColumn == "" {
			continue
		}
		if foreignKeyExists(db, schema.Name, col.Name, col.RefTable, col.RefColumn) {
			continue
		}
		log.Printf("WARNING: SQLite cannot add a foreign key on existing table %s.%s -> %s(%s).",
			schema.Name, col.Name, col.RefTable, col.RefColumn)
	}

	// Compare stored table-level constraints with expected ones (if table_settings exists)
	if tableExists(db, "table_settings") {
		stored, err := getSavedTableConstraints(db, schema.Name)
		if err == nil {
			if stored != schema.SQLConstraints {
				log.Printf("WARNING: Stored SQL constraints for table %s differ. Stored: %q, Expected: %q",
					schema.Name, stored, schema.SQLConstraints)
			}
		} else {
			log.Printf("WARNING: Could not read stored constraints for table %s: %v", schema.Name, err)
		}
	}
}

// createTable builds a CREATE TABLE query from a TableSchema, returns true if the table was created
func createTable(db *sql.DB, schema TableSchema) bool {
	var query strings.Builder
	query.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n", schema.Name))

	for i, col := range schema.Columns {
		query.WriteString("\t" + col.Name + " " + col.Type)

		// Inline REFERENCES for column-level foreign keys
		if col.RefTable != "" && col.RefColumn != "" {
			query.WriteString(" REFERENCES " + col.RefTable + "(" + col.RefColumn + ")")
		}

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

	// Add table-level constraints (like UNIQUE)
	if schema.SQLConstraints != "" {
		query.WriteString(",\n\t" + schema.SQLConstraints)
	}
	query.WriteString("\n);")

	log.Printf("Executing table creation query for: %s", schema.Name)
	r, err := db.Exec(query.String())
	if err != nil {
		log.Panicf("Error creating table %s: %v", schema.Name, err)
	}
	liid, err := r.LastInsertId()
	if err != nil {
		log.Printf("Error getting LastInsertId for table %s: %v", schema.Name, err)
	}
	raff, err := r.RowsAffected()
	if err != nil {
		log.Printf("Error getting RowsAffected for table %s: %v", schema.Name, err)
	}
	log.Printf("Result of creating table %s: %v %v", schema.Name, liid, raff)
	// Short return false if table existed already
	if rows, err := r.RowsAffected(); err == nil {
		if rows == 0 {
			return false
		}
	}

	// After creating the table, persist its constraints in table_settings
	upsertSavedTableConstraints(db, schema.Name, schema.SQLConstraints)
	return true
}

// initDB initializes the database and creates/updates tables
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
		if !createTable(db, schema) {
			updateTableSchema(db, schema)
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

	// Ensure parent columns referenced by foreign keys have UNIQUE indexes
	ensureParentUniqueIndexes(db, expectedSchemas)

	// Set foreign key constraints
	if _, err = db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		log.Panicf("Error enabling foreign keys: %v", err)
	}

	// Ensure the bot_settings row exists
	_, err = db.Exec("INSERT OR IGNORE INTO bot_settings (id, schema_version, last_update_id) VALUES (1, ?, 0)", dbSchemaVersion)

	log.Println("Database initialized successfully with schema validation")

	return db
}
