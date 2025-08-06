package main

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// OfferType represents the type of offer
type OfferType int

const (
	OfferTypeSell OfferType = iota
	OfferTypeBuy
)

// ParsedOffer represents a parsed buy/sell command
type ParsedOffer struct {
	Type     OfferType
	Amount   float64
	Currency string
}

// parseOfferCommand parses /buy or /sell commands with amount and currency
func parseOfferCommand(text string) (*ParsedOffer, error) {
	parts := strings.Fields(text)
	if len(parts) < 3 {
		return nil, fmt.Errorf("insufficient parameters")
	}

	command := strings.ToLower(parts[0])
	var offerType OfferType

	switch command {
	case "/sell":
		offerType = OfferTypeSell
	case "/buy":
		offerType = OfferTypeBuy
	default:
		return nil, fmt.Errorf("unknown command")
	}

	// Parse remaining parts for amount and currency
	var amount float64
	var currency string

	for _, part := range parts[1:] {
		// Try to parse as number
		if num, parseErr := strconv.ParseFloat(part, 64); parseErr == nil {
			amount = num
		} else {
			// Assume it's currency
			currency = strings.ToUpper(part)
		}
	}

	if amount <= 0 {
		return nil, fmt.Errorf("invalid amount")
	}
	if currency == "" {
		return nil, fmt.Errorf("missing currency")
	}

	return &ParsedOffer{
		Type:     offerType,
		Amount:   amount,
		Currency: currency,
	}, nil
}

type BotContext struct {
	bot      *tgbotapi.BotAPI
	db       *sql.DB
	settings *Settings
}

// formatOfferMessage formats the offer message for posting
func formatOfferMessage(username string, reputation int, amount float64, currency string, offerType OfferType) string {
	action := "has"
	if offerType == OfferTypeBuy {
		action = "needs"
	}

	return fmt.Sprintf("@%s [%d] %s %.2f %s [delete]", username, reputation, action, amount, currency)
}

// createOfferKeyboard creates inline keyboard for offer interactions
func createOfferKeyboard(userID int, offerID int) tgbotapi.InlineKeyboardMarkup {
	contactButton := tgbotapi.NewInlineKeyboardButtonURL("contact", fmt.Sprintf("tg://user?id=%d", userID))
	feedbackButton := tgbotapi.NewInlineKeyboardButtonData("feedback", fmt.Sprintf("feedback_%d", offerID))

	keyboard := tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
			{contactButton, feedbackButton},
		},
	}
	return keyboard
}

// handleBuyCommand handles /buy command
func (ctx BotContext) handleBuyCommand(message *tgbotapi.Message) error {
	offer, err := parseOfferCommand(message.Text)
	if err != nil {
		reply := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Error parsing command: %s", err.Error()))
		_, err = ctx.bot.Send(reply)
		return err
	}

	// Get user reputation
	reputation, err := getUserReputation(ctx.db, message.From.ID)
	if err != nil {
		log.Printf("Error getting user reputation: %v", err)
		reputation = 0
	}

	channelID := message.Chat.ID

	// Save offer to database
	err = saveOffer(ctx.db, message.From.ID, message.From.UserName, offer, channelID, message.MessageID)
	if err != nil {
		log.Printf("Error saving offer: %v", err)
	}

	// Format and send the offer message
	offerText := formatOfferMessage(message.From.UserName, reputation, offer.Amount, offer.Currency, offer.Type)

	msg := tgbotapi.NewMessage(channelID, offerText)
	sentMsg, err := ctx.bot.Send(msg)
	if err != nil {
		return err
	}

	// Find and post matching offers
	matches, err := findMatchingOffers(ctx.db, offer)
	if err != nil {
		log.Printf("Error finding matches: %v", err)
		return nil
	}

	for _, match := range matches {
		matchText := formatOfferMessage(
			match["username"].(string),
			match["reputation"].(int),
			match["amount"].(float64),
			match["currency"].(string),
			OfferTypeSell, // Opposite of buy
		)

		keyboard := createOfferKeyboard(match["userid"].(int), sentMsg.MessageID)
		matchMsg := tgbotapi.NewMessageToChannel(fmt.Sprintf("%d", channelID), matchText)
		matchMsg.ReplyMarkup = keyboard
		_, err = ctx.bot.Send(matchMsg)
		if err != nil {
			log.Printf("Error sending match: %v", err)
		}
	}

	return nil
}

