package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	defaultWatchURL        = "https://in.njuko.com/marathondelille2026"
	defaultIntervalMinutes = 10
	defaultLastHashFile    = "last_hash.txt"
	defaultSubscribersFile = "subscribers.json"
	commandPrefix          = "!"
)

type Config struct {
	DiscordBotToken      string `json:"discord_bot_token"`
	DefaultUserID        string `json:"default_user_id"`
	WatchURL             string `json:"watch_url"`
	CheckIntervalMinutes int    `json:"check_interval_minutes"`
	LastHashFile         string `json:"last_hash_file"`
	SubscribersFile      string `json:"subscribers_file"`
}

type SubscriberStore struct {
	path string
	mu   sync.RWMutex
	ids  map[string]struct{}
}

type Watcher struct {
	cfg        Config
	store      *SubscriberStore
	discord    *discordgo.Session
	httpClient *http.Client
	checkMu    sync.Mutex
}

type CheckResult struct {
	Changed      bool
	FirstRun     bool
	ProbablyOpen bool
	OldHash      string
	NewHash      string
	PageSnippet  string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("configuration invalide: %v", err)
	}

	store, err := NewSubscriberStore(cfg.SubscribersFile)
	if err != nil {
		log.Fatalf("chargement des abonnes impossible: %v", err)
	}
	if cfg.DefaultUserID != "" {
		if added, err := store.Add(cfg.DefaultUserID); err != nil {
			log.Fatalf("impossible d'ajouter default_user_id: %v", err)
		} else if added {
			log.Printf("abonne par defaut ajoute: %s", cfg.DefaultUserID)
		}
	}

	dg, err := discordgo.New("Bot " + cfg.DiscordBotToken)
	if err != nil {
		log.Fatalf("creation session Discord impossible: %v", err)
	}
	dg.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	watcher := &Watcher{
		cfg:     cfg,
		store:   store,
		discord: dg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	dg.AddHandler(watcher.onReady)
	dg.AddHandler(watcher.onMessageCreate)

	if err := dg.Open(); err != nil {
		log.Fatalf("connexion Discord impossible: %v", err)
	}
	defer dg.Close()

	log.Printf("watcher demarre: url=%s intervalle=%dmin", cfg.WatchURL, cfg.CheckIntervalMinutes)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go watcher.runScheduler(ctx)

	<-ctx.Done()
	log.Println("arret demande, fermeture propre...")
}

func loadConfig(path string) (Config, error) {
	var cfg Config

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("%s introuvable, copiez config.example.json vers config.json", path)
		}
		return cfg, err
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return cfg, err
	}

	cfg.DiscordBotToken = strings.TrimSpace(cfg.DiscordBotToken)
	cfg.DefaultUserID = strings.TrimSpace(cfg.DefaultUserID)
	cfg.WatchURL = strings.TrimSpace(cfg.WatchURL)
	cfg.LastHashFile = strings.TrimSpace(cfg.LastHashFile)
	cfg.SubscribersFile = strings.TrimSpace(cfg.SubscribersFile)

	if cfg.DiscordBotToken == "" || cfg.DiscordBotToken == "VOTRE_TOKEN_DISCORD_ICI" {
		return cfg, errors.New("discord_bot_token est obligatoire")
	}
	if cfg.WatchURL == "" {
		cfg.WatchURL = defaultWatchURL
	}
	if cfg.CheckIntervalMinutes <= 0 {
		cfg.CheckIntervalMinutes = defaultIntervalMinutes
	}
	if cfg.LastHashFile == "" {
		cfg.LastHashFile = defaultLastHashFile
	}
	if cfg.SubscribersFile == "" {
		cfg.SubscribersFile = defaultSubscribersFile
	}

	return cfg, nil
}

func NewSubscriberStore(path string) (*SubscriberStore, error) {
	store := &SubscriberStore{
		path: path,
		ids:  make(map[string]struct{}),
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	defer f.Close()

	var ids []string
	if err := json.NewDecoder(f).Decode(&ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			store.ids[id] = struct{}{}
		}
	}

	return store, nil
}

func (s *SubscriberStore) Add(userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, errors.New("user id vide")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.ids[userID]; exists {
		return false, nil
	}
	s.ids[userID] = struct{}{}
	return true, s.saveLocked()
}

func (s *SubscriberStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	return ids
}

func (s *SubscriberStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ids)
}

func (s *SubscriberStore) saveLocked() error {
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}

	if err := os.MkdirAll(filepath.Dir(normalizePathForDir(s.path)), 0o755); err != nil {
		return err
	}

	tmpPath := s.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ids); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.path)
}

