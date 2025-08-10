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

// foreignKeyExists checks if a foreign key exists in a table for the given
// source column referencing the specified destination table and column.
func foreignKeyExists(db *sql.DB, tableName, columnName, refTable, refColumn string) bool {
	// Use the table-valued PRAGMA so we can filter directly in SQL.
	// Columns are: id, seq, "table", "from", "to", on_update, on_delete, match
	// Note: quoting "table", "from", "to" avoids keyword conflicts.
	var one int
	err := db.QueryRow(
		`SELECT 1 FROM pragma_foreign_key_list(?)
		 WHERE "from" = ? AND "table" = ? AND "to" = ?
		 LIMIT 1`,
		tableName, columnName, refTable, refColumn,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		log.Panicf("error checking foreign keys for table %s: %v", tableName, err)
	}
	return one == 1
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
