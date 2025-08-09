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
	OfferTypeBuy      OfferType = -1
	OfferTypeSell     OfferType = 1
	OfferTypeBuyName            = "buy"
	OfferTypeSellName           = "sell"
)

// ParsedOffer represents a parsed buy/sell command
type ParsedOffer struct {
	Type     OfferType
	Amount   float64
	Currency string
}

// parseOfferCommand parses /buy or /sell commands with amount and currency
func parseOfferCommand(message *tgbotapi.Message) (*ParsedOffer, error) {
	parts := strings.Fields(message.CommandArguments())
	if len(parts) < 2 {
		return nil, fmt.Errorf("insufficient parameters")
	}

	// Command() already returns without the leading '/'
	command := message.Command()
	var offerType OfferType
	switch command {
	case OfferTypeSellName:
		offerType = OfferTypeSell
	case OfferTypeBuyName:
		offerType = OfferTypeBuy
	default:
		return nil, fmt.Errorf("unknown offer type: %s", command)
	}

	// Parse parts for amount and currency (order-independent, first numeric & first non-numeric)
	var amount float64
	var currency string
	for _, part := range parts { // don't skip the first arg
		if amount == 0 { // only attempt parse if we didn't already capture a number
			if num, parseErr := strconv.ParseFloat(part, 64); parseErr == nil {
				amount = num
				continue
			}
		}
		if currency == "" { // capture first non-numeric token as currency
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
	commands map[string]func(message *tgbotapi.Message) error
}

// createOfferKeyboard creates inline keyboard for offer interactions
func createOfferKeyboard(userID int) tgbotapi.InlineKeyboardMarkup {
	contactButton := tgbotapi.NewInlineKeyboardButtonURL("contact", fmt.Sprintf("tg://user?id=%d", userID))
	feedbackButton := tgbotapi.NewInlineKeyboardButtonData("feedback", fmt.Sprintf("feedback_%d", userID))

	keyboard := tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
			{contactButton, feedbackButton},
		},
	}
	return keyboard
}