func normalizePathForDir(path string) string {
	if filepath.Dir(path) == "." {
		return filepath.Join(".", path)
	}
	return path
}

func (w *Watcher) onReady(_ *discordgo.Session, ready *discordgo.Ready) {
	log.Printf("connecte a Discord en tant que %s#%s", ready.User.Username, ready.User.Discriminator)
}

func (w *Watcher) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return
	}

	content := strings.TrimSpace(m.Content)
	lowerContent := strings.ToLower(content)

	switch lowerContent {
	case commandPrefix + "subscribe":
		w.handleSubscribe(s, m)
	case commandPrefix + "check":
		w.handleManualCheck(s, m)
	case commandPrefix + "status":
		w.handleStatus(s, m)
	case commandPrefix + "help":
		w.reply(s, m.ChannelID, "Commandes disponibles: `!subscribe`, `!check`, `!status`.")
	}
}

func (w *Watcher) handleSubscribe(s *discordgo.Session, m *discordgo.MessageCreate) {
	added, err := w.store.Add(m.Author.ID)
	if err != nil {
		log.Printf("erreur abonnement %s: %v", m.Author.ID, err)
		w.reply(s, m.ChannelID, "Impossible d'enregistrer ton abonnement pour le moment.")
		return
	}

	if added {
		log.Printf("nouvel abonne: %s (%s)", m.Author.ID, m.Author.Username)
	} else {
		log.Printf("abonne deja present: %s (%s)", m.Author.ID, m.Author.Username)
	}

	dmText := "Tu es bien abonne aux alertes du Marathon de Lille 2026. Je t'enverrai un DM si la page change ou si les inscriptions semblent ouvertes."
	if err := w.sendDM(m.Author.ID, dmText); err != nil {
		log.Printf("DM de confirmation impossible pour %s: %v", m.Author.ID, err)
		w.reply(s, m.ChannelID, "Abonnement enregistre, mais je n'arrive pas a t'envoyer de DM. Verifie tes parametres de confidentialite Discord.")
		return
	}

	if added {
		w.reply(s, m.ChannelID, "Abonnement enregistre. Je t'ai envoye un DM de confirmation.")
	} else {
		w.reply(s, m.ChannelID, "Tu etais deja abonne. Je t'ai renvoye un DM de confirmation.")
	}
}

func (w *Watcher) handleManualCheck(s *discordgo.Session, m *discordgo.MessageCreate) {
	w.reply(s, m.ChannelID, "Verification manuelle lancee...")

	result, err := w.checkOnce()
	if err != nil {
		log.Printf("verification manuelle demandee par %s en erreur: %v", m.Author.ID, err)
		w.reply(s, m.ChannelID, "La verification a echoue: `"+escapeInlineCode(err.Error())+"`")
		return
	}

	if result.FirstRun {
		w.reply(s, m.ChannelID, "Premier passage: hash initialise, aucune alerte envoyee.")
		return
	}
	if !result.Changed {
		w.reply(s, m.ChannelID, "Aucun changement detecte. Dernier hash: `"+shortHash(result.NewHash)+"`.")
		return
	}

	message := w.buildNotification(result)
	w.reply(s, m.ChannelID, "Changement detecte. Notification envoyee aux abonnes.")
	w.notifySubscribers(message)
}

func (w *Watcher) handleStatus(s *discordgo.Session, m *discordgo.MessageCreate) {
	hash, err := readLastHash(w.cfg.LastHashFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		w.reply(s, m.ChannelID, "Statut indisponible: `"+escapeInlineCode(err.Error())+"`")
		return
	}
	if hash == "" {
		hash = "pas encore initialise"
	} else {
		hash = shortHash(hash)
	}

	w.reply(
		s,
		m.ChannelID,
		fmt.Sprintf("Watcher actif. URL: <%s> | abonnes: %d | intervalle: %d min | dernier hash: `%s`",
			w.cfg.WatchURL,
			w.store.Count(),
			w.cfg.CheckIntervalMinutes,
			hash,
		),
	)
}

func (w *Watcher) runScheduler(ctx context.Context) {
	if _, err := w.checkOnce(); err != nil {
		log.Printf("verification initiale en erreur: %v", err)
	}

	ticker := time.NewTicker(time.Duration(w.cfg.CheckIntervalMinutes) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := w.checkOnce()
			if err != nil {
				log.Printf("verification planifiee en erreur: %v", err)
				continue
			}
			if result.FirstRun {
				log.Println("hash initialise, aucune notification envoyee")
				continue
			}
			if !result.Changed {
				log.Printf("aucun changement, hash=%s", shortHash(result.NewHash))
				continue
			}

			log.Printf("changement detecte: %s -> %s", shortHash(result.OldHash), shortHash(result.NewHash))
			w.notifySubscribers(w.buildNotification(result))
		}
	}
}