// handleSellCommand handles /sell command
func (ctx *BotContext) handleSellCommand(message *tgbotapi.Message) error {
	offer, err := parseOfferCommand(message.Text)
	if err != nil {
		reply := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Error parsing command: %s", err.Error()))
		_, err = ctx.bot.Send(reply)
		return err
	}

	// Get user reputation
	reputation, err := getUserReputation(ctx.db, message.From.ID)
	if err != nil {
		log.Printf("Error getting user reputation: %v", err)
		reputation = 0
	}

	channelID := message.Chat.ID

	// Save offer to database
	err = saveOffer(ctx.db, message.From.ID, message.From.UserName, offer, channelID, message.MessageID)
	if err != nil {
		log.Printf("Error saving offer: %v", err)
	}

	// Format and send the offer message
	offerText := formatOfferMessage(message.From.UserName, reputation, offer.Amount, offer.Currency, offer.Type)

	msg := tgbotapi.NewMessageToChannel(fmt.Sprintf("%d", channelID), offerText)
	sentMsg, err := ctx.bot.Send(msg)
	if err != nil {
		return err
	}

	// Find and post matching offers
	matches, err := findMatchingOffers(ctx.db, offer)
	if err != nil {
		log.Printf("Error finding matches: %v", err)
		return nil
	}

	for _, match := range matches {
		matchText := formatOfferMessage(
			match["username"].(string),
			match["reputation"].(int),
			match["amount"].(float64),
			match["currency"].(string),
			OfferTypeBuy, // Opposite of sell
		)

		keyboard := createOfferKeyboard(match["userid"].(int), sentMsg.MessageID)
		matchMsg := tgbotapi.NewMessageToChannel(fmt.Sprintf("%d", channelID), matchText)
		matchMsg.ReplyMarkup = keyboard
		_, err = ctx.bot.Send(matchMsg)
		if err != nil {
			log.Printf("Error sending match: %v", err)
		}
	}

	return nil
}

// handleStatsCommand handles /stats command
func (ctx *BotContext) handleStatsCommand(message *tgbotapi.Message) error {
	// Get user statistics
	reputation, err := getUserReputation(ctx.db, message.From.ID)
	if err != nil {
		log.Printf("Error getting user reputation: %v", err)
		reputation = 0
	}

	// Count user's offers
	var offerCount int
	err = ctx.db.QueryRow("SELECT COUNT(*) FROM offers WHERE userid = ?", message.From.ID).Scan(&offerCount)
	if err != nil {
		log.Printf("Error counting offers: %v", err)
		offerCount = 0
	}

	statsText := fmt.Sprintf("Your stats:\nReputation: %d\nTotal offers: %d", reputation, offerCount)

	reply := tgbotapi.NewMessage(message.Chat.ID, statsText)
	_, err = ctx.bot.Send(reply)
	return err
}

// handleListCommand handles /list command
func (ctx *BotContext) handleListCommand(message *tgbotapi.Message) error {
	// Get recent offers
	rows, err := ctx.db.Query(`
		SELECT o.username, o.have_amount, o.have_currency, o.want_amount, o.want_currency, e.reputation
		FROM offers o
		LEFT JOIN exchangers e ON o.userid = e.userid
		ORDER BY o.posted_at DESC
		LIMIT 10`)
	if err != nil {
		log.Printf("Error querying offers: %v", err)
		reply := tgbotapi.NewMessage(message.Chat.ID, "Error retrieving offers")
		_, err = ctx.bot.Send(reply)
		return err
	}
	defer rows.Close()

	var listText strings.Builder
	listText.WriteString("Recent offers:\n\n")

	for rows.Next() {
		var username string
		var haveAmount, wantAmount float64
		var haveCurrency, wantCurrency string
		var reputation sql.NullInt64

		err := rows.Scan(&username, &haveAmount, &haveCurrency, &wantAmount, &wantCurrency, &reputation)
		if err != nil {
			continue
		}

		rep := 0
		if reputation.Valid {
			rep = int(reputation.Int64)
		}

		if haveAmount > 0 {
			listText.WriteString(fmt.Sprintf("@%s [%d] has %.2f %s\n", username, rep, haveAmount, haveCurrency))
		}
		if wantAmount > 0 {
			listText.WriteString(fmt.Sprintf("@%s [%d] needs %.2f %s\n", username, rep, wantAmount, wantCurrency))
		}
	}

	if listText.Len() == len("Recent offers:\n\n") {
		listText.WriteString("No offers found.")
	}

	reply := tgbotapi.NewMessage(message.Chat.ID, listText.String())
	_, err = ctx.bot.Send(reply)
	return err
}

