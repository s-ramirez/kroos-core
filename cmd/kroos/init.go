package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/config"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"golang.org/x/term"
)

//go:embed templates/AGENTS.md
var tplAgents string

//go:embed templates/IDENTITY.md
var tplIdentity string

//go:embed templates/USER.md
var tplUser string

//go:embed templates/MEMORY.md
var tplMemory string

// runInit bootstraps $KROOS_HOME (templates, dirs) and runs the setup wizard.
// Idempotent: existing files are kept, and the wizard starts from the current
// config.json, so re-running only changes what you change.
func runInit(args []string) error {
	home := config.Home()
	out := os.Stdout
	fmt.Fprintf(out, "kroos instance: %s (set KROOS_HOME to change)\n\n", home)
	if err := scaffoldHome(out, home); err != nil {
		return err
	}
	if err := loadDotenv(config.EnvPath()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading .env: %v\n", err)
	}
	w, err := newWizard(os.Stdin, out, config.GlobalPath())
	if err != nil {
		return err
	}
	if err := w.run(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nDone. Run `kroos` to start; re-run `kroos init` any time to change settings.")
	return nil
}

func scaffoldHome(out io.Writer, home string) error {
	for _, d := range []string{home, filepath.Join(home, "workspace"), filepath.Join(home, "memory"), filepath.Join(home, "skills")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	fmt.Fprintln(out, "Templates:")
	writeTemplateFile(out, filepath.Join(home, "AGENTS.md"), tplAgents)
	writeTemplateFile(out, filepath.Join(home, "IDENTITY.md"), tplIdentity)
	writeTemplateFile(out, filepath.Join(home, "USER.md"), tplUser)
	writeTemplateFile(out, filepath.Join(home, "memory", "MEMORY.md"), tplMemory)
	return nil
}

func writeTemplateFile(out io.Writer, path, content string) {
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(out, "  keep  %s\n", path)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintf(out, "  error %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(out, "  wrote %s\n", path)
}

// errQuit ends the wizard (EOF / "q").
var errQuit = errors.New("quit")

type wizard struct {
	in       *bufio.Reader
	out      io.Writer
	path     string
	cfg      map[string]string
	authPath string
	// readSecret reads a line without echo (nil = plain readLine, e.g. piped input).
	readSecret func() (string, error)
	// login and listModels are swappable for tests.
	login      func(ctx context.Context, path string, w io.Writer) error
	listModels func(ctx context.Context, p *llm.Providers, provider string) ([]string, error)
}

func newWizard(in io.Reader, out io.Writer, path string) (*wizard, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	w := &wizard{
		in: bufio.NewReader(in), out: out, path: path, cfg: cfg,
		authPath: llm.DefaultAuthPath(),
		login:    llm.ChatGPTLogin,
		listModels: func(ctx context.Context, p *llm.Providers, provider string) ([]string, error) {
			return p.ListModels(ctx, provider)
		},
	}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		w.readSecret = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(out)
			if err != nil {
				return "", errQuit
			}
			return strings.TrimSpace(string(b)), nil
		}
	}
	return w, nil
}

type menuItem struct {
	label  string
	status func() string
	run    func() error
}

func (w *wizard) run() error {
	err := w.menu("Setup", []menuItem{
		{"Providers", w.providersStatus, w.providersMenu},
		{"Channels", w.channelsStatus, w.channelsMenu},
		{"Heartbeat", w.heartbeatStatus, w.heartbeatSetup},
		{"Extras", w.extrasStatus, w.extrasSetup},
	}, "Done")
	if errors.Is(err, errQuit) {
		return nil
	}
	return err
}

// menu loops a numbered selection until the exit item is chosen.
func (w *wizard) menu(title string, items []menuItem, exit string) error {
	for {
		fmt.Fprintf(w.out, "\n== %s ==\n", title)
		for i, it := range items {
			status := ""
			if it.status != nil {
				status = it.status()
			}
			fmt.Fprintf(w.out, "  %d. %-18s %s\n", i+1, it.label, status)
		}
		fmt.Fprintf(w.out, "  %d. %s\n", len(items)+1, exit)
		n, err := w.pick(len(items)+1, len(items)+1)
		if err != nil {
			return err
		}
		if n == len(items)+1 {
			return nil
		}
		if err := items[n-1].run(); err != nil {
			return err
		}
	}
}

// pick reads a number in [1,max]; Enter = def.
func (w *wizard) pick(max, def int) (int, error) {
	for {
		fmt.Fprintf(w.out, "> [%d]: ", def)
		line, err := w.readLine()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		if line == "q" {
			return 0, errQuit
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= max {
			return n, nil
		}
		fmt.Fprintf(w.out, "  enter 1-%d\n", max)
	}
}

// choose picks one of options (current value preselected); returns the option.
func (w *wizard) choose(label string, options []string, current string) (string, error) {
	fmt.Fprintf(w.out, "  %s:\n", label)
	def := 1
	for i, o := range options {
		mark := " "
		if o == current {
			mark, def = "*", i+1
		}
		fmt.Fprintf(w.out, "   %s%2d. %s\n", mark, i+1, o)
	}
	n, err := w.pick(len(options), def)
	if err != nil {
		return "", err
	}
	return options[n-1], nil
}

func (w *wizard) confirm(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(w.out, "  %s [%s]: ", label, hint)
	line, err := w.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func (w *wizard) readLine() (string, error) {
	line, err := w.in.ReadString('\n')
	if err != nil && line == "" {
		return "", errQuit
	}
	return strings.TrimSpace(line), nil
}

// get returns the configured value for key (config.json, else env, else default).
func (w *wizard) get(key string) string {
	if v, ok := w.cfg[key]; ok {
		return v
	}
	k, _ := config.Lookup(key)
	if v := os.Getenv(k.Env); v != "" {
		return v
	}
	return k.Default
}

func (w *wizard) set(key, val string) error {
	k, _ := config.Lookup(key)
	if val == "" || val == k.Default {
		if _, ok := w.cfg[key]; !ok {
			return nil
		}
		delete(w.cfg, key)
	} else if w.cfg[key] == val {
		return nil
	} else {
		w.cfg[key] = val
	}
	if err := config.Save(w.path, w.cfg); err != nil {
		return err
	}
	if v, ok := os.LookupEnv(k.Env); ok && v != val {
		fmt.Fprintf(w.out, "  note: %s is also set in the environment/.env, which takes precedence\n", k.Env)
	}
	return nil
}

// field prompts for key, showing the current value. Enter keeps, "-" clears.
func (w *wizard) field(key, label string) error {
	k, ok := config.Lookup(key)
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	cur := w.get(key)
	shown := cur
	if k.Secret && cur != "" {
		shown = config.Mask(cur)
	}
	if label == "" {
		label = k.Desc
	}
	if shown != "" {
		fmt.Fprintf(w.out, "  %s [%s]: ", label, shown)
	} else {
		fmt.Fprintf(w.out, "  %s: ", label)
	}
	read := w.readLine
	if k.Secret && w.readSecret != nil {
		read = w.readSecret
	}
	line, err := read()
	if err != nil {
		return err
	}
	switch line {
	case "":
		return nil
	case "-":
		return w.set(key, "")
	}
	return w.set(key, line)
}

func (w *wizard) fields(keys ...string) error {
	for _, k := range keys {
		if err := w.field(k, ""); err != nil {
			return err
		}
	}
	return nil
}

func (w *wizard) providers() *llm.Providers {
	return &llm.Providers{
		Default:  w.get("provider"),
		OMLXURL:  w.get("omlx.url"),
		OMLXKey:  w.get("omlx.api_key"),
		AuthPath: w.authPath,
		Models: map[string]string{
			llm.ProviderChatGPT: w.get("chatgpt.model"),
			llm.ProviderOMLX:    w.get("omlx.model"),
		},
	}
}

// ---- providers ----

func (w *wizard) chatgptStatus() string {
	if _, err := llm.LoadChatGPTAuth(w.authPath); err != nil {
		return "not logged in"
	}
	return "logged in · " + w.get("chatgpt.model")
}

func (w *wizard) omlxStatus() string {
	if w.get("omlx.url") == "" {
		return "not configured"
	}
	m := w.get("omlx.model")
	if m == "" {
		m = "auto model"
	}
	return w.get("omlx.url") + " · " + m
}

func (w *wizard) providersStatus() string {
	return fmt.Sprintf("default %s · chatgpt: %s · omlx: %s", w.get("provider"), w.chatgptStatus(), w.omlxStatus())
}

func (w *wizard) providersMenu() error {
	return w.menu("Providers", []menuItem{
		{"ChatGPT", w.chatgptStatus, w.chatgptSetup},
		{"oMLX", w.omlxStatus, w.omlxSetup},
		{"Default & fallback", func() string {
			fb := "fallback on"
			if w.get("fallback") == "false" {
				fb = "fallback off"
			}
			return w.get("provider") + " · " + fb
		}, w.defaultProviderSetup},
	}, "Back")
}

func (w *wizard) chatgptSetup() error {
	if _, err := llm.LoadChatGPTAuth(w.authPath); err != nil {
		ok, err := w.confirm("Log in to ChatGPT now?", true)
		if err != nil || !ok {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err = w.login(ctx, w.authPath, w.out)
		stop()
		if err != nil {
			fmt.Fprintf(w.out, "  login failed: %v\n", err)
			return nil
		}
		fmt.Fprintln(w.out, "  logged in")
	} else {
		relog, err := w.confirm("Logged in. Log in again (switch account)?", false)
		if err != nil {
			return err
		}
		if relog {
			if err := llm.ChatGPTLogout(w.authPath); err != nil {
				return err
			}
			return w.chatgptSetup()
		}
	}
	return w.modelSetup(llm.ProviderChatGPT, "chatgpt.model")
}

func (w *wizard) omlxSetup() error {
	if err := w.fields("omlx.url", "omlx.api_key"); err != nil {
		return err
	}
	if w.get("omlx.url") == "" {
		return nil
	}
	return w.modelSetup(llm.ProviderOMLX, "omlx.model")
}

// modelSetup tests the provider by listing its models and lets you pick the default.
func (w *wizard) modelSetup(provider, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	models, err := w.listModels(ctx, w.providers(), provider)
	cancel()
	if err != nil {
		fmt.Fprintf(w.out, "  could not reach %s: %v\n", provider, err)
		return w.field(key, "Default model (free text)")
	}
	fmt.Fprintf(w.out, "  connected · %d models\n", len(models))
	if len(models) == 0 {
		return w.field(key, "Default model (free text)")
	}
	options := models
	if provider == llm.ProviderOMLX {
		options = append([]string{"(auto: first served)"}, models...)
	}
	cur := w.get(key)
	if cur == "" && provider == llm.ProviderOMLX {
		cur = options[0]
	}
	choice, err := w.choose("Default model", options, cur)
	if err != nil {
		return err
	}
	if strings.HasPrefix(choice, "(auto") {
		choice = ""
	}
	return w.set(key, choice)
}

func (w *wizard) defaultProviderSetup() error {
	p, err := w.choose("Default provider", []string{llm.ProviderChatGPT, llm.ProviderOMLX}, w.get("provider"))
	if err != nil {
		return err
	}
	if err := w.set("provider", p); err != nil {
		return err
	}
	fb, err := w.confirm("Fall back to the other provider when one fails?", w.get("fallback") != "false")
	if err != nil {
		return err
	}
	return w.set("fallback", strconv.FormatBool(fb))
}

// ---- channels ----

type channelSpec struct {
	name, label string
	enabledKey  string
	credKeys    []string // all must be set for the channel to run
	keys        []string // prompted in order
}

var channelSpecs = []channelSpec{
	{"discord", "Discord", "discord.enabled", []string{"discord.token"},
		[]string{"discord.token", "discord.owner_ids", "discord.guild_tag", "discord.home_channel", "discord.allow_all_users"}},
	{"telegram", "Telegram", "telegram.enabled", []string{"telegram.token"},
		[]string{"telegram.token", "telegram.owner_ids", "telegram.group_tag", "telegram.home_channel", "telegram.home_channel_thread_id", "telegram.allow_all_users"}},
	{"imessage", "iMessage (BlueBubbles)", "imessage.enabled", []string{"imessage.server_url", "imessage.password"},
		[]string{"imessage.server_url", "imessage.password", "imessage.owner_handles", "imessage.group_tag", "imessage.webhook_host", "imessage.webhook_port", "imessage.home_channel"}},
}

func (w *wizard) channelState(c channelSpec) string {
	for _, k := range c.credKeys {
		if w.get(k) == "" {
			return "not configured"
		}
	}
	if w.get(c.enabledKey) == "false" {
		return "disabled"
	}
	return "enabled"
}

func (w *wizard) channelsStatus() string {
	var parts []string
	for _, c := range channelSpecs {
		parts = append(parts, c.name+": "+w.channelState(c))
	}
	return strings.Join(parts, " · ")
}

func (w *wizard) channelsMenu() error {
	var items []menuItem
	for _, c := range channelSpecs {
		items = append(items, menuItem{c.label, func() string { return w.channelState(c) }, func() error { return w.channelSetup(c) }})
	}
	return w.menu("Channels", items, "Back")
}

func (w *wizard) channelSetup(c channelSpec) error {
	enabled, err := w.confirm("Enable "+c.label+"?", w.channelState(c) != "disabled")
	if err != nil {
		return err
	}
	if !enabled {
		return w.set(c.enabledKey, "false") // keep credentials for later
	}
	if err := w.set(c.enabledKey, ""); err != nil {
		return err
	}
	fmt.Fprintln(w.out, "  Enter keeps the current value, - clears it.")
	if err := w.fields(c.keys...); err != nil {
		return err
	}
	if st := w.channelState(c); st != "enabled" {
		fmt.Fprintf(w.out, "  %s is %s: set %s to run it\n", c.label, st, strings.Join(c.credKeys, " + "))
	}
	return nil
}

// ---- heartbeat ----

func (w *wizard) heartbeatStatus() string {
	iv := w.get("heartbeat.interval")
	if iv == "" {
		return "off"
	}
	return "every " + iv + " → " + w.get("heartbeat.target")
}

func (w *wizard) heartbeatSetup() error {
	on, err := w.confirm("Enable heartbeat (periodic background check-in)?", w.get("heartbeat.interval") != "")
	if err != nil {
		return err
	}
	if !on {
		return w.set("heartbeat.interval", "")
	}
	if w.get("heartbeat.interval") == "" {
		if err := w.set("heartbeat.interval", "30m"); err != nil {
			return err
		}
	}
	if err := w.field("heartbeat.interval", "Interval (Go duration)"); err != nil {
		return err
	}
	target, err := w.choose("Deliver replies to", []string{"cli", "discord", "telegram", "imessage", "none"}, w.get("heartbeat.target"))
	if err != nil {
		return err
	}
	if err := w.set("heartbeat.target", target); err != nil {
		return err
	}
	if target != "cli" && target != "none" {
		if err := w.field("heartbeat.to", "Recipient (blank = channel's home channel)"); err != nil {
			return err
		}
	}
	return w.fields("heartbeat.active_hours_start", "heartbeat.active_hours_end", "heartbeat.timezone")
}

// ---- extras ----

func (w *wizard) extrasStatus() string {
	var parts []string
	if p := w.get("webui.port"); p != "" {
		parts = append(parts, "web UI :"+p)
	}
	if w.get("web_search.brave_api_key") != "" {
		parts = append(parts, "web search")
	}
	if w.get("log_dir") != "" {
		parts = append(parts, "logging")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

func (w *wizard) extrasSetup() error {
	return w.fields("webui.port", "web_search.brave_api_key", "log_dir")
}
