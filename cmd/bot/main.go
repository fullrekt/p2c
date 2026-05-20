package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"p2c-sniper/internal/config"
	"p2c-sniper/internal/db"
	"p2c-sniper/internal/sniper"
)

type sessionState int

const (
	stateIdle sessionState = iota
	stateWaitToken
	stateWaitMin
	stateWaitMax
)

const (
	btnSetToken   = "🔑 Установить токен"
	btnSetLimits  = "💸 Лимиты"
	btnStartSniper = "🚀 Запустить"
	btnStopSniper = "🛑 Остановить"
	btnClearToken = "🗑 Очистить токен"
	btnAccount    = "👤 Аккаунт"
	btnVolume     = "📊 Объем 24ч"
	btnHelp       = "📋 Меню"
)

type userSession struct {
	State sessionState
	Min   float64
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg := config.Load()
	dbPath := strings.TrimSpace(os.Getenv("DB_PATH"))
	if dbPath == "" {
		dbPath = "bot_users.db"
	}
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	bot, err := newTelegramBot(cfg.BotToken)
	if err != nil {
		log.Fatal(err)
	}
	notifier := sniper.TelegramNotifier{Bot: bot}

	sniperCfg := sniper.Config{
		ConcurrentRequests: cfg.ConcurrentRequests,
		RequestTimeoutSec:  cfg.RequestTimeoutSec,
		UserAgents:         cfg.UserAgents,
	}

	var (
		activeMu sync.RWMutex
		active   = map[int64]*sniper.Bot{}
		stateMu  sync.RWMutex
		states   = map[int64]*userSession{}
	)

	restoreRunners(database, active, &activeMu, sniperCfg, notifier)
	go dailyReportsLoop(ctx, database, bot, cfg.AdminIDs)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			log.Println("shutdown signal received, stopping snipers...")
			bot.StopReceivingUpdates()
			activeMu.Lock()
			for id, sb := range active {
				sb.Stop("")
				delete(active, id)
			}
			activeMu.Unlock()
			log.Println("shutdown complete")
			return
		case upd, ok := <-updates:
			if !ok {
				return
			}

			// Inline-button callback: "Я оплатил"
			if upd.CallbackQuery != nil {
				cb := upd.CallbackQuery
				bot.Request(tgbotapi.NewCallback(cb.ID, ""))
				if strings.HasPrefix(cb.Data, "complete:") {
					parts := strings.SplitN(strings.TrimPrefix(cb.Data, "complete:"), ":", 2)
					paymentID, err := strconv.ParseInt(parts[0], 10, 64)
					reqID := ""
					if len(parts) == 2 {
						reqID = parts[1]
					}
					if err == nil {
						user, ok, err := database.GetUser(cb.From.ID)
						if err == nil && ok && user.AccessToken != "" {
							go sendCompleteRequest(bot, cb.From.ID, user.AccessToken, paymentID, reqID)
						}
					}
				}
				continue
			}

			if upd.Message == nil || upd.Message.From == nil {
				continue
			}
		msg := upd.Message
		uid := msg.From.ID
		text := normalizeCommandInput(strings.TrimSpace(msg.Text))

		_ = database.AddUser(uid, msg.From.UserName)

		stateMu.Lock()
		sess := states[uid]
		if sess == nil {
			sess = &userSession{State: stateIdle}
			states[uid] = sess
		}
		state := sess.State
		stateMu.Unlock()

		switch {
		case text == "/start":
			send(bot, uid, "👋 Выбери действие кнопкой ниже")
		case text == "/settoken":
			updateState(&stateMu, states, uid, stateWaitToken)
			send(bot, uid, "Отправь access_token")
		case state == stateWaitToken:
			token := cleanToken(text)
			if token == "" {
				send(bot, uid, "Токен пустой. Отправь access_token повторно")
				continue
			}
			accID, info, err := getFirstActiveAccount(token)
			if err != nil {
				send(bot, uid, "Токен невалидный или нет активных реквизитов: "+err.Error()+"\nОтправь access_token повторно")
				continue
			}
			stateMu.Lock()
			delete(states, uid)
			stateMu.Unlock()
			if err := database.UpdateToken(uid, token, accID, ""); err != nil {
				send(bot, uid, "Ошибка БД: "+err.Error())
				continue
			}
			send(bot, uid, "✅ Успешно: "+info)
		case text == "/setlimits":
			updateState(&stateMu, states, uid, stateWaitMin)
			send(bot, uid, "Введите минимальную сумму")
		case state == stateWaitMin:
			min, err := strconv.ParseFloat(text, 64)
			if err != nil || min <= 0 {
				send(bot, uid, "Нужно число > 0")
				continue
			}
			stateMu.Lock()
			sess.Min = min
			sess.State = stateWaitMax
			stateMu.Unlock()
			send(bot, uid, "Введите максимальную сумму")
		case state == stateWaitMax:
			max, err := strconv.ParseFloat(text, 64)
			if err != nil {
				send(bot, uid, "Нужно число")
				continue
			}
			stateMu.Lock()
			min := sess.Min
			delete(states, uid)
			stateMu.Unlock()
			if max <= min {
				send(bot, uid, "Максимум должен быть больше минимума")
				continue
			}
			if err := database.UpdateLimits(uid, min, max); err != nil {
				send(bot, uid, "Ошибка БД: "+err.Error())
				continue
			}
			activeMu.RLock()
			if sb, ok := active[uid]; ok {
				sb.SetLimits(min, max)
			}
			activeMu.RUnlock()
			send(bot, uid, fmt.Sprintf("✅ Лимиты обновлены: %.2f - %.2f", min, max))
		case text == "/startsniper":
			activeMu.RLock()
			_, already := active[uid]
			activeMu.RUnlock()
			if already {
				send(bot, uid, "Уже запущен")
				continue
			}
			user, ok, err := database.GetUser(uid)
			if err != nil || !ok || user.AccessToken == "" {
				send(bot, uid, "Сначала настрой токен: /settoken")
				continue
			}
			sb := sniper.New(user, sniperCfg, database, notifier)
			activeMu.Lock()
			active[uid] = sb
			activeMu.Unlock()
			go func(id int64, b *sniper.Bot) {
				b.Start()
				activeMu.Lock()
				delete(active, id)
				activeMu.Unlock()
			}(uid, sb)
			send(bot, uid, "🚀 Снайпер запущен")
		case text == "/cleartoken":
			activeMu.Lock()
			sb, running := active[uid]
			if running {
				delete(active, uid)
			}
			activeMu.Unlock()
			if running {
				sb.Stop("")
			}
			if err := database.ClearAccessToken(uid); err != nil {
				send(bot, uid, "Ошибка БД: "+err.Error())
				continue
			}
			stateMu.Lock()
			delete(states, uid)
			stateMu.Unlock()
			send(bot, uid, "🗑 Токен и реквизиты удалены из бота, снайпер снят с автозапуска. Чтобы снова работать — /settoken")
		case text == "/stopsniper":
			activeMu.Lock()
			sb, ok := active[uid]
			if ok {
				delete(active, uid)
			}
			activeMu.Unlock()
			if !ok {
				if err := database.SetRunningStatus(uid, false); err != nil {
					send(bot, uid, "Ошибка БД: "+err.Error())
					continue
				}
				send(bot, uid, "Снайпер не в памяти; флаг автозапуска сброшен. Если заявки всё ещё ловятся — /cleartoken")
				continue
			}
			sb.Stop("Остановлен пользователем")
			send(bot, uid, "🛑 Остановлен")
		case text == "/account":
			user, ok, err := database.GetUser(uid)
			if err != nil || !ok {
				send(bot, uid, "Нет данных")
				continue
			}
			activeMu.RLock()
			_, run := active[uid]
			activeMu.RUnlock()
			status := "OFF"
			if run {
				status = "ON"
			}
			send(bot, uid, fmt.Sprintf("Status: %s\nMin: %.2f\nMax: %.2f", status, user.MinAmount, user.MaxAmount))
		case text == "/volume":
			v, err := database.GetDailyVolume(uid)
			if err != nil {
				send(bot, uid, "Ошибка: "+err.Error())
				continue
			}
			send(bot, uid, fmt.Sprintf("Объем за 24ч: %.2f RUB", v))
		}
		}
	}
}

