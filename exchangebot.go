package main

import (
	"database/sql"
	"fmt"
	"log"
)

func initDB(dbPath string) *sql.DB {
	log.Printf("Initializing database at %s", dbPath)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		panic(fmt.Errorf("Error opening database: %w", err))
	}

	// Create table with URL and timestamp
	queries := []string{`
	CREATE TABLE IF NOT EXISTS exchangers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	userid INTEGER NOT NULL,
	reputation INTEGER NOT NULL,
	name TEXT NOT NULL
	);`, `
	CREATE TABLE IF NOT EXISTS offers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	userid INTEGER NOT NULL,
	username TEXT NOT NULL,
	have_amount REAL NOT NULL,
	have_currency TEXT NOT NULL,
	want_amount REAL NOT NULL,
	want_currency TEXT NOT NULL,
	message_id INTEGER NOT NULL,
	posted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (userid) REFERENCES exchangers(userid)
	);`, `
	CREATE TABLE IF NOT EXISTS reviews (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	offer_id INTEGER NOT NULL,
	reviewer_id INTEGER NOT NULL,
	reviewer_name TEXT NOT NULL,
	reviewee_id INTEGER NOT NULL,
	reviewee_name TEXT NOT NULL,
	comment TEXT,
	rating INTEGER NOT NULL CHECK (rating >= -1 AND rating <= -1),
	posted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (offer_id) REFERENCES offers(id),
	FOREIGN KEY (reviewer_id) REFERENCES exchangers(userid),
	FOREIGN KEY (reviewee_id) REFERENCES exchangers(userid)
	);`}

	for _, query := range queries {
		log.Printf("Executing query: %s", query)
		_, err = db.Exec(query)
		if err != nil {
			db.Close()
			// Rename the database file to avoid confusion
			panic(fmt.Errorf("Error creating tables: %v", err))
		}
	}

	// Set foreign key constraints
	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		db.Close()
		panic(fmt.Errorf("Error enabling foreign keys: %w", err))
	}

	fmt.Println("Database initialized successfully")

	return db
}

// Run executes the service
func Run() {
	secrets, settings := Init()
	db := initDB(getDBPath())
	defer db.Close()

	if err := sendToTelegramChannel(secrets.TelegramBotToken, settings.TelegramChannelID, "test"); err != nil {
		log.Fatalf("Error sending message to Telegram channel: %v", err)
	}

	fmt.Println("Service is running...")
}

func main() {
	Run()
}
