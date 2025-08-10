package main

const (
	dbSchemaVersion = 1 // Increment this when changing the database schema
)

// TableColumn represents a database column definition
type TableColumn struct {
	Name         string
	Type         string
	NotNull      bool
	DefaultValue string
	PrimaryKey   bool
}

// ForeignKey represents a foreign key constraint
type ForeignKey struct {
	ColumnName string
	RefTable   string
	RefColumn  string
}

// TableSchema represents the expected schema for a table
type TableSchema struct {
	Name           string
	Columns        []TableColumn
	ForeignKeys    []ForeignKey
	SQLConstraints string // Additional SQL suffix for the table (e.g., UNIQUE constraints)
}

// getExpectedSchemas returns the expected database schemas
func getExpectedSchemas() []TableSchema {
	return []TableSchema{
		{
			Name: "bot_settings",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "schema_version", Type: "INTEGER", NotNull: true},
				{Name: "last_update_id", Type: "INTEGER", NotNull: true, DefaultValue: "-1"},
			},
		},
		{
			Name: "exchangers",
			Columns: []TableColumn{
				{Name: "userid", Type: "INTEGER", PrimaryKey: true},
				{Name: "reputation", Type: "INTEGER", NotNull: true},
				{Name: "name", Type: "TEXT", NotNull: true},
				{Name: "date_added", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
		},
		{
			Name: "command_replies",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "channel_id", Type: "INTEGER", NotNull: true},
				{Name: "message_id", Type: "INTEGER", NotNull: true},
				{Name: "reply_message_id", Type: "INTEGER", NotNull: true},
				{Name: "posted_at", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
			SQLConstraints: "UNIQUE(channel_id, message_id)",
		},
		{
			Name: "offers",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "userid", Type: "INTEGER", NotNull: true},
				{Name: "username", Type: "TEXT", NotNull: true},
				{Name: "have_amount", Type: "REAL"},
				{Name: "have_currency", Type: "TEXT"},
				{Name: "want_amount", Type: "REAL"},
				{Name: "want_currency", Type: "TEXT"},
				{Name: "channel_id", Type: "INTEGER", NotNull: true},
				{Name: "message_id", Type: "INTEGER", NotNull: true},
				{Name: "reply_id", Type: "INTEGER"},
				{Name: "posted_at", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
			ForeignKeys: []ForeignKey{
				{ColumnName: "userid", RefTable: "exchangers", RefColumn: "userid"},
				{ColumnName: "reply_id", RefTable: "command_replies", RefColumn: "id"},
			},
			SQLConstraints: "UNIQUE(channel_id, message_id)",
		},
		{
			Name: "reviews",
			Columns: []TableColumn{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "offer_id", Type: "INTEGER"},
				{Name: "reviewer_id", Type: "INTEGER", NotNull: true},
				{Name: "reviewer_name", Type: "TEXT", NotNull: true},
				{Name: "reviewee_id", Type: "INTEGER", NotNull: true},
				{Name: "reviewee_name", Type: "TEXT", NotNull: true},
				{Name: "comment", Type: "TEXT"},
				{Name: "rating", Type: "INTEGER", NotNull: true},
				{Name: "posted_at", Type: "TIMESTAMP", DefaultValue: "CURRENT_TIMESTAMP"},
			},
			ForeignKeys: []ForeignKey{
				{ColumnName: "offer_id", RefTable: "offers", RefColumn: "id"},
				{ColumnName: "reviewer_id", RefTable: "exchangers", RefColumn: "userid"},
				{ColumnName: "reviewee_id", RefTable: "exchangers", RefColumn: "userid"},
			},
		},
	}
}
