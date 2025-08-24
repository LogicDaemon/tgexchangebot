package main

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

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

type ParsedOffer struct {
	Type     OfferType
	Amount   float64
	Currency string
}

type MessageIndex struct {
	ChannelID int64
	MessageID int
}

// parseOfferCommand parses /buy or /sell commands with amount and currency
func parseOfferCommand(message *tgbotapi.Message) (ParsedOffer, error) {
	parts := strings.Fields(message.CommandArguments())
	if len(parts) < 2 {
		return ParsedOffer{}, fmt.Errorf("insufficient parameters")
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
		return ParsedOffer{}, fmt.Errorf("unknown offer type: %s", command)
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
			currency = part
		}
	}

	if amount <= 0 {
		return ParsedOffer{}, fmt.Errorf("invalid amount")
	}
	if currency == "" {
		return ParsedOffer{}, fmt.Errorf("missing currency")
	}

	// Normalize currency (aliases and regex)
	normCurr, ok := normalizeCurrency(currency)
	if !ok {
		return ParsedOffer{}, fmt.Errorf("unknown currency: %s", currency)
	}

	return ParsedOffer{
		Type:     offerType,
		Amount:   amount,
		Currency: normCurr,
	}, nil
}

type BotContext struct {
	bot      *tgbotapi.BotAPI
	db       *sql.DB
	settings *Settings
	commands map[string]func(*tgbotapi.Message, MessageIndex) error
	rates    *tbcRateCache
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

// sendReply sends a reply message to the original message
func (ctx *BotContext) sendReply(original *tgbotapi.Message, replyText string) (int64, error) {
	msg := tgbotapi.NewMessage(original.Chat.ID, replyText)
	msg.ReplyToMessageID = original.MessageID
	// Some channel posts may have nil From; only attach keyboard when we have a user ID
	if original.From != nil {
		msg.ReplyMarkup = createOfferKeyboard(original.From.ID)
	}
	// Avoid Markdown/HTML parse errors by sending plain text

	sent, err := ctx.bot.Send(msg)
	if err != nil {
		return 0, err
	}

	// Save the reply message ID
	replyID, err := saveReplyMessageID(ctx.db, MessageIndex{ChannelID: original.Chat.ID,
		MessageID: original.MessageID}, sent.MessageID)
	if err != nil {
		ctx.logToTelegramAndConsole(fmt.Errorf("Error saving reply_message_id: %w", err).Error())
	}
	return replyID, nil
}

// handleBuySellCommand handles /buy command
func (ctx *BotContext) handleBuySellCommand(message *tgbotapi.Message, update MessageIndex) error {
	offer, err := parseOfferCommand(message)
	if err != nil {
		cmd := message.Command() // "buy" or "sell"
		help := fmt.Sprintf(
			"Error parsing command: %s\n\nUsage:\n/%s <amount> <CURRENCY>\n/%s <CURRENCY> <amount>\n\nExamples:\n- /%s $ 100\n- /%s 20 лар\n\n%s\n",
			err.Error(), cmd, cmd, cmd, cmd, optionsForError(),
		)
		reply := tgbotapi.NewMessage(message.Chat.ID, help)
		reply.ReplyToMessageID = message.MessageID
		_, sendErr := ctx.bot.Send(reply)
		return sendErr
	}

	// Get user reputation
	reputation, err := getUserReputation(ctx.db, message.From.ID)
	if err != nil {
		log.Printf("Error getting user reputation: %v", err)
		reputation = 0
	}

	channelID := message.Chat.ID

	// If only one side provided, compute the other using TBC conversions
	// For simplicity, interpret:
	// - /sell <amount> <CUR>: user has <amount> <CUR>; compute want in default counter currency
	// - /buy <amount> <CUR>: user wants <amount> <CUR>; compute have in default counter currency

	storedOffer := StoredOffer{
		UserID:     message.From.ID,
		Username:   message.From.UserName,
		Reputation: reputation,
	}
	if offer.Type == OfferTypeSell {
		storedOffer.HaveAmount = offer.Amount
		storedOffer.HaveCurrency = offer.Currency
		// compute Want*
		if storedOffer.WantAmount == 0 || storedOffer.WantCurrency == "" {
			otherCur, otherAmt, convErr := ctx.rates.computeCounterAmount(storedOffer.HaveCurrency, storedOffer.HaveAmount, offer.Type)
			if convErr == nil {
				storedOffer.WantCurrency = otherCur
				storedOffer.WantAmount = otherAmt
			}
		}
	} else {
		storedOffer.WantAmount = offer.Amount
		storedOffer.WantCurrency = offer.Currency
		// compute Have*
		if storedOffer.HaveAmount == 0 || storedOffer.HaveCurrency == "" {
			otherCur, otherAmt, convErr := ctx.rates.computeCounterAmount(storedOffer.WantCurrency, storedOffer.WantAmount, offer.Type)
			if convErr == nil {
				storedOffer.HaveCurrency = otherCur
				storedOffer.HaveAmount = otherAmt
			}
		}
	}
	offerText := storedOfferToStringBuilder(nil, storedOffer)

	replyID, err := ctx.sendReply(message, offerText.String())
	if err != nil {
		return err
	}

	// Save offer to database
	_, err = saveOffer(ctx.db, NewOffer{
		UserID:       message.From.ID,
		Username:     message.From.UserName,
		HaveAmount:   storedOffer.HaveAmount,
		HaveCurrency: storedOffer.HaveCurrency,
		WantAmount:   storedOffer.WantAmount,
		WantCurrency: storedOffer.WantCurrency,
		ChannelID:    channelID,
		MessageID:    message.MessageID,
		ReplyID:      replyID,
	})
	if err != nil {
		ctx.logToTelegramAndConsole(fmt.Sprintf("Error saving offer: %v", err))
		return err
	}

	// Find and post matching offers
	matches, err := findMatchingOffers(ctx.db, offer)
	if err != nil {
		log.Printf("Error finding matches: %v", err)
		return nil
	}

	for _, match := range matches {
		matchesText := storedOfferToStringBuilder(nil, match)

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
func (ctx *BotContext) handleStatsCommand(message *tgbotapi.Message, update MessageIndex) error {
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

	_, err = ctx.sendReply(message, statsText)
	return err
}

// formatStoredOffers formats a list of StoredOffer for display
func (ctx *BotContext) formatStoredOffers(offers []StoredOffer, listText *strings.Builder) {
	listText.WriteString("Recent offers:\n\n")

	for _, offer := range offers {
		listText = storedOfferToStringBuilder(listText, offer)
		listText.WriteString("\n")
	}
}

// handleListCommand handles /list command
func (ctx *BotContext) handleListCommand(message *tgbotapi.Message, update MessageIndex) error {
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
	// reply := tgbotapi.NewMessage(message.Chat.ID, listText.String())
	_, err = ctx.sendReply(message, listText.String())
	return err
}

// handleRatesCommand handles /rates command to dump current rates and their age
func (ctx *BotContext) handleRatesCommand(message *tgbotapi.Message, update MessageIndex) error {
	if ctx.rates == nil {
		reply := tgbotapi.NewMessage(message.Chat.ID, "Rates cache is not initialized")
		_, err := ctx.bot.Send(reply)
		return err
	}
	// If user asked to refresh, perform a blocking full refresh (no timeout) then dump
	args := strings.TrimSpace(message.CommandArguments())
	if args == "refresh" {
		// Synchronously refresh with no timeout
		done := make(chan error, 1)
		ctx.rates.reqCh <- refreshReq{timeout: 0, respCh: done}
		if err := <-done; err != nil {
			_, _ = ctx.sendReply(message, "Refresh failed: "+err.Error())
			return nil
		}
	}
	base, cacheTS, rates := ctx.rates.snapshot()
	var sb strings.Builder
	sb.WriteString("Rates (base=" + base + ")\n")
	sb.WriteString("Cache: ")
	if cacheTS.IsZero() {
		sb.WriteString("never\n")
	} else {
		sb.WriteString(cacheTS.Format(time.RFC3339))
		sb.WriteString("\n")
	}
	// stable order by code
	codes := make([]string, 0, len(rates))
	for code := range rates {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	now := time.Now()
	for _, code := range codes {
		r := rates[code]
		age := "never"
		if !r.LastUpdated.IsZero() {
			d := now.Sub(r.LastUpdated).Round(time.Minute)
			// format hh:mm
			h := int(d.Hours())
			m := int(d.Minutes()) % 60
			age = fmt.Sprintf("%02d:%02d", h, m)
		}
		// USD: buy/sell and age
		sb.WriteString(fmt.Sprintf("%s: buy=%.4f sell=%.4f age=%s\n", code, r.Buy, r.Sell, age))
	}
	_, err := ctx.sendReply(message, sb.String())
	return err
}

// storedOfferToStringBuilder formats a StoredOffer for display
func storedOfferToStringBuilder(sb *strings.Builder, offer StoredOffer) *strings.Builder {
	if sb == nil {
		sb = &strings.Builder{}
	}
	sb.WriteString(fmt.Sprintf(
		"@%s [%d] ",
		offer.Username,
		offer.Reputation,
	))

	if offer.HaveAmount > 0 {
		sb.WriteString(fmt.Sprintf("has %.2f %s ", offer.HaveAmount, formatCodeWithRep(offer.HaveCurrency)))
	}
	if offer.HaveAmount > 0 && offer.WantAmount > 0 {
		sb.WriteString("and ")
	}
	if offer.WantAmount > 0 {
		sb.WriteString(fmt.Sprintf("wants %.2f %s ", offer.WantAmount, formatCodeWithRep(offer.WantCurrency)))
	}
	return sb
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
	// if update.Message == nil {
	// 	return nil
	// }

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

	var message *tgbotapi.Message
	if update.Message != nil {
		message = update.Message
	} else if update.ChannelPost != nil {
		message = update.ChannelPost
	} else if update.EditedMessage != nil {
		message = update.EditedMessage
	} else if update.EditedChannelPost != nil {
		message = update.EditedChannelPost
	}
	prevReply, err := findReplyMessageID(ctx.db, MessageIndex{message.Chat.ID, message.MessageID})
	if err != nil {
		ctx.logToTelegramAndConsole(fmt.Sprintf("Error finding reply message ID: %v", err))
		return err
	}

	// Handle commands
	command := message.Command()
	if command == "" {
		if prevReply.MessageID != 0 {
			ctx.logToTelegramAndConsole(fmt.Sprintf("Received message without command, deleting message with ID %d", prevReply))
			delRequest := tgbotapi.NewDeleteMessage(prevReply.ChannelID, prevReply.MessageID)
			if _, err := ctx.bot.Send(delRequest); err != nil {
				log.Printf("Error deleting message with ID %d: %v", prevReply, err)
			}
			deleteOfferByMessage(ctx.db, prevReply)
		}
		return nil
	}
	// message.From can be nil for channel posts; avoid nil dereference in logs
	fromUser := ""
	if message.From != nil {
		fromUser = message.From.UserName
	}
	log.Printf(`Received command "%s" from user "%s"`, command, fromUser)

	if handler, exists := ctx.commands[command]; exists {
		if err := handler(message, prevReply); err != nil {
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

	return nil
}

// handleCallbackQuery processes callback queries from inline buttons
func (ctx *BotContext) handleCallbackQuery(callback *tgbotapi.CallbackQuery) error {
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

	ctx.commands = map[string]func(*tgbotapi.Message, MessageIndex) error{
		OfferTypeBuyName:  ctx.handleBuySellCommand,
		OfferTypeSellName: ctx.handleBuySellCommand,
		"stats":           ctx.handleStatsCommand,
		"list":            ctx.handleListCommand,
		"rates":           ctx.handleRatesCommand,
	}

	for update := range updates {
		if update.Message != nil || update.ChannelPost != nil {
			if err := ctx.handleUpdate(update); err != nil {
				log.Printf("Error %v handling update %v", err, update)
			}
		}
		if update.CallbackQuery != nil {
			if err := ctx.handleCallbackQuery(update.CallbackQuery); err != nil {
				log.Printf("Error %v handling callback query %v", err, update)
			}
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

	rates := initCurrencyRates(secrets.TBCApiKey)

	// Send test message to verify channel connection
	if err := sendToTelegram(bot, settings.TelegramServiceChannelID, "ExchangeBot started"); err != nil {
		log.Fatalf("Error sending message to Telegram channel: %v", err)
	}

	ctx := BotContext{
		bot:      bot,
		db:       db,
		settings: settings,
		rates:    rates,
	}

	// Start message handler
	ctx.handleUpdates()
}
