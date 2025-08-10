package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// updateTableSchema updates a table schema to match the expected schema
func updateTableSchema(db *sql.DB, expectedSchema TableSchema) {
	if !tableExists(db, expectedSchema.Name) {
		log.Printf("Table %s does not exist", expectedSchema.Name)
		return // Table will be created by CREATE TABLE IF NOT EXISTS
	}

	log.Printf("Checking schema for table: %s", expectedSchema.Name)
	// Get current columns
	currentColumns := getCurrentTableColumns(db, expectedSchema.Name)

	var checkSuffix bool
	// Check for missing columns and add them
	for _, expectedCol := range expectedSchema.Columns {
		if columnExists(currentColumns, expectedCol.Name) {
			continue
		}
		checkSuffix = true
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

	// Check for missing foreign keys
	for _, fk := range expectedSchema.ForeignKeys {
		if foreignKeyExists(db, expectedSchema.Name, fk.ColumnName, fk.RefTable, fk.RefColumn) {
			continue
		}
		checkSuffix = true
		log.Printf("Adding missing foreign key %s to table %s", fk.ColumnName, expectedSchema.Name)
		query := fmt.Sprintf("ALTER TABLE %s ADD FOREIGN KEY (%s) REFERENCES %s(%s)",
			expectedSchema.Name, fk.ColumnName, fk.RefTable, fk.RefColumn)
		if _, err := db.Exec(query); err != nil {
			log.Panicf("error adding foreign key %s to table %s: %v", fk.ColumnName, expectedSchema.Name, err)
		}
		log.Printf("Successfully added foreign key %s to table %s", fk.ColumnName, expectedSchema.Name)
	}

	if !checkSuffix {
		return
	}

	// Check for additional SQL suffix (e.g., UNIQUE constraints)
	if expectedSchema.SQLConstraints != "" {
		log.Printf("Adding additional SQL suffix to table %s: %s", expectedSchema.Name, expectedSchema.SQLConstraints)
		query := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s", expectedSchema.Name, expectedSchema.SQLConstraints)
		if _, err := db.Exec(query); err != nil {
			log.Panicf("error adding SQL suffix to table %s: %v", expectedSchema.Name, err)
		}
		log.Printf("Successfully added SQL suffix to table %s", expectedSchema.Name)
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
	if schema.SQLConstraints != "" {
		query.WriteString("\n" + schema.SQLConstraints)
	}
	query.WriteString("\n);")
	return query.String()
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
