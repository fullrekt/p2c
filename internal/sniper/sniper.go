package sniper

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gorilla/websocket"
	qrcode "github.com/skip2/go-qrcode"
	"p2c-sniper/internal/db"
)

const (
	wsURL       = "wss://app.cr.bot/internal/v1/p2c-socket/?EIO=4&transport=websocket"
	apiTakeURL  = "https://app.cr.bot/internal/v1/p2c/payments/take"
	paymentsURL = "https://app.cr.bot/internal/v1/p2c/payments?size=20"
)

type Config struct {
	ConcurrentRequests int
	RequestTimeoutSec  int
	UserAgents         []string
}

type Notifier interface {
	Send(userID int64, text string)
	SendQRCode(userID int64, imgBytes []byte, nspkURL string, paymentID int64, reqID string)
}

type TelegramNotifier struct {
	Bot *tgbotapi.BotAPI
}

func (t TelegramNotifier) Send(userID int64, text string) {
	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "HTML"
	if _, err := t.Bot.Send(msg); err != nil {
		log.Printf("telegram send failed to %d: %v", userID, err)
	}
}

func (t TelegramNotifier) SendQRCode(userID int64, imgBytes []byte, nspkURL string, paymentID int64, reqID string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Я оплатил", fmt.Sprintf("complete:%d:%s", paymentID, reqID)),
		),
	)

	if len(imgBytes) > 0 {
		file := tgbotapi.FileBytes{Name: "payment-qr.png", Bytes: imgBytes}
		photo := tgbotapi.NewPhoto(userID, file)
		photo.Caption = "QR для оплаты"
		photo.ParseMode = "HTML"
		photo.ReplyMarkup = keyboard
		if _, sendErr := t.Bot.Send(photo); sendErr == nil {
			return
		} else {
			log.Printf("telegram send qr bytes failed to %d: %v", userID, sendErr)
		}
	}

	// Fallback: NSPK link with button.
	if nspkURL != "" {
		msg := tgbotapi.NewMessage(userID, `<a href="`+nspkURL+`">Оплатить через СБП</a>`)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = keyboard
		if _, err := t.Bot.Send(msg); err != nil {
			log.Printf("telegram send nspk link failed to %d: %v", userID, err)
		}
	}
}

type Bot struct {
	userID int64
	token  string
	reqID  string
	proxy  string

	min float64
	max float64

	cfg      Config
	db       *db.DB
	notifier Notifier

	httpClient *http.Client
	userAgent  string
	ctx        context.Context
	cancel     context.CancelFunc

	mu          sync.RWMutex
	knownOrders map[string]time.Time
}

func New(user db.User, cfg Config, dbase *db.DB, n Notifier) *Bot {
	ua := "Mozilla/5.0"
	if len(cfg.UserAgents) > 0 {
		ua = cfg.UserAgents[0]
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:    200,
		MaxConnsPerHost: 0,
		IdleConnTimeout: 60 * time.Second,
	}
	if user.Proxy != "" {
		if p, err := url.Parse(user.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(p)
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.RequestTimeoutSec) * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	b := &Bot{
		userID:      user.UserID,
		token:       user.AccessToken,
		reqID:       user.ReqID,
		proxy:       user.Proxy,
		min:         user.MinAmount,
		max:         user.MaxAmount,
		cfg:         cfg,
		db:          dbase,
		notifier:    n,
		httpClient:  client,
		userAgent:   ua,
		ctx:         ctx,
		cancel:      cancel,
		knownOrders: make(map[string]time.Time),
	}
	return b
}

func (b *Bot) SetLimits(min, max float64) {
	b.mu.Lock()
	b.min = min
	b.max = max
	b.mu.Unlock()
}

func (b *Bot) Stop(reason string) {
	b.cancel()
	_ = b.db.SetRunningStatus(b.userID, false)
	if reason != "" {
		b.notifier.Send(b.userID, "🛑 <b>Снайпер остановлен!</b>\nПричина: "+reason)
	}
}

func (b *Bot) Start() {
	_ = b.db.SetRunningStatus(b.userID, true)
	go b.monitorPayments()
	go b.cleanKnownOrders()
	b.runWSLoop()
}

// cleanKnownOrders periodically removes knownOrders entries older than 2 hours
// to prevent unbounded memory growth.
func (b *Bot) cleanKnownOrders() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("cleanKnownOrders panic recovered user=%d: %v", b.userID, r)
		}
	}()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-2 * time.Hour)
			b.mu.Lock()
			for k, t := range b.knownOrders {
				if t.Before(cutoff) {
					delete(b.knownOrders, k)
				}
			}
			b.mu.Unlock()
		}
	}
}

