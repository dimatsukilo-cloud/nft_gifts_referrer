package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	tele "gopkg.in/telebot.v4"
	_ "modernc.org/sqlite"
)

// ---------- настройки ----------

const (
	offerTTL           = 12 * time.Hour
	disableLinkPreview = true
	defaultEmoji       = "🎁"
	codeAlphabet       = "abcdefghijklmnopqrstuvwxyz0123456789"
	codeLen            = 7
)

var (
	nftRe   = regexp.MustCompile(`(?i)t\.me/nft/([A-Za-z0-9_]+)-(\d+)`)
	camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	numRe   = regexp.MustCompile(`\d+`)
	prefRe  = regexp.MustCompile(`^\s*\d+\s*[.)]\s*`)
	codeRe  = regexp.MustCompile(`^[a-z0-9]{7}$`)
)

// ---------- модель ----------

type Offer struct {
	ID        int64
	Code      string
	NFTURL    string
	Stars     int64
	EmojiID   string
	EmojiChar string
	ChatID    int64
	MessageID int64
	BCID      string
	ExpiresAt int64
	State     string // draft | sent | accepted
	Deleted   bool
}

type App struct {
	bot           *tele.Bot
	db            *sql.DB
	ownerID       int64
	ownerUsername string
	giftURL       string

	mu      sync.Mutex
	pending map[int64]bool
}

// ---------- сборка текста с entities (offset/length в UTF-16) ----------