// handleBuySellCommand handles /buy command
func (ctx *BotContext) handleBuySellCommand(message *tgbotapi.Message) error {
	offer, err := parseOfferCommand(message)
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

	// Save offer to database (store user's command message ID); we'll update reply_message_id after posting
	offerID, err := saveOffer(ctx.db, message.From.ID, message.From.UserName, offer, channelID, message.MessageID)
	if err != nil {
		log.Printf("Error saving offer: %v", err)
	}

	// Format and send the offer message
	var offerText strings.Builder
	storedOffer := StoredOffer{
		UserID:     message.From.ID,
		Username:   message.From.UserName,
		Reputation: reputation,
	}
	if offer.Type == OfferTypeSell {
		storedOffer.HaveAmount = offer.Amount
		storedOffer.HaveCurrency = offer.Currency
	} else {
		storedOffer.WantAmount = offer.Amount
		storedOffer.WantCurrency = offer.Currency
	}
	writeStoredOffer(storedOffer, &offerText)

	msg := tgbotapi.NewMessage(channelID, offerText.String())
	sent, err := ctx.bot.Send(msg)
	if err != nil {
		return err
	}

	// Update the offer with the bot's reply message ID
	if offerID != 0 {
		if err := updateOfferReplyMessageID(ctx.db, offerID, sent.MessageID); err != nil {
			ctx.logToTelegramAndConsole(fmt.Errorf("Error updating reply_message_id for offer %d: %w", offerID, err).Error())
		}
	}

	// Find and post matching offers
	matches, err := findMatchingOffers(ctx.db, offer)
	if err != nil {
		log.Printf("Error finding matches: %v", err)
		return nil
	}

	for _, match := range matches {
		var matchesText strings.Builder
		writeStoredOffer(match, &matchesText)

		keyboard := createOfferKeyboard(match.UserID)
		matchMsg := tgbotapi.NewMessage(channelID, matchesText.String())
		matchMsg.ReplyMarkup = keyboard
		if _, err = ctx.bot.Send(matchMsg); err != nil {
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
	offers, err := getFilteredOffers(ctx.db, 10, "")
	if err != nil {
		ctx.logToTelegramAndConsole(fmt.Sprintf("Error getting recent offers: %v", err))
		return err
	}
	if len(offers) == 0 {
		reply := tgbotapi.NewMessage(message.Chat.ID, "No offers found.")
		_, err = ctx.bot.Send(reply)
		return err
	}

	var listText strings.Builder
	ctx.formatStoredOffers(offers, &listText)
	reply := tgbotapi.NewMessage(message.Chat.ID, listText.String())
	_, err = ctx.bot.Send(reply)
	return err
}

// formatStoredOffers formats a list of StoredOffer for display
func (ctx *BotContext) formatStoredOffers(offers []StoredOffer, listText *strings.Builder) {
	listText.WriteString("Recent offers:\n\n")

	for _, offer := range offers {
		writeStoredOffer(offer, listText)
		listText.WriteString("\n")
	}
}

// writeStoredOffer formats a StoredOffer for display
func writeStoredOffer(offer StoredOffer, listText *strings.Builder) {
	listText.WriteString(fmt.Sprintf(
		"@%s [%d] ",
		offer.Username,
		offer.Reputation,
	))

	if offer.HaveAmount > 0 {
		listText.WriteString(fmt.Sprintf("has %.2f %s ", offer.HaveAmount, offer.HaveCurrency))
	}
	if offer.HaveAmount > 0 && offer.WantAmount > 0 {
		listText.WriteString("and ")
	}
	if offer.WantAmount > 0 {
		listText.WriteString(fmt.Sprintf("wants %.2f %s ", offer.WantAmount, offer.WantCurrency))
	}
}

// logToTelegramAndConsole logs messages to both console and Telegram channel
func (ctx BotContext) logToTelegramAndConsole(message string) {
	// Log to console
	log.Println(message)

	// Send to Telegram channel
	if err := sendToTelegram(ctx.bot, ctx.settings.TelegramServiceChannelID, message); err != nil {
		log.Printf("Error sending log message to Telegram: %v", err)
	}
}

// handleUpdate processes incoming updates from Telegram
func (ctx *BotContext) handleUpdate(update tgbotapi.Update) (err error) {
	if update.Message == nil {
		return nil
	}

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

	message := update.Message

	// Handle commands
	if command := message.Command(); command != "" {
		ctx.logToTelegramAndConsole(fmt.Sprintf(`Received command "%s" from user "%s"`, command, message.From.UserName))

		if handler, exists := ctx.commands[command]; exists {
			if err := handler(message); err != nil {
				log.Printf("Error handling %s command: %v", command, err)
			}
		} else {
			commandNames := make([]string, 0, len(ctx.commands))
			for name := range ctx.commands {
				commandNames = append(commandNames, "/"+name)
			}
			reply := tgbotapi.NewMessage(message.Chat.ID, "Unknown command. Available commands: "+strings.Join(commandNames, ", "))
			if _, err := ctx.bot.Send(reply); err != nil {
				log.Printf("Error sending reply: %v", err)
			}
		}
	}

	return nil
}

// handleCallbackQuery processes callback queries from inline buttons
func (ctx *BotContext) handleCallbackQuery(update tgbotapi.Update) error {
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
	return nil
}

// handleUpdates sets up the message handling loop
func (ctx *BotContext) handleUpdates() {
	u := tgbotapi.NewUpdate(getNextUpdateId(ctx.db))
	u.Timeout = 60

	updates, err := ctx.bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatalf("Error getting updates channel: %v", err)
	}

	ctx.commands = map[string]func(message *tgbotapi.Message) error{
		OfferTypeBuyName:  ctx.handleBuySellCommand,
		OfferTypeSellName: ctx.handleBuySellCommand,
		"stats":           ctx.handleStatsCommand,
		"list":            ctx.handleListCommand,
	}

	for update := range updates {
		var err error
		if update.Message != nil {
			err = ctx.handleUpdate(update)
		}
		if update.CallbackQuery != nil {
			err = ctx.handleCallbackQuery(update)
		}
		if err != nil {
			log.Printf("Error %v handling update %v", err, update)
		}
		if err := saveLastUpdateID(ctx.db, update.UpdateID); err != nil {
			log.Printf("Error saving last update ID: %v", err)
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
	if err := sendToTelegram(bot, settings.TelegramServiceChannelID, "ExchangeBot started"); err != nil {
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
