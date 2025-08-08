package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

// Secrets holds only the authentication secrets for the bot
type Secrets struct {
	TelegramBotToken string `json:"telegram_bot_token"`
}

// Settings holds the configuration settings for the bot
type Settings struct {
	TelegramServiceChannelID int64 `json:"telegram_service_channel_id"`
}

const (
	dbFileName = "exchangers.sqlite3"
	botName    = "ExchangeBot"
)

func loadFile(filePath string, displayType string) []byte {
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Panicf(`%s file not found at "%s"`, displayType, filePath)
	}

	rawdata, err := os.ReadFile(filePath)
	if err != nil {
		log.Panicf(`error %v reading %s file "%s"`, err, displayType, filePath)
	}

	return rawdata
}

func getLocalAppDataDir() string {
	// Default paths based on OS
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			log.Panicf("LOCALAPPDATA environment variable is not set")
		}
		return localAppData
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Panicf("Error getting home directory: %v", err)
		}
		return filepath.Join(homeDir, ".local")
	}
}

func getDefaultSecretsPath() string {
	var secretDataDir string

	// Check environment variable
	if envPath := os.Getenv("SECRETS_PATH"); envPath != "" {
		return envPath
	}

	// Check SecretDataDir environment variable
	if dir := os.Getenv("SecretDataDir"); dir != "" {
		secretDataDir = dir
	} else {
		secretDataDir = filepath.Join(getLocalAppDataDir(), "_sec")
	}

	return filepath.Join(secretDataDir, botName+".json")
}

func getSettingsPath() string {
	var dataDir string

	// Default paths based on OS
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		dataDir = filepath.Join(localAppData, botName)
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Error getting home directory: %v", err)
		}
		dataDir = filepath.Join(homeDir, ".local", botName)
	}

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("Error creating data directory: %v", err)
	}

	return filepath.Join(dataDir, "settings.json")
}

func loadSecrets() *Secrets {
	var secrets Secrets

	if err := json.Unmarshal(loadFile(getDefaultSecretsPath(), "secrets"), &secrets); err != nil {
		panic(fmt.Errorf("error parsing secrets file: %v", err))
	}

	if secrets.TelegramBotToken == "" {
		panic(fmt.Errorf("missing required secrets"))
	}

	return &secrets
}

func loadSettings() *Settings {
	var settings Settings

	if err := json.Unmarshal(loadFile(getSettingsPath(), "settings"), &settings); err != nil {
		panic(fmt.Errorf("error parsing settings file: %v", err))
	}

	if settings.TelegramServiceChannelID == 0 {
		panic(fmt.Errorf("missing required settings"))
	}

	return &settings
}

func getDBPath() string {
	dataDir := filepath.Join(getLocalAppDataDir(), botName)

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("Error creating data directory: %v", err)
	}

	return filepath.Join(dataDir, dbFileName)
}

func sendToTelegramChannel(bot *tgbotapi.BotAPI, channelID int64, message string) error {
	// Use plain text mode
	msg := tgbotapi.NewMessage(channelID, message)

	_, err := bot.Send(msg)
	if err != nil {
		return fmt.Errorf("error sending message: %v", err)
	}

	return nil
}

func printInstructions() {
	fmt.Println("Missing required configuration.")
	fmt.Println("\nPlease create the following configuration files:")

	// Secrets file
	fmt.Println("\n1. Secrets file (for the bot token):")
	fmt.Printf("   Path: %s\n", getDefaultSecretsPath())
	fmt.Println("   Format:")
	fmt.Println(`   {
     "telegram_bot_token": "YOUR_TELEGRAM_BOT_TOKEN"
     }`)
	fmt.Println("   To obtain, create a Telegram bot by talking to @BotFather and get the token")

	// Settings file
	fmt.Println("\n2. Settings file (for the channel ID):")
	fmt.Printf("   Path: %s\n", getSettingsPath())
	fmt.Println("   Format:")
	fmt.Println(`   {
     "telegram_channel_id": YOUR_CHANNEL_ID_NUMBER
   }`)
	fmt.Println("   To get it, add your bot to the target channel as an administrator,")
	fmt.Println("   and forward a message from the channel to @userinfobot.")
	fmt.Println("   Use the 'Id' number from the 'Forwarded from chat' value (including the negative sign)")
}

func getConfig() (*Secrets, *Settings) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Initialization failed: %v", r)
			printInstructions()
			os.Exit(1)
		}
	}()
	secrets := loadSecrets()
	settings := loadSettings()

	return secrets, settings
}
