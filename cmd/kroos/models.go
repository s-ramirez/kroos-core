package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/config"
	"github.com/s-ramirez/kroos-core/internal/llm"
)

type modelChoice struct{ Provider, Model string }

func (c modelChoice) String() string { return c.Provider + ":" + c.Model }

// modelCatalog is the numbered model list across providers; failed providers land in errs.
type modelCatalog struct {
	choices []modelChoice
	errs    map[string]error
}

func newProvidersFromEnv() *llm.Providers {
	return &llm.Providers{
		Default: envOr("KROOS_PROVIDER", llm.ProviderChatGPT),
		OMLXURL: envOr("KROOS_OMLX_URL", configDefault("omlx.url")),
		OMLXKey: os.Getenv("KROOS_OMLX_API_KEY"),
	}
}

func configDefault(name string) string {
	if k, ok := config.Lookup(name); ok {
		return k.Default
	}
	return ""
}

func fetchCatalog(ctx context.Context, p *llm.Providers, only string) modelCatalog {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cat := modelCatalog{errs: map[string]error{}}
	for _, name := range p.Names() {
		if only != "" && name != only {
			continue
		}
		models, err := p.ListModels(ctx, name)
		if err != nil {
			cat.errs[name] = err
			continue
		}
		for _, m := range models {
			cat.choices = append(cat.choices, modelChoice{name, m})
		}
	}
	return cat
}

func (cat modelCatalog) write(w io.Writer, current modelChoice) {
	last := ""
	for i, c := range cat.choices {
		if c.Provider != last {
			fmt.Fprintf(w, "%s:\n", c.Provider)
			last = c.Provider
		}
		mark := " "
		if c == current {
			mark = "*"
		}
		fmt.Fprintf(w, " %s%3d. %s\n", mark, i+1, c.Model)
	}
	for name, err := range cat.errs {
		fmt.Fprintf(w, "%s: unavailable (%v)\n", name, err)
	}
}

// resolve maps a selection — list number, "provider:model", or model name —
// to a choice. Unlisted names are accepted for defProvider.
func (cat modelCatalog) resolve(arg, defProvider string) (modelChoice, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return modelChoice{}, errors.New("no model given")
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(cat.choices) {
			return modelChoice{}, fmt.Errorf("no model #%d", n)
		}
		return cat.choices[n-1], nil
	}
	if prov, model, ok := strings.Cut(arg, ":"); ok && (prov == llm.ProviderChatGPT || prov == llm.ProviderOMLX) {
		return modelChoice{prov, model}, nil
	}
	for _, c := range cat.choices {
		if c.Model == arg && c.Provider == defProvider {
			return c, nil
		}
	}
	for _, c := range cat.choices {
		if c.Model == arg {
			return c, nil
		}
	}
	return modelChoice{defProvider, arg}, nil
}

const modelsUsage = `usage:
  kroos models [chatgpt|omlx]             list available models (* = configured)
  kroos models select [--project] [pick]  choose a model and save it to config
                                          pick = list number, model, or provider:model
`

// runModels handles `kroos models ...`.
func runModels(args []string, in io.Reader, out io.Writer) error {
	p := newProvidersFromEnv()
	current := modelChoice{p.Default, envOr("KROOS_MODEL", "gpt-5.4")}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(out, modelsUsage)
		return nil
	}
	if len(args) == 0 || args[0] != "select" {
		only := ""
		if len(args) > 0 {
			only = args[0]
		}
		cat := fetchCatalog(context.Background(), p, only)
		cat.write(out, current)
		return nil
	}

	fs := flag.NewFlagSet("models select", flag.ContinueOnError)
	project := fs.Bool("project", false, "save to project config (.kroos/config.json)")
	fs.SetOutput(out)
	var pos []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	cat := fetchCatalog(context.Background(), p, "")
	pick := strings.Join(pos, " ")
	if pick == "" {
		cat.write(out, current)
		fmt.Fprint(out, "Select model: ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		pick = strings.TrimSpace(line)
	}
	choice, err := cat.resolve(pick, p.Default)
	if err != nil {
		return err
	}
	path := config.GlobalPath()
	if *project {
		path = config.ProjectPath()
	}
	m, err := config.Load(path)
	if err != nil {
		return err
	}
	m["provider"] = choice.Provider
	m["model"] = choice.Model
	if err := config.Save(path, m); err != nil {
		return err
	}
	fmt.Fprintf(out, "Selected %s (saved to %s)\n", choice, path)
	return nil
}

// sessionModelCommand implements the in-session /model command.
func sessionModelCommand(p *llm.Providers, defaults modelChoice) func(context.Context, *channel.Conversation, string) string {
	return func(ctx context.Context, conv *channel.Conversation, args string) string {
		current := defaults
		if conv.Model != "" {
			current = modelChoice{conv.Provider, conv.Model}
			if current.Provider == "" {
				current.Provider = defaults.Provider
			}
		}
		switch args {
		case "":
			var b strings.Builder
			fmt.Fprintf(&b, "Current model: %s\n", current)
			fetchCatalog(ctx, p, "").write(&b, current)
			b.WriteString("Use /model <number|name|provider:model>, or /model default.\n")
			return b.String()
		case "default", "reset":
			conv.Provider, conv.Model = "", ""
			return fmt.Sprintf("Model reset to default (%s).\n", defaults)
		}
		var cat modelCatalog
		if _, err := strconv.Atoi(args); err == nil || !strings.Contains(args, ":") {
			cat = fetchCatalog(ctx, p, "")
		}
		choice, err := cat.resolve(args, current.Provider)
		if err != nil {
			return fmt.Sprintf("error: %v\n", err)
		}
		if _, err := p.New(choice.Provider, choice.Model, time.Second); err != nil {
			return fmt.Sprintf("error: %v\n", err)
		}
		conv.Provider, conv.Model = choice.Provider, choice.Model
		return fmt.Sprintf("Model for this conversation: %s\n", choice)
	}
}