func (w *Watcher) checkOnce() (CheckResult, error) {
	w.checkMu.Lock()
	defer w.checkMu.Unlock()

	body, err := w.fetchPage()
	if err != nil {
		return CheckResult{}, err
	}

	newHash := sha256Hex(body)
	oldHash, err := readLastHash(w.cfg.LastHashFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CheckResult{}, err
	}

	result := CheckResult{
		FirstRun:     oldHash == "",
		Changed:      oldHash != "" && oldHash != newHash,
		ProbablyOpen: registrationsProbablyOpen(string(body)),
		OldHash:      oldHash,
		NewHash:      newHash,
		PageSnippet:  firstCleanSnippet(string(body), 280),
	}

	if result.FirstRun || result.Changed {
		if err := writeLastHash(w.cfg.LastHashFile, newHash); err != nil {
			return CheckResult{}, err
		}
	}

	return result, nil
}

func (w *Watcher) fetchPage() ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, w.cfg.WatchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Surveillance-Marathon-Lille/1.0 (+https://github.com/)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("statut HTTP inattendu: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func (w *Watcher) buildNotification(result CheckResult) string {
	if result.ProbablyOpen {
		return fmt.Sprintf(
			"**Alerte Marathon de Lille 2026**\n\n"+
				"La page a change et les inscriptions semblent probablement ouvertes.\n"+
				"Va verifier maintenant: <%s>\n\n"+
				"Hash: `%s` -> `%s`",
			w.cfg.WatchURL,
			shortHash(result.OldHash),
			shortHash(result.NewHash),
		)
	}

	return fmt.Sprintf(
		"**Surveillance Marathon de Lille 2026**\n\n"+
			"La page a change, mais je ne vois pas encore de signal clair d'ouverture des inscriptions.\n"+
			"A verifier ici: <%s>\n\n"+
			"Hash: `%s` -> `%s`\n"+
			"Extrait detecte: `%s`",
		w.cfg.WatchURL,
		shortHash(result.OldHash),
		shortHash(result.NewHash),
		escapeInlineCode(result.PageSnippet),
	)
}

func (w *Watcher) notifySubscribers(message string) {
	ids := w.store.List()
	if len(ids) == 0 {
		log.Println("aucun abonne, notification non envoyee")
		return
	}

	for _, userID := range ids {
		if err := w.sendDM(userID, message); err != nil {
			log.Printf("notification DM impossible pour %s: %v", userID, err)
			continue
		}
		log.Printf("notification envoyee a %s", userID)
	}
}

func (w *Watcher) sendDM(userID, message string) error {
	channel, err := w.discord.UserChannelCreate(userID)
	if err != nil {
		return err
	}
	_, err = w.discord.ChannelMessageSend(channel.ID, message)
	return err
}

func (w *Watcher) reply(s *discordgo.Session, channelID, message string) {
	if _, err := s.ChannelMessageSend(channelID, message); err != nil {
		log.Printf("reponse Discord impossible dans %s: %v", channelID, err)
	}
}

func registrationsProbablyOpen(page string) bool {
	text := strings.ToLower(page)

	positiveSignals := []string{
		"inscription",
		"inscriptions",
		"s'inscrire",
		"je m'inscris",
		"register",
		"registration",
		"billetterie",
		"dossard",
		"marathon de Lille",
		"semi marathon de Lille",
		"10km de Lille",
		"5km de Lille",
	}
	negativeSignals := []string{
		"prochainement",
		"bientot",
		"bientôt",
		"soon",
		"ferme",
		"fermée",
		"fermees",
		"fermées",
		"complet",
		"sold out",
		"closed",
	}

	hasPositive := false
	for _, signal := range positiveSignals {
		if strings.Contains(text, signal) {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		return false
	}

	for _, signal := range negativeSignals {
		if strings.Contains(text, signal) {
			return false
		}
	}

	return true
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readLastHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeLastHash(path, hash string) error {
	if err := os.MkdirAll(filepath.Dir(normalizePathForDir(path)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hash+"\n"), 0o600)
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func firstCleanSnippet(text string, maxLen int) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "aucun extrait lisible"
	}
	snippet := strings.Join(fields, " ")
	if len(snippet) <= maxLen {
		return snippet
	}
	return snippet[:maxLen] + "..."
}

func escapeInlineCode(text string) string {
	return strings.ReplaceAll(text, "`", "'")
}