func newTelegramBot(token string) (*tgbotapi.BotAPI, error) {
	client := &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 20 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	var lastErr error
	for i := 1; i <= 8; i++ {
		bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, client)
		if err == nil {
			if i > 1 {
				log.Printf("telegram api connected after retry=%d", i-1)
			}
			return bot, nil
		}
		lastErr = err
		backoff := time.Duration(i*2) * time.Second
		log.Printf("telegram init failed (attempt %d/8): %v; retry in %s", i, err, backoff)
		time.Sleep(backoff)
	}
	return nil, fmt.Errorf("telegram init failed after retries: %w", lastErr)
}

func updateState(mu *sync.RWMutex, states map[int64]*userSession, uid int64, st sessionState) {
	mu.Lock()
	defer mu.Unlock()
	if st == stateIdle {
		delete(states, uid)
		return
	}
	sess := states[uid]
	if sess == nil {
		sess = &userSession{}
		states[uid] = sess
	}
	sess.State = st
}

func send(bot *tgbotapi.BotAPI, userID int64, text string) {
	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = mainMenuKeyboard()
	if _, err := bot.Send(msg); err != nil {
		log.Printf("send failed: %v", err)
	}
}

func mainMenuKeyboard() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnSetToken),
			tgbotapi.NewKeyboardButton(btnSetLimits),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnStartSniper),
			tgbotapi.NewKeyboardButton(btnStopSniper),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnAccount),
			tgbotapi.NewKeyboardButton(btnVolume),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnClearToken),
			tgbotapi.NewKeyboardButton(btnHelp),
		),
	)
	kb.ResizeKeyboard = true
	kb.OneTimeKeyboard = false
	kb.Selective = false
	return kb
}