type entity struct {
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	URL           string `json:"url,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

type builder struct {
	sb   strings.Builder
	n    int
	ents []entity
}

func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

func (b *builder) text(s string) {
	b.sb.WriteString(s)
	b.n += u16len(s)
}

func (b *builder) withEntity(s string, e entity) {
	e.Offset = b.n
	e.Length = u16len(s)
	b.text(s)
	b.ents = append(b.ents, e)
}

func sliceU16(s string, off, ln int) string {
	u := utf16.Encode([]rune(s))
	if off < 0 || ln <= 0 || off+ln > len(u) {
		return ""
	}
	return string(utf16.Decode(u[off : off+ln]))
}

func hoursWord(n int) string {
	m100, m10 := n%100, n%10
	if m100 >= 11 && m100 <= 14 {
		return "часов"
	}
	switch {
	case m10 == 1:
		return "час"
	case m10 >= 2 && m10 <= 4:
		return "часа"
	}
	return "часов"
}

// parseNFT: t.me/nft/DeskCalendar-239500 -> "Desk Calendar #239500", нормализованная ссылка
func parseNFT(raw string) (name, url string, ok bool) {
	m := nftRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", false
	}
	name = camelRe.ReplaceAllString(m[1], "$1 $2") + " #" + m[2]
	url = "https://t.me/nft/" + m[1] + "-" + m[2]
	return name, url, true
}

func (a *App) renderOffer(o *Offer) (string, []entity) {
	name, _, _ := parseNFT(o.NFTURL)
	if name == "" {
		name = o.NFTURL
	}
	hours := 12
	if o.ExpiresAt > 0 {
		rem := time.Until(time.Unix(o.ExpiresAt, 0))
		hours = int(math.Ceil(rem.Hours()))
		if hours < 1 {
			hours = 1
		}
	}

	var b builder
	b.text(fmt.Sprintf("@%s предлагает вам %d ", a.ownerUsername, o.Stars))
	b.withEntity("⭐️", entity{Type: "custom_emoji", CustomEmojiID: starEmojiID})
	b.text("Stars за подарок ")
	ch := o.EmojiChar
	if ch == "" {
		ch = defaultEmoji
	}
	if o.EmojiID != "" {
		b.withEntity(ch, entity{Type: "custom_emoji", CustomEmojiID: o.EmojiID})
	} else {
		b.text(ch)
	}
	b.text(" ")
	b.withEntity(name, entity{Type: "text_link", URL: o.NFTURL})
	b.text(fmt.Sprintf("\n\nЭто неофициальное предложение, а не оффер Telegram или Fragment. Оплату в Stars отправит лично @%s после получения подарка.", a.ownerUsername))
	b.text(fmt.Sprintf("\n\nПредложение действительно ещё %d %s", hours, hoursWord(hours)))
	return b.sb.String(), b.ents
}

// ---------- клавиатуры (сырой JSON) ----------

type btn map[string]string

func kb(rows ...[]btn) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}

// Иконки кнопок. Пока CustomID пустой, в начало текста кнопки ставится обычный
// эмодзи Emoji. Как только впишешь сюда id премиум-эмодзи, кнопка получит
// премиум-иконку (поле icon_custom_emoji_id), а обычный эмодзи из текста уберётся.
type icon struct {
	Emoji    string
	CustomID string
}

// Премиум-эмодзи для слова Stars в тексте оффера.
const starEmojiID = "6005661956931850799"

var icons = map[string]icon{
	"reject": {Emoji: "❌", CustomID: "5219776129669276751"}, // Отклонить
	"accept": {Emoji: "✅", CustomID: "5220049680431326552"}, // Принять
	"yes":    {Emoji: "👍", CustomID: "5220049680431326552"}, // Да
	"no":     {Emoji: "🔙", CustomID: "5219776129669276751"}, // Нет
	"give":   {Emoji: "🎁", CustomID: "5974412071238897156"}, // Передать NFT
	"how":    {Emoji: "💡", CustomID: "6001517450930163276"}, // Как передать NFT
}

// withIcon возвращает кнопку с иконкой по ключу из icons.
func withIcon(key, text string, b btn) btn {
	ic := icons[key]
	if ic.CustomID != "" {
		b["icon_custom_emoji_id"] = ic.CustomID
		b["text"] = text
	} else if ic.Emoji != "" {
		b["text"] = ic.Emoji + " " + text
	} else {
		b["text"] = text
	}
	return b
}

func cbBtn(key, text, action string, id int64) btn {
	return withIcon(key, text, btn{"callback_data": fmt.Sprintf("%s:%d", action, id)})
}

func (a *App) mainKB(id int64) map[string]any {
	return kb([]btn{cbBtn("reject", "Отклонить", "rej", id), cbBtn("accept", "Принять", "acc", id)})
}

func (a *App) previewKB() map[string]any {
	return kb([]btn{cbBtn("reject", "Отклонить", "noop", 0), cbBtn("accept", "Принять", "noop", 0)})
}

func (a *App) confirmKB(id int64) map[string]any {
	return kb([]btn{cbBtn("yes", "Да", "rejy", id), cbBtn("no", "Нет", "rejn", id)})
}

func (a *App) acceptedKB(id int64) map[string]any {
	url := strings.ReplaceAll(a.giftURL, "{username}", a.ownerUsername)
	return kb(
		[]btn{withIcon("give", "Передать NFT", btn{"url": url})},
		[]btn{cbBtn("how", "Как передать NFT", "how", id)},
	)
}

func (a *App) howText() string {
	return fmt.Sprintf("Нажмите «Передать NFT», выберите @%s и отправьте подарок. Это личное предложение: Stars отправит сам @%s после получения подарка.", a.ownerUsername, a.ownerUsername)
}

// ---------- сырые вызовы Bot API ----------

func (a *App) sendOffer(chatID int64, bcid string, o *Offer, kbd map[string]any) (int64, error) {
	text, ents := a.renderOffer(o)
	p := map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"entities":     ents,
		"reply_markup": kbd,
	}
	if disableLinkPreview {
		p["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	if bcid != "" {
		p["business_connection_id"] = bcid
	}
	data, err := a.bot.Raw("sendMessage", p)
	if err != nil {
		return 0, err
	}
	var r struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, err
	}
	return r.Result.MessageID, nil
}

func (a *App) editMessage(o *Offer, text string, ents []entity, kbd map[string]any) error {
	p := map[string]any{
		"chat_id":      o.ChatID,
		"message_id":   o.MessageID,
		"text":         text,
		"reply_markup": kbd,
	}
	if len(ents) > 0 {
		p["entities"] = ents
	}
	if disableLinkPreview {
		p["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	if o.BCID != "" {
		p["business_connection_id"] = o.BCID
	}
	_, err := a.bot.Raw("editMessageText", p)
	return err
}

func (a *App) deleteMessage(chatID, messageID int64, bcid string) error {
	if bcid != "" {
		_, err := a.bot.Raw("deleteBusinessMessages", map[string]any{
			"business_connection_id": bcid,
			"message_ids":            []int64{messageID},
		})
		return err
	}
	_, err := a.bot.Raw("deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID})
	return err
}

// ---------- БД ----------

func (a *App) initDB() error {
	_, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS offers(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		code TEXT UNIQUE,
		nft_url TEXT NOT NULL,
		stars INTEGER NOT NULL,
		emoji_id TEXT NOT NULL DEFAULT '',
		emoji_char TEXT NOT NULL DEFAULT '',
		chat_id INTEGER NOT NULL DEFAULT 0,
		message_id INTEGER NOT NULL DEFAULT 0,
		bcid TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'draft',
		deleted INTEGER NOT NULL DEFAULT 0
	)`)
	return err
}