func (b *Bot) runWSLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("runWSLoop panic recovered user=%d: %v", b.userID, r)
		}
	}()
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}
	if b.proxy != "" {
		if p, err := url.Parse(b.proxy); err == nil {
			dialer.Proxy = http.ProxyURL(p)
		}
	}

	headers := http.Header{}
	headers.Set("Cookie", "access_token="+b.token)
	headers.Set("Origin", "https://app.cr.bot")
	headers.Set("Accept", "application/json, text/plain, */*")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", b.userAgent)

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		conn, _, err := dialer.Dial(wsURL, headers)
		if err != nil {
			log.Printf("socket connect failed user=%d: %v", b.userID, err)
			time.Sleep(3 * time.Second)
			continue
		}
		log.Printf("socket connected user=%d", b.userID)

		for {
			select {
			case <-b.ctx.Done():
				_ = conn.Close()
				return
			default:
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				break
			}
			payload := string(msg)

			switch {
			case strings.HasPrefix(payload, "42") && strings.Contains(payload, `"op":"add"`):
				b.handleAddPayload(payload)
			case strings.HasPrefix(payload, "0"):
				_ = conn.WriteMessage(websocket.TextMessage, []byte("40"))
			case payload == "2":
				_ = conn.WriteMessage(websocket.TextMessage, []byte("3"))
			case strings.HasPrefix(payload, "40"):
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`42["list:initialize"]`))
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func (b *Bot) handleAddPayload(payload string) {
	idx := strings.Index(payload, "[")
	if idx == -1 {
		return
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(payload[idx:]), &arr); err != nil || len(arr) < 2 {
		return
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(arr[1], &items); err != nil {
		return
	}

	for _, it := range items {
		var op string
		_ = json.Unmarshal(it["op"], &op)
		if op != "add" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(it["data"], &data); err != nil {
			continue
		}
		orderID, _ := data["id"].(string)
		amt := toFloat(data["in_amount"])
		if orderID == "" {
			continue
		}
		log.Printf("order seen user=%d id=%s amount=%.2f", b.userID, orderID, amt)
		b.mu.RLock()
		ok := amt >= b.min && amt <= b.max
		b.mu.RUnlock()
		if ok {
			log.Printf("order matched limits user=%d id=%s amount=%.2f", b.userID, orderID, amt)
			go b.tryTakeOrder(orderID, amt)
		}
	}
}

func (b *Bot) monitorPayments() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("monitorPayments panic recovered user=%d: %v", b.userID, r)
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			log.Printf("poll tick user=%d", b.userID)
			req, _ := http.NewRequestWithContext(b.ctx, http.MethodGet, paymentsURL, nil)
			req.Header.Set("Cookie", "access_token="+b.token)
			req.Header.Set("User-Agent", b.userAgent)
			resp, err := b.httpClient.Do(req)
			if err != nil {
				log.Printf("poll error user=%d err=%v", b.userID, err)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusUnauthorized {
				b.Stop("Токен истек (401). Обновите токен!")
				return
			}
			if resp.StatusCode != http.StatusOK {
				log.Printf("poll non-200 user=%d code=%d", b.userID, resp.StatusCode)
				continue
			}
			log.Printf("poll ok user=%d bytes=%d", b.userID, len(body))

			var parsed struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				continue
			}

			for _, ord := range parsed.Data {
				oid, _ := ord["id"].(string)
				st, _ := ord["status"].(string)
				key := oid + "_" + st

				b.mu.Lock()
				_, exists := b.knownOrders[key]
				if !exists && st == "completed" {
					b.knownOrders[key] = time.Now()
					amt := toFloat(ord["in_amount"])
					b.notifier.Send(b.userID, "✅ <b>Payment Completed!</b>\n\n💰 Amount: "+formatFloat(amt)+" RUB\n🆔 ID: <code>"+oid+"</code>")
				}
				if st == "canceled" {
					b.knownOrders[key] = time.Now()
				}
				b.mu.Unlock()
			}
		}
	}
}

