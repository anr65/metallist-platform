package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

type App struct {
	db       *sql.DB
	storage  string
	location *time.Location
}
type User struct{ ID, Login, Name, Role string }
type M map[string]interface{}
type Posting struct {
	Account, Side                            string
	Amount                                   int64
	Merchant, Card, Custodian, Ref, Category string
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func token() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func digest(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func nilID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
func amount(s string) (int64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	p := strings.Split(s, ".")
	if len(p) > 2 || len(p[0]) == 0 || len(p[0]) > 15 {
		return 0, errors.New("некорректная сумма")
	}
	if len(p) == 2 && (len(p[1]) < 1 || len(p[1]) > 2) {
		return 0, errors.New("только копейки")
	}
	for _, c := range strings.ReplaceAll(s, ".", "") {
		if c < '0' || c > '9' {
			return 0, errors.New("некорректная сумма")
		}
	}
	v, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil {
		return 0, e
	}
	v *= 100
	if len(p) == 2 {
		q := p[1]
		if len(q) == 1 {
			q += "0"
		}
		n, _ := strconv.ParseInt(q, 10, 64)
		v += n
	}
	if v <= 0 {
		return 0, errors.New("сумма должна быть положительной")
	}
	return v, nil
}
func rub(v int64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}
func fee(total int64, bp int) int64 {
	return (total/10000)*int64(bp) + ((total%10000)*int64(bp)+5000)/10000
}
func str(m M, k string) string { v, _ := m[k].(string); return v }
func jsonBody(r *http.Request) (M, error) {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var m M
	if e := dec.Decode(&m); e != nil {
		return nil, e
	}
	return m, nil
}
func decodeMap(raw []byte) (M, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var m M
	e := dec.Decode(&m)
	return m, e
}
func respond(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}
	conf, e := pgx.ParseConfig(dsn)
	if e != nil {
		log.Fatal("invalid database configuration")
	}
	db := sql.OpenDB(stdlib.GetConnector(*conf))
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		log.Fatal("database unavailable")
	}
	loc, e := time.LoadLocation("Europe/Moscow")
	if e != nil {
		log.Fatal(e)
	}
	storage := os.Getenv("DOCUMENT_DIR")
	if storage == "" {
		storage = "data/private"
	}
	if e = os.MkdirAll(storage, 0700); e != nil {
		log.Fatal(e)
	}
	if os.Getenv("APP_ENV") == "production" {
		if _, e = panAEAD(); e != nil {
			log.Fatal("защищённое хранение карт не настроено")
		}
		vaultDir := os.Getenv("PAN_VAULT_DIR")
		if vaultDir == "" || !filepath.IsAbs(vaultDir) {
			log.Fatal("каталог номеров карт не настроен")
		}
		if e = os.MkdirAll(vaultDir, 0700); e != nil {
			log.Fatal("каталог номеров карт недоступен")
		}
		info, statErr := os.Stat(vaultDir)
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			log.Fatal("каталог номеров карт должен быть закрыт для других пользователей")
		}
		entries, readErr := os.ReadDir(vaultDir)
		if readErr != nil {
			log.Fatal("не удалось проверить каталог номеров карт")
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".pan") {
				log.Fatal("неожиданный файл в каталоге номеров карт")
			}
			cardID := strings.TrimSuffix(entry.Name(), ".pan")
			if _, e = loadPAN(cardID); e != nil {
				log.Fatal("ключ не открывает сохранённые номера карт")
			}
		}
		cipherRows, queryErr := db.Query("SELECT id,pan_ciphertext FROM cards WHERE pan_ciphertext IS NOT NULL")
		if queryErr != nil {
			log.Fatal("не удалось проверить исправленные номера карт")
		}
		for cipherRows.Next() {
			var cardID string
			var ciphertext []byte
			if queryErr = cipherRows.Scan(&cardID, &ciphertext); queryErr != nil {
				break
			}
			if _, queryErr = readCardPAN(cardID, ciphertext); queryErr != nil {
				break
			}
		}
		if queryErr == nil {
			queryErr = cipherRows.Err()
		}
		cipherRows.Close()
		if queryErr != nil {
			log.Fatal("ключ не открывает исправленные номера карт")
		}
	}
	app := &App{db, storage, loc}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sync-nspk-banks":
			if os.Getenv("APP_ENV") != "production" || len(os.Args) != 2 {
				log.Fatal("обновление справочника НСПК разрешено только в production")
			}
			syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer syncCancel()
			count, syncErr := app.syncNSPKBanks(syncCtx, officialNSPKClient(), nspkBanksURL)
			if syncErr != nil {
				log.Fatal(syncErr)
			}
			fmt.Printf("справочник НСПК обновлён: %d банков\n", count)
			return
		case "import-card-pans":
			if os.Getenv("APP_ENV") != "production" {
				log.Fatal("импорт полных номеров разрешён только в production")
			}
			created, existing, importErr := app.importCardPANs(os.Stdin)
			if importErr != nil {
				log.Fatal(importErr)
			}
			fmt.Printf("сохранено: %d; уже было сохранено: %d\n", created, existing)
			return
		case "migrate":
			log.Fatal("use the guarded deployment migration procedure")
		case "set-password":
			if len(os.Args) != 4 {
				log.Fatal("set-password LOGIN PASSWORD_FILE")
			}
			if e = app.setPassword(os.Args[2], os.Args[3]); e != nil {
				log.Fatal(e)
			}
			return
		case "bootstrap":
			if len(os.Args) != 4 {
				log.Fatal("bootstrap CHIEF_PASSWORD_FILE SYSADMIN_PASSWORD_FILE")
			}
			if e = app.bootstrap(os.Args[2], os.Args[3]); e != nil {
				log.Fatal(e)
			}
			return
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, M{"status": "ok"}) })
	mux.HandleFunc("/login", app.login)
	mux.HandleFunc("/logout", app.auth(app.logout))
	mux.HandleFunc("/api/me", app.auth(app.me))
	mux.HandleFunc("/api/password", app.auth(app.changePassword))
	mux.HandleFunc("/api/catalog", app.auth(app.catalog))
	mux.HandleFunc("/api/catalog/create", app.auth(app.catalogCreate))
	mux.HandleFunc("/api/catalog/update", app.auth(app.catalogUpdate))
	mux.HandleFunc("/api/merchant/update", app.auth(app.merchantUpdate))
	mux.HandleFunc("/api/merchant/delete", app.auth(app.merchantDelete))
	mux.HandleFunc("/api/card/pan/replace", app.auth(app.replaceCardPAN))
	mux.HandleFunc("/api/user/telegram", app.auth(app.linkTelegram))
	mux.HandleFunc("/api/registry/upload", app.auth(app.upload))
	mux.HandleFunc("/api/registry/manual", app.auth(app.createManualRegistry))
	mux.HandleFunc("/api/registry/manual/cards", app.auth(app.manualRegistryCards))
	mux.HandleFunc("/api/payment-request/create", app.auth(app.createPaymentRequest))
	mux.HandleFunc("/api/payment-request/update", app.auth(app.updatePaymentRequest))
	mux.HandleFunc("/api/payment-request/delete", app.auth(app.deletePaymentRequest))
	mux.HandleFunc("/api/payment-request/next-reference", app.auth(app.paymentRequestNextReference))
	mux.HandleFunc("/api/payment-contacts", app.auth(app.paymentContacts))
	mux.HandleFunc("/api/payment-requests", app.auth(app.paymentRequests))
	mux.HandleFunc("/api/payment-request", app.auth(app.paymentRequest))
	mux.HandleFunc("/api/payment-request/rows", app.auth(app.paymentRequestRows))
	mux.HandleFunc("/api/payment-request/export", app.auth(app.paymentRequestExport))
	mux.HandleFunc("/api/registries", app.auth(app.registries))
	mux.HandleFunc("/api/registry/rows", app.auth(app.registryRows))
	mux.HandleFunc("/api/registry/candidates", app.auth(app.registryCandidates))
	mux.HandleFunc("/api/registry/reconcile", app.auth(app.reconcileRegistryRow))
	mux.HandleFunc("/api/registry/manual-rate", app.auth(app.manualRate))
	mux.HandleFunc("/api/manual/preview", app.auth(app.manualPreview))
	mux.HandleFunc("/api/manual/confirm", app.auth(app.manualConfirm))
	mux.HandleFunc("/api/registry/confirm", app.auth(app.confirmRegistry))
	mux.HandleFunc("/api/registry/delete", app.auth(app.deleteRegistry))
	mux.HandleFunc("/api/registry/reverse", app.auth(app.reverseRegistry))
	mux.HandleFunc("/api/tariff/preview", app.auth(app.tariffPreview))
	mux.HandleFunc("/api/tariff/confirm", app.auth(app.tariffConfirm))
	mux.HandleFunc("/api/draft", app.auth(app.draft))
	mux.HandleFunc("/api/drafts", app.auth(app.drafts))
	mux.HandleFunc("/api/draft/confirm", app.auth(app.confirmDraft))
	mux.HandleFunc("/api/draft/reject", app.auth(app.rejectDraft))
	mux.HandleFunc("/api/draft/reverse", app.auth(app.reverseDraft))
	mux.HandleFunc("/api/observation", app.auth(app.observation))
	mux.HandleFunc("/api/report", app.auth(app.report))
	mux.HandleFunc("/api/balances", app.auth(app.balances))
	mux.HandleFunc("/api/report/approve", app.auth(app.approveReport))
	mux.HandleFunc("/api/audit", app.auth(app.audit))
	mux.HandleFunc("/telegram/webhook", app.telegram)
	mux.HandleFunc("/assets/app.js", app.javascript)
	mux.HandleFunc("/assets/app.css", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, []byte(appCSS), "text/css; charset=utf-8", false)
	})
	mux.HandleFunc("/assets/fonts/geist-sans.woff2", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, r, geistSans, "font/woff2", true) })
	mux.HandleFunc("/assets/fonts/geist-mono.woff2", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, r, geistMono, "font/woff2", true) })
	mux.HandleFunc("/", app.ui)
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if telegramToken() != "" {
		go app.telegramSetupCommands()
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, secureHeaders(mux)))
}
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		for key := range r.URL.Query() {
			if strings.EqualFold(key, "login") || strings.EqualFold(key, "password") {
				if r.URL.Path != "/" {
					w.Header().Set("Cache-Control", "no-store")
					fail(w, http.StatusBadRequest, errors.New("реквизиты входа не принимаются в адресе"))
					return
				}
				break
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && r.URL.Path != "/login" && r.URL.Path != "/telegram/webhook" {
			if r.Header.Get("X-CSRF") != "1" {
				fail(w, 403, errors.New("CSRF header required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) setPassword(login, file string) error {
	b, e := os.ReadFile(file)
	if e != nil {
		return e
	}
	pass := strings.TrimSpace(string(b))
	if len(pass) < 16 {
		return errors.New("password too short")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var userID string
	e = tx.QueryRow("UPDATE users SET password_hash=$1 WHERE login=$2 RETURNING id", string(hash), login).Scan(&userID)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=$1", userID); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO audit_events(id,actor_role,channel,action,object_type,object_id,outcome) VALUES($1,'system','cli','password_reset','user',$2,'success')", id(), userID); e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) bootstrap(chiefFile, adminFile string) error {
	b, e := os.ReadFile(chiefFile)
	if e != nil {
		return e
	}
	pass := strings.TrimSpace(string(b))
	if len(pass) < 16 {
		return errors.New("password too short")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	b, e = os.ReadFile(adminFile)
	if e != nil {
		return e
	}
	pass = strings.TrimSpace(string(b))
	if len(pass) < 16 {
		return errors.New("admin password too short")
	}
	ah, e := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errors.New("users already exist")
	}
	_, e = tx.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'chief','Главный администратор','chief',$2),($3,'sysadmin','Системный администратор','sysadmin',$4)", id(), string(h), id(), string(ah))
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Главный администратор','chief')", id())
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, errors.New("method"))
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	var u User
	var hash string
	var active bool
	e = a.db.QueryRow("SELECT id,login,name,role,password_hash,active FROM users WHERE login=$1", str(m, "login")).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &hash, &active)
	if e != nil || !active || bcrypt.CompareHashAndPassword([]byte(hash), []byte(str(m, "password"))) != nil {
		fail(w, 401, errors.New("неверный вход"))
		return
	}
	t := token()
	_, e = a.db.Exec("INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '12 hours')", digest(t), u.ID)
	if e != nil {
		fail(w, 500, errors.New("session error"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "metallist_session", Value: t, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, u)
}

type handler func(http.ResponseWriter, *http.Request, User)

func (a *App) auth(fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("metallist_session")
		if e != nil {
			fail(w, 401, errors.New("войдите"))
			return
		}
		var u User
		e = a.db.QueryRow("SELECT u.id,u.login,u.name,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active", digest(c.Value)).Scan(&u.ID, &u.Login, &u.Name, &u.Role)
		if e != nil {
			fail(w, 401, errors.New("войдите"))
			return
		}
		fn(w, r, u)
	}
}
func (a *App) logout(w http.ResponseWriter, r *http.Request, u User) {
	c, _ := r.Cookie("metallist_session")
	if c != nil {
		_, _ = a.db.Exec("DELETE FROM sessions WHERE token_hash=$1", digest(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "metallist_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	respond(w, 200, M{"ok": true})
}
func (a *App) me(w http.ResponseWriter, r *http.Request, u User) { respond(w, 200, u) }
func (a *App) changePassword(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" {
		fail(w, 405, errors.New("method"))
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	var old string
	e = a.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", u.ID).Scan(&old)
	if e != nil || bcrypt.CompareHashAndPassword([]byte(old), []byte(str(m, "current"))) != nil {
		fail(w, 403, errors.New("неверный текущий пароль"))
		return
	}
	next := str(m, "new")
	if len(next) < 16 {
		fail(w, 400, errors.New("новый пароль не короче 16 символов"))
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if e != nil {
		fail(w, 500, e)
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE users SET password_hash=$1 WHERE id=$2", string(hash), u.ID); e != nil {
		fail(w, 500, e)
		return
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=$1", u.ID); e != nil {
		fail(w, 500, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "password_change", "user", u.ID, "success", "", M{}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "metallist_session", Value: "", Path: "/", HttpOnly: true, Secure: true, MaxAge: -1})
	respond(w, 200, M{"ok": true})
}
func (a *App) require(w http.ResponseWriter, u User, roles ...string) bool {
	for _, r := range roles {
		if u.Role == r {
			return true
		}
	}
	a.logAudit(u.ID, "web", "permission_denied", "access", "", "rejected", "role", M{})
	fail(w, 403, errors.New("недостаточно прав"))
	return false
}
func (a *App) logAudit(actor, channel, action, typ, obj, outcome, reason string, detail M) {
	b, _ := json.Marshal(detail)
	_, _ = a.db.Exec("INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail) VALUES($1,$2,(SELECT role FROM users WHERE id=$2),$3,$4,$5,$6,$7,$8,$9)", id(), nilID(actor), channel, action, typ, nilID(obj), outcome, reason, b)
}
func txAudit(tx *sql.Tx, actor, channel, action, typ, obj, outcome, reason string, detail M) error {
	_, e := tx.Exec("INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail) VALUES($1,$2,(SELECT role FROM users WHERE id=$2),$3,$4,$5,$6,$7,$8,$9)", id(), nilID(actor), channel, action, typ, nilID(obj), outcome, reason, encode(detail))
	return e
}
func (a *App) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(indexHTML))
}
func (a *App) javascript(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, []byte(appJS), "text/javascript; charset=utf-8", false)
}
func serveAsset(w http.ResponseWriter, r *http.Request, data []byte, mediaType string, immutable bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, http.StatusMethodNotAllowed, errors.New("method"))
		return
	}
	w.Header().Set("Content-Type", mediaType)
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	_, _ = w.Write(data)
}
func safeFilename(s string) string { return filepath.Base(strings.ReplaceAll(s, "\\", "/")) }

var cardMaskPattern = regexp.MustCompile(`^[0-9]{6}\*{6}[0-9]{4}$`)