const offerCols = `id, COALESCE(code,''), nft_url, stars, emoji_id, emoji_char, chat_id, message_id, bcid, expires_at, state, deleted`

type scanner interface{ Scan(dest ...any) error }

func scanOffer(s scanner) (*Offer, error) {
	o := &Offer{}
	var del int
	err := s.Scan(&o.ID, &o.Code, &o.NFTURL, &o.Stars, &o.EmojiID, &o.EmojiChar,
		&o.ChatID, &o.MessageID, &o.BCID, &o.ExpiresAt, &o.State, &del)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Deleted = del == 1
	return o, nil
}

func (a *App) byID(id int64) (*Offer, error) {
	return scanOffer(a.db.QueryRow(`SELECT `+offerCols+` FROM offers WHERE id=?`, id))
}

func (a *App) byCode(code string) (*Offer, error) {
	return scanOffer(a.db.QueryRow(`SELECT `+offerCols+` FROM offers WHERE code=? AND state='draft'`, code))
}

func randomCode() (string, error) {
	out := make([]byte, codeLen)
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = codeAlphabet[n.Int64()]
	}
	return string(out), nil
}

func (a *App) createOffer(nftURL string, stars int64, emojiID, emojiChar string) (*Offer, error) {
	for i := 0; i < 10; i++ {
		code, err := randomCode()
		if err != nil {
			return nil, err
		}
		res, err := a.db.Exec(`INSERT INTO offers(code, nft_url, stars, emoji_id, emoji_char) VALUES(?,?,?,?,?)`,
			code, nftURL, stars, emojiID, emojiChar)
		if err != nil {
			continue // коллизия кода — пробуем другой
		}
		id, _ := res.LastInsertId()
		return a.byID(id)
	}
	return nil, fmt.Errorf("не удалось сгенерировать код")
}

// ---------- диалог с владельцем ----------

func (a *App) setPending(id int64, v bool) {
	a.mu.Lock()
	a.pending[id] = v
	a.mu.Unlock()
}

func (a *App) isPending(id int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending[id]
}

func (a *App) ownerOnly(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Sender() == nil || c.Sender().ID != a.ownerID {
			return nil
		}
		return next(c)
	}
}

const askText = "Какой создать оффер? Напиши одним сообщением в столбик:\n\n" +
	"1. Ссылка на NFT (например t.me/nft/DeskCalendar-239500)\n" +
	"2. Сколько stars предложить\n" +
	"3. Премиум-эмодзи, который будет перед названием NFT"

func (a *App) onStart(c tele.Context) error {
	a.setPending(c.Sender().ID, true)
	return c.Send(askText)
}

