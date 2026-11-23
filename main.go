// Command portfolio-contact-api receives contact-form messages from the
// portfolio site, validates them, rate-limits each IP, stores them and
// (optionally) emails a notification. Standard library only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type message struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
	Website string `json:"website"` // honeypot: real users leave this empty
}

type config struct {
	port           string
	allowedOrigins map[string]bool
	dataFile       string
	smtpHost       string
	smtpPort       string
	smtpUser       string
	smtpPass       string
	notifyTo       string
}

func loadConfig() config {
	origins := map[string]bool{}
	for _, o := range strings.Split(getenv("ALLOWED_ORIGINS", "http://localhost:5173"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins[o] = true
		}
	}
	return config{
		port:           getenv("PORT", "8080"),
		allowedOrigins: origins,
		dataFile:       getenv("DATA_FILE", "messages.jsonl"),
		smtpHost:       os.Getenv("SMTP_HOST"),
		smtpPort:       getenv("SMTP_PORT", "587"),
		smtpUser:       os.Getenv("SMTP_USER"),
		smtpPass:       os.Getenv("SMTP_PASS"),
		notifyTo:       os.Getenv("NOTIFY_TO"),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// validate trims the fields and returns a user-safe error message.
func validate(m *message) error {
	m.Name = strings.TrimSpace(m.Name)
	m.Email = strings.TrimSpace(m.Email)
	m.Message = strings.TrimSpace(m.Message)
	if n := utf8.RuneCountInString(m.Name); n < 2 || n > 80 {
		return errors.New("name must be 2 to 80 characters")
	}
	if _, err := mail.ParseAddress(m.Email); err != nil || len(m.Email) > 120 {
		return errors.New("email is not valid")
	}
	if n := utf8.RuneCountInString(m.Message); n < 10 || n > 2000 {
		return errors.New("message must be 10 to 2000 characters")
	}
	if strings.ContainsAny(m.Name+m.Email, "\r\n") {
		return errors.New("invalid characters")
	}
	return nil
}

// limiter allows `limit` requests per `window` for each key (sliding window).
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

type server struct {
	cfg     config
	limiter *limiter
	store   *store
	notify  func(message) error
}

type store struct {
	mu   sync.Mutex
	path string
}

func (s *store) save(m message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	rec := struct {
		message
		At time.Time `json:"at"`
	}{m, time.Now().UTC()}
	rec.Website = ""
	return json.NewEncoder(f).Encode(rec)
}

func clientIP(r *http.Request) string {
	// Behind a proxy (Render, Fly) the real client is first in X-Forwarded-For.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !s.cfg.allowedOrigins[origin] {
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	return true
}

func (s *server) handleContact(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}
	if !s.limiter.allow(clientIP(r), time.Now()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many messages, try again later"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var m message
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if m.Website != "" { // bot filled the honeypot: pretend success, store nothing
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
		return
	}
	if err := validate(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.save(m); err != nil {
		log.Printf("save failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save message"})
		return
	}
	if s.notify != nil {
		if err := s.notify(m); err != nil {
			log.Printf("notify failed (message was saved): %v", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func smtpNotifier(c config) func(message) error {
	if c.smtpHost == "" || c.notifyTo == "" {
		return nil
	}
	return func(m message) error {
		body := "From: " + c.smtpUser + "\r\nTo: " + c.notifyTo + "\r\nReply-To: " + m.Email +
			"\r\nSubject: Portfolio message from " + m.Name + "\r\n\r\n" + m.Message + "\r\n\r\nReply to: " + m.Email + "\r\n"
		auth := smtp.PlainAuth("", c.smtpUser, c.smtpPass, c.smtpHost)
		return smtp.SendMail(c.smtpHost+":"+c.smtpPort, auth, c.smtpUser, []string{c.notifyTo}, []byte(body))
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/contact", s.handleContact)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func main() {
	cfg := loadConfig()
	s := &server{
		cfg:     cfg,
		limiter: newLimiter(5, time.Hour), // 5 messages per IP per hour
		store:   &store{path: cfg.dataFile},
		notify:  smtpNotifier(cfg),
	}
	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	go func() {
		log.Printf("listening on :%s", cfg.port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
