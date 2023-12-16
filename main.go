package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/steambap/captcha"
)

// Settings contains the theme submission settings.
type Settings struct {
	Address    string
	Title      string
	Text       string
	Entries    int
	UseCaptcha bool
	UseHeader  bool
	StartDate  time.Time
	EndDate    time.Time
}

type Entries map[string]int

var entries Entries
var saveLock sync.Mutex
var settings Settings
var captchaLock sync.Mutex
var funcMap template.FuncMap

var sessionManager *scs.SessionManager
var sessionCaptchas map[string]*captcha.Data

type templateSettings struct {
	Title           string
	Text            string
	Submissions     []string
	SubmissionCount int
	UseHeader       bool
	UseCaptcha      bool
	CaptchaFailed   bool
	IsSubmission    bool
	IsResults       bool
	Results         Entries
	IsStarted       bool
	IsEnded         bool
}

func init() {
	sessionCaptchas = make(map[string]*captcha.Data)
	entries = make(Entries)
	funcMap = template.FuncMap{
		"inc": func(i int) int {
			return i + 1
		},
	}
	settings.Entries = 4
}

func main() {
	if err := loadSettings(); err != nil {
		fmt.Println(err)
		settings.Address = ":8080"
		settings.Entries = 4
		settings.Title = "Game Jam"
		settings.UseCaptcha = true
		settings.StartDate = time.Now().Add(7 * 24 * time.Hour)
		settings.EndDate = time.Now().Add(3 * 7 * 24 * time.Hour)
		if err := saveSettings(); err != nil {
			fmt.Println(err)
		}
	}

	sessionManager = scs.New()
	sessionManager.Lifetime = 24 * time.Hour

	if err := loadEntries(); err != nil {
		fmt.Println(err)
	}

	go func() {
		for {
			// This is lazy and bad, but just clear the captchas after 20 minutes. If this bites anyone, it'll just result in them having to enter a new captcha, so I don't really care.
			<-time.After(time.Minute * 20)
			captchaLock.Lock()
			sessionCaptchas = make(map[string]*captcha.Data)
			captchaLock.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/captcha", handleCaptcha)
	mux.HandleFunc("/results", handleResults)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	if err := http.ListenAndServe(settings.Address, sessionManager.LoadAndSave(mux)); err != nil {
		panic(err)
	}
}

func addCaptcha(token string) error {
	cap, err := captcha.New(250, 100)
	if err != nil {
		return err
	}
	captchaLock.Lock()
	sessionCaptchas[token] = cap
	captchaLock.Unlock()
	return nil
}

func removeCaptcha(token string) {
	captchaLock.Lock()
	delete(sessionCaptchas, token)
	captchaLock.Unlock()
}

func generateCaptcha(w http.ResponseWriter, r *http.Request) {
	if !settings.UseCaptcha {
		return
	}
	if token := sessionManager.Token(r.Context()); token != "" {
		removeCaptcha(token)
	}
	if err := sessionManager.RenewToken(r.Context()); err != nil {
		fmt.Println(err)
		return
	}
	if err := addCaptcha(sessionManager.Token(r.Context())); err != nil {
		fmt.Println(err)
		return
	}
}

func handleResults(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("index.html").Funcs(funcMap).ParseFiles("index.html")
	if err != nil {
		fmt.Println(err)
	}
	if err := tmpl.Execute(w, templateSettings{
		IsResults: true,
		UseHeader: settings.UseHeader,
		Text:      settings.Text,
		Title:     settings.Title,
		Results:   entries,
	}); err != nil {
		fmt.Println(err)
		return
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	var s templateSettings
	s.UseHeader = settings.UseHeader
	s.UseCaptcha = settings.UseCaptcha
	s.Text = settings.Text
	s.Title = settings.Title
	if time.Since(settings.StartDate) >= 0 {
		s.IsStarted = true
	}
	if time.Since(settings.EndDate) >= 0 {
		s.IsEnded = true
	}
	if r.Method == "GET" {
		tmpl, err := template.New("index.html").Funcs(funcMap).ParseFiles("index.html")
		if err != nil {
			fmt.Println(err)
			return
		}
		s.Submissions = make([]string, settings.Entries) // 4 for now...
		if err := tmpl.Execute(w, s); err != nil {
			fmt.Println(err)
		}
		return
	} else if r.Method == "POST" {
		if !s.IsStarted || s.IsEnded {
			return
		}
		if err := r.ParseForm(); err != nil {
			fmt.Println(err)
			return
		}
		for k, v := range r.PostForm {
			if k == "submission[]" {
				s.Submissions = append(s.Submissions, v...)
			} else if k == "captcha" {
				if capt, ok := sessionCaptchas[sessionManager.Token(r.Context())]; !ok || v[0] != capt.Text {
					s.CaptchaFailed = true
				}
			}
		}
		if !settings.UseCaptcha || !s.CaptchaFailed {
			for _, v := range s.Submissions {
				v = strings.TrimSpace(strings.ToLower(v))
				if v == "" {
					continue
				}
				entries[v]++
				s.SubmissionCount++
			}
			saveEntries()
		}
		s.IsSubmission = true
		tmpl, err := template.New("index.html").Funcs(funcMap).ParseFiles("index.html")
		if err != nil {
			fmt.Println(err)
			return
		}
		tmpl.Execute(w, s)
		return
	}
	// 500 or w/e
}

func handleCaptcha(w http.ResponseWriter, r *http.Request) {
	if !settings.UseCaptcha {
		return
	}

	generateCaptcha(w, r)
	if capt, ok := sessionCaptchas[sessionManager.Token(r.Context())]; ok {
		capt.WriteImage(w)
		return
	}
	fmt.Println("missing token for captcha")
	// TODO: Error out
}

func loadSettings() error {
	bytes, err := os.ReadFile("settings.json")
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, &settings)
}

func saveSettings() error {
	bytes, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := os.WriteFile("settings.json", bytes, 0644); err != nil {
		return err
	}
	return nil
}

func loadEntries() error {
	bytes, err := os.ReadFile("entries.json")
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, &entries)
}

func saveEntries() error {
	saveLock.Lock()
	defer saveLock.Unlock()
	bytes, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile("entries.json", bytes, 0644)
}