func (a *App) onText(c tele.Context) error {
	if !a.isPending(c.Sender().ID) {
		return c.Send("Напиши /start, чтобы создать оффер.")
	}
	msg := c.Message()

	var lines []string
	for _, l := range strings.Split(msg.Text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 3 {
		return c.Send("Нужно три строки: ссылка на NFT, сумма в stars, эмодзи.\n\n" + askText)
	}

	_, nftURL, ok := parseNFT(lines[0])
	if !ok {
		return c.Send("Не распознал ссылку на NFT. Нужен формат t.me/nft/Название-12345.")
	}
	numStr := numRe.FindString(prefRe.ReplaceAllString(lines[1], ""))
	stars, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || stars <= 0 {
		return c.Send("Не понял сумму stars. Напиши число, например 900.")
	}

	// премиум-эмодзи берём из entities сообщения
	var emojiID, emojiChar string
	for _, e := range msg.Entities {
		if string(e.Type) == "custom_emoji" {
			emojiID = e.CustomEmojiID
			emojiChar = sliceU16(msg.Text, e.Offset, e.Length)
			break
		}
	}
	if emojiID == "" { // обычный эмодзи как запасной вариант
		l3 := prefRe.ReplaceAllString(lines[2], "")
		if r := []rune(strings.TrimSpace(l3)); len(r) > 0 && len(r) <= 8 {
			emojiChar = string(r)
		}
	}
	if emojiChar == "" {
		return c.Send("Не нашёл эмодзи в третьей строке.")
	}

	o, err := a.createOffer(nftURL, stars, emojiID, emojiChar)
	if err != nil {
		log.Println("createOffer:", err)
		return c.Send("Ошибка базы данных, попробуй ещё раз.")
	}
	a.setPending(c.Sender().ID, false)

	if _, err := a.sendOffer(c.Chat().ID, "", o, a.previewKB()); err != nil {
		log.Println("preview:", err)
	}
	return c.Send(fmt.Sprintf("Предпросмотр выше. Чтобы отправить оффер, напиши в чате с пользователем:\n<code>/%s</code>", o.Code), tele.ModeHTML)
}

// ---------- кнопки под оффером ----------

func (a *App) onCallback(c tele.Context) error {
	cb := c.Callback()
	data := strings.TrimPrefix(cb.Data, "\f")
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 {
		return c.Respond()
	}
	action := parts[0]
	id, _ := strconv.ParseInt(parts[1], 10, 64)

	if action == "noop" {
		return c.Respond(&tele.CallbackResponse{Text: "Это предпросмотр, кнопки неактивны"})
	}

	o, err := a.byID(id)
	if err != nil || o == nil || o.Deleted || o.MessageID == 0 {
		return c.Respond(&tele.CallbackResponse{Text: "Оффер уже недоступен", ShowAlert: true})
	}

	switch action {
	case "rej":
		if err := a.editMessage(o, "Вы уверены что хотите отклонить оффер?", nil, a.confirmKB(o.ID)); err != nil {
			log.Println("edit confirm:", err)
		}
	case "rejn":
		text, ents := a.renderOffer(o)
		kbd := a.mainKB(o.ID)
		if o.State == "accepted" {
			kbd = a.acceptedKB(o.ID)
		}
		if err := a.editMessage(o, text, ents, kbd); err != nil {
			log.Println("edit back:", err)
		}
	case "rejy":
		if err := a.deleteMessage(o.ChatID, o.MessageID, o.BCID); err != nil {
			log.Println("delete:", err)
		}
		a.db.Exec(`UPDATE offers SET deleted=1 WHERE id=?`, o.ID)
	case "acc":
		a.db.Exec(`UPDATE offers SET state='accepted' WHERE id=?`, o.ID)
		o.State = "accepted"
		text, ents := a.renderOffer(o)
		if err := a.editMessage(o, text, ents, a.acceptedKB(o.ID)); err != nil {
			log.Println("edit accept:", err)
		}
	case "how":
		return c.Respond(&tele.CallbackResponse{Text: a.howText(), ShowAlert: true})
	}
	return c.Respond()
}

// ---------- business-сообщения: команда /код в чате с пользователем ----------

type bizMessage struct {
	MessageID            int64  `json:"message_id"`
	BusinessConnectionID string `json:"business_connection_id"`
	Text                 string `json:"text"`
	Chat                 struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

func (a *App) onBusinessMessage(m *bizMessage) {
	if m.From.ID != a.ownerID {
		return
	}
	t := strings.Fields(strings.TrimSpace(m.Text))
	if len(t) == 0 || !strings.HasPrefix(t[0], "/") {
		return
	}
	code := strings.ToLower(strings.TrimPrefix(t[0], "/"))
	if !codeRe.MatchString(code) {
		return
	}
	o, err := a.byCode(code)
	if err != nil || o == nil {
		return
	}
	// забираем код атомарно: после использования его в базе больше нет
	res, err := a.db.Exec(`UPDATE offers SET code=NULL WHERE id=? AND code IS NOT NULL`, o.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}

	o.ChatID = m.Chat.ID
	o.BCID = m.BusinessConnectionID
	o.ExpiresAt = time.Now().Add(offerTTL).Unix()

	mid, err := a.sendOffer(o.ChatID, o.BCID, o, a.mainKB(o.ID))
	if err != nil {
		log.Println("send offer:", err)
		a.db.Exec(`UPDATE offers SET code=? WHERE id=?`, code, o.ID) // возвращаем код
		a.bot.Send(&tele.User{ID: a.ownerID}, "Не удалось отправить оффер: "+err.Error())
		return
	}
	a.db.Exec(`UPDATE offers SET chat_id=?, message_id=?, bcid=?, expires_at=?, state='sent' WHERE id=?`,
		o.ChatID, mid, o.BCID, o.ExpiresAt, o.ID)

	// одноразовая команда /код сразу удаляется из чата
	if err := a.deleteMessage(m.Chat.ID, m.MessageID, m.BusinessConnectionID); err != nil {
		log.Println("delete command:", err)
	}
}

// ---------- автоудаление через 12 часов ----------

func (a *App) reaper() {
	t := time.NewTicker(30 * time.Second)
	for range t.C {
		rows, err := a.db.Query(`SELECT `+offerCols+` FROM offers WHERE state IN ('sent','accepted') AND deleted=0 AND expires_at>0 AND expires_at<=?`, time.Now().Unix())
		if err != nil {
			log.Println("reaper:", err)
			continue
		}
		var expired []*Offer
		for rows.Next() {
			if o, err := scanOffer(rows); err == nil && o != nil {
				expired = append(expired, o)
			}
		}
		rows.Close()
		for _, o := range expired {
			if err := a.deleteMessage(o.ChatID, o.MessageID, o.BCID); err != nil {
				log.Println("expire delete:", err)
			}
			a.db.Exec(`UPDATE offers SET deleted=1 WHERE id=?`, o.ID)
		}
	}
}

// ---------- свой getUpdates (telebot не знает business_message) ----------

func (a *App) pollLoop() {
	offset := 0
	allowed := []string{"message", "callback_query", "business_message"}
	for {
		data, err := a.bot.Raw("getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         30,
			"allowed_updates": allowed,
		})
		if err != nil {
			log.Println("getUpdates:", err)
			time.Sleep(3 * time.Second)
			continue
		}
		var resp struct {
			Result []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			log.Println("decode updates:", err)
			continue
		}
		for _, raw := range resp.Result {
			var head struct {
				UpdateID int         `json:"update_id"`
				BM       *bizMessage `json:"business_message"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				continue
			}
			offset = head.UpdateID + 1
			if head.BM != nil {
				go a.onBusinessMessage(head.BM)
				continue
			}
			var u tele.Update
			if err := json.Unmarshal(raw, &u); err != nil {
				continue
			}
			a.bot.ProcessUpdate(u)
		}
	}
}

// ---------- main ----------

func main() {
	token := os.Getenv("BOT_TOKEN")
	ownerID, _ := strconv.ParseInt(os.Getenv("OWNER_ID"), 10, 64)
	username := strings.TrimPrefix(os.Getenv("OWNER_USERNAME"), "@")
	if token == "" || ownerID == 0 || username == "" {
		log.Fatal("нужны переменные окружения BOT_TOKEN, OWNER_ID, OWNER_USERNAME")
	}
	giftURL := os.Getenv("GIFT_URL")
	if giftURL == "" {
		giftURL = "tg://premium_multigift?to={username}"
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "offers.db"
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Client: &http.Client{Timeout: 45 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	a := &App{
		bot:           b,
		db:            db,
		ownerID:       ownerID,
		ownerUsername: username,
		giftURL:       giftURL,
		pending:       map[int64]bool{},
	}
	if err := a.initDB(); err != nil {
		log.Fatal(err)
	}

	b.Handle("/start", a.onStart, a.ownerOnly)
	b.Handle(tele.OnText, a.onText, a.ownerOnly)
	b.Handle(tele.OnCallback, a.onCallback)

	go a.reaper()
	log.Println("бот запущен")
	a.pollLoop()
}