func normalizeCommandInput(text string) string {
	switch text {
	case btnSetToken:
		return "/settoken"
	case btnSetLimits:
		return "/setlimits"
	case btnStartSniper:
		return "/startsniper"
	case btnStopSniper:
		return "/stopsniper"
	case btnClearToken:
		return "/cleartoken"
	case btnAccount:
		return "/account"
	case btnVolume:
		return "/volume"
	case btnHelp:
		return "/start"
	default:
		return text
	}
}

func cleanToken(raw string) string {
	s := strings.TrimSpace(strings.Trim(raw, `"'`))
	if strings.Contains(s, "access_token=") {
		s = strings.SplitN(s, "access_token=", 2)[1]
	}
	if idx := strings.Index(s, ";"); idx > -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func getFirstActiveAccount(token string) (string, string, error) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://app.cr.bot/internal/v1/p2c/accounts", nil)
	req.Header.Set("Cookie", "access_token="+token)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", err
	}
	if len(parsed.Data) == 0 {
		return "", "", fmt.Errorf("токен рабочий, но нет реквизитов")
	}
	acc := parsed.Data[0]
	id, _ := acc["id"].(string)
	title, _ := acc["title"].(string)
	if title == "" {
		title, _ = acc["bank_code"].(string)
	}
	cur, _ := acc["currency"].(string)
	if cur == "" {
		cur = "RUB"
	}
	return id, title + " (" + cur + ")", nil
}

func restoreRunners(database *db.DB, active map[int64]*sniper.Bot, activeMu *sync.RWMutex, cfg sniper.Config, notifier sniper.TelegramNotifier) {
	runners, err := database.GetActiveRunners()
	if err != nil {
		log.Printf("restore error: %v", err)
		return
	}
	for _, u := range runners {
		sb := sniper.New(u, cfg, database, notifier)
		activeMu.Lock()
		active[u.UserID] = sb
		activeMu.Unlock()
		go func(id int64, b *sniper.Bot) {
			b.Start()
			activeMu.Lock()
			delete(active, id)
			activeMu.Unlock()
		}(u.UserID, sb)
	}
}

func dailyReportsLoop(ctx context.Context, database *db.DB, bot *tgbotapi.BotAPI, admins map[int64]struct{}) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}

		users, err := database.GetAllUsers()
		if err != nil {
			continue
		}
		total := 0.0
		report := "📊 <b>Daily Admin Report</b>\n\n"
		for _, u := range users {
			v, _ := database.GetDailyVolume(u.UserID)
			if v <= 0 {
				continue
			}
			total += v
			send(bot, u.UserID, fmt.Sprintf("🌙 <b>Ежедневный отчет</b>\nЗа сутки поймано: <b>%.2f RUB</b>", v))
			report += fmt.Sprintf("👤 %s (ID: <code>%d</code>): %.0f₽\n", u.Username, u.UserID, v)
		}
		report += fmt.Sprintf("\n💰 <b>Total System: %.0f RUB</b>", total)
		for id := range admins {
			send(bot, id, report)
		}
	}
}

func sendCompleteRequest(bot *tgbotapi.BotAPI, userID int64, token string, paymentID int64, reqID string) {
	url := fmt.Sprintf("https://app.send.tg/internal/v1/p2c/payments/%d/complete", paymentID)
	body := fmt.Sprintf(`{"method":%q}`, reqID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		send(bot, userID, "❌ Ошибка формирования запроса: "+err.Error())
		return
	}
	req.Header.Set("Cookie", "access_token="+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		send(bot, userID, "❌ Ошибка запроса: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		send(bot, userID, "✅ Оплата подтверждена!")
	} else {
		body, _ := io.ReadAll(resp.Body)
		send(bot, userID, fmt.Sprintf("⚠️ Сервер вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
}
