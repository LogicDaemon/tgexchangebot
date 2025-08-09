package main

import (
	"database/sql"
	"fmt"
	"log"
)

// getCurrentTableColumns gets the current columns for a table
func getCurrentTableColumns(db *sql.DB, tableName string) []TableColumn {
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		log.Panicf("error getting current columns for table %s: %v", tableName, err)
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
			log.Panicf("error scanning row for table %s: %v", tableName, err)
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

	return columns
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

// verifyTableSchema verifies that a table has the expected schema
func verifyTableSchema(db *sql.DB, expectedSchema TableSchema) error {
	currentColumns := getCurrentTableColumns(db, expectedSchema.Name)

	// Check that all expected columns exist
	for _, expectedCol := range expectedSchema.Columns {
		if !columnExists(currentColumns, expectedCol.Name) {
			return fmt.Errorf("missing column: %s", expectedCol.Name)
		}
	}

	return nil
}