func (b *Bot) tryTakeOrder(orderID string, amount float64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("tryTakeOrder panic recovered user=%d id=%s: %v", b.userID, orderID, r)
		}
	}()
	urlTake := apiTakeURL + "/" + orderID
	payload := []byte(`{"payment_method_id":"` + b.reqID + `"}`)
	log.Printf("take start user=%d id=%s amount=%.2f workers=%d", b.userID, orderID, amount, b.cfg.ConcurrentRequests)

	type result struct {
		code int
		body string
	}
	resultCh := make(chan result, b.cfg.ConcurrentRequests)
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()

	for i := 0; i < b.cfg.ConcurrentRequests; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlTake, bytes.NewReader(payload))
			req.Header.Set("Cookie", "access_token="+b.token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", b.userAgent)
			resp, err := b.httpClient.Do(req)
			if err != nil {
				resultCh <- result{code: 0}
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			resultCh <- result{code: resp.StatusCode, body: string(body)}
		}()
	}

	var sawInvalid bool
	for i := 0; i < b.cfg.ConcurrentRequests; i++ {
		r := <-resultCh
		if r.code != 0 {
			log.Printf("take response user=%d id=%s code=%d body=%s", b.userID, orderID, r.code, trim(r.body, 120))
		}
		if r.code == http.StatusOK {
			_ = b.db.LogOrder(orderID, b.userID, amount, "sniped")
			b.notifier.Send(b.userID, "🔔 <b>New Payment Detected!</b>\n\n💰 Amount: "+formatFloat(amount)+" RUB\n🆔 ID: <code>"+orderID+"</code>")
			nspkURL, _, paymentID := parseTakeQR(r.body)
			var imgBytes []byte
			if nspkURL != "" {
				imgBytes, _ = qrcode.Encode(nspkURL, qrcode.High, 512)
			}
			b.notifier.SendQRCode(b.userID, imgBytes, nspkURL, paymentID, b.reqID)
			cancel()
			return
		}
		if r.code == http.StatusUnauthorized {
			b.Stop("Токен истек во время захвата!")
			cancel()
			return
		}
		if r.code == http.StatusForbidden && strings.Contains(r.body, "MerchantPenalized") {
			b.notifier.Send(b.userID, buildPenaltyMessage(r.body))
			b.Stop("")
			cancel()
			return
		}
		if r.code == http.StatusBadRequest && strings.Contains(r.body, "InvalidStatus") {
			sawInvalid = true
		}
	}
	if sawInvalid {
		_ = b.db.LogOrder(orderID, b.userID, amount, "missed")
	}
	log.Printf("take miss user=%d id=%s amount=%.2f", b.userID, orderID, amount)
}

func buildPenaltyMessage(body string) string {
	var p struct {
		Error        string `json:"error"`
		PenaltyType  string `json:"penalty_type"`
		PenaltyEndAt string `json:"penalty_end_at"`
	}
	_ = json.Unmarshal([]byte(body), &p)

	reason := humanPenaltyReason(p.PenaltyType)

	var endAtStr string
	if t, err := time.Parse(time.RFC3339, p.PenaltyEndAt); err == nil {
		loc, errLoc := time.LoadLocation("Europe/Moscow")
		if errLoc != nil {
			loc = time.FixedZone("MSK", 3*3600)
		}
		endAtStr = t.In(loc).Format("02.01.2006 в 15:04:05 МСК")
	}

	msg := "🚫 <b>Мерчант временно отстранён</b>\n\n" +
		"📌 Причина: " + reason + "\n"
	if endAtStr != "" {
		msg += "⏰ Заявки снова можно будет брать: <b>" + endAtStr + "</b>\n\n"
	} else {
		msg += "\n"
	}
	msg += "🤖 Снайпер остановлен. Перезапустите его после окончания блокировки командой /run."
	return msg
}

func humanPenaltyReason(code string) string {
	switch code {
	case "excessive_cancellations":
		return "слишком много отмен заявок"
	case "slow_payments":
		return "медленная оплата заявок"
	case "":
		return "неизвестная причина"
	default:
		return code
	}
}

func formatFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconvFormat(v), "0"), ".")
}

func strconvFormat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func trim(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}


func parseTakeQR(body string) (nspkURL string, payload string, id int64) {
	var resp struct {
		Data struct {
			ID      int64  `json:"id"`
			URL     string `json:"url"`
			Payload string `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return "", "", 0
	}
	return strings.TrimSpace(resp.Data.URL), strings.TrimSpace(resp.Data.Payload), resp.Data.ID
}