func (ctx BotContext) logToTelegramAndConsole(message string) {
	// Log to console
	log.Println(message)

	// Send to Telegram channel
	if err := sendToTelegramChannel(ctx.bot, ctx.settings.TelegramServiceChannelID, message); err != nil {
		log.Printf("Error sending log message to Telegram: %v", err)
	}
}

// handleUpdate processes incoming updates from Telegram
func (ctx *BotContext) handleUpdate(update tgbotapi.Update) (err error) {
	defer func() {
		if r := recover(); r != nil {
			panicMsg := fmt.Errorf("recovered from panic: %v", r)
			if err == nil {
				err = panicMsg
			} else {
				err = fmt.Errorf("%w; %v", err, panicMsg)
			}
		}
	}()
	if update.Message == nil {
		return nil
	}

	message := update.Message

	// Handle commands
	if command := message.Command(); command != "" {
		ctx.logToTelegramAndConsole(fmt.Sprintf(`Received command "%s" from user "%s"`, command, message.From.UserName))

		var err error
		switch command {
		case "buy":
			err = ctx.handleBuyCommand(message)
		case "sell":
			err = ctx.handleSellCommand(message)
		case "stats":
			err = ctx.handleStatsCommand(message)
		case "list":
			err = ctx.handleListCommand(message)
		default:
			reply := tgbotapi.NewMessage(message.Chat.ID, "Unknown command. Available commands: /buy, /sell, /stats, /list")
			_, err = ctx.bot.Send(reply)
			if err != nil {
				log.Printf("Error sending reply: %v", err)
			}
			return err
		}
		if err != nil {
			log.Printf("Error handling %s command: %v", command, err)
		}
	}

	// Handle callback queries (for feedback buttons)
	if update.CallbackQuery != nil {
		callback := update.CallbackQuery
		if strings.HasPrefix(callback.Data, "feedback_") {
			// Handle feedback button press
			response := tgbotapi.CallbackConfig{
				CallbackQueryID: callback.ID,
				Text:            "Feedback feature coming soon!",
			}
			_, err := ctx.bot.AnswerCallbackQuery(response)
			if err != nil {
				log.Printf("Error answering callback: %v", err)
			}
		}
	}
	return nil
}

// handleUpdates sets up the message handling loop
func (ctx *BotContext) handleUpdates() {
	u := tgbotapi.NewUpdate(getLastUpdateId(ctx.db))
	u.Timeout = 60

	updates, err := ctx.bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatalf("Error getting updates channel: %v", err)
	}

	for update := range updates {
		if err := ctx.handleUpdate(update); err != nil {
			ctx.logToTelegramAndConsole(fmt.Sprintf("Error handling update: %v", err))
		} else {
			saveLastUpdateID(ctx.db, update.UpdateID)
		}
	}
}

// Run executes the service
func main() {
	secrets, settings := getConfig()
	db := initDB(getDBPath())
	defer db.Close()

	// Initialize bot
	bot, err := tgbotapi.NewBotAPI(secrets.TelegramBotToken)
	if err != nil {
		log.Fatalf("Error initializing bot: %v", err)
	}

	bot.Debug = false
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Send test message to verify channel connection
	if err := sendToTelegramChannel(bot, settings.TelegramServiceChannelID, "ExchangeBot started"); err != nil {
		log.Fatalf("Error sending message to Telegram channel: %v", err)
	}

	ctx := BotContext{
		bot:      bot,
		db:       db,
		settings: settings,
	}

	// Start message handler
	ctx.handleUpdates()
}
