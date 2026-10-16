package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/s-ramirez/kroos-core/internal/config"
)

const configUsage = `usage: kroos config <command> [args]

commands:
  show            show resolved config (secrets masked; --reveal to show)
  get <key>       print a resolved value
  set <key> <val> write a value to the config file
  unset <key>     remove a value from the config file
  keys            list all keys with env var, default and description
  path            print config file path
  edit            open config file in $EDITOR

Config file: $KROOS_HOME/config.json (default ~/.kroos/config.json).
`

func runConfig(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	reveal := fs.Bool("reveal", false, "show secret values unmasked")
	fs.Usage = func() { fmt.Fprint(out, configUsage) }
	// Allow flags anywhere: pull them out before positional parsing.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) == 0 {
		fs.Usage()
		return nil
	}

	path := config.GlobalPath()
	cmd, rest := pos[0], pos[1:]

	switch cmd {
	case "path":
		fmt.Fprintln(out, path)
	case "keys":
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tENV\tDEFAULT\tDESCRIPTION")
		for _, k := range config.Keys {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.Name, k.Env, k.Default, k.Desc)
		}
		return tw.Flush()
	case "show":
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, k := range config.Keys {
			v, ok := os.LookupEnv(k.Env)
			if !ok {
				if k.Default == "" {
					continue
				}
				v = k.Default + " (default)"
			} else if k.Secret && !*reveal {
				v = config.Mask(v)
			}
			fmt.Fprintf(tw, "%s\t%s\n", k.Name, v)
		}
		return tw.Flush()
	case "get":
		k, err := keyArg(rest, 1)
		if err != nil {
			return err
		}
		v, ok := os.LookupEnv(k.Env)
		if !ok {
			v = k.Default
		} else if k.Secret && !*reveal {
			v = config.Mask(v)
		}
		fmt.Fprintln(out, v)
	case "set":
		k, err := keyArg(rest, 2)
		if err != nil {
			return err
		}
		m, err := config.Load(path)
		if err != nil {
			return err
		}
		m[k.Name] = rest[1]
		if err := config.Save(path, m); err != nil {
			return err
		}
		fmt.Fprintf(out, "set %s in %s\n", k.Name, path)
	case "unset":
		k, err := keyArg(rest, 1)
		if err != nil {
			return err
		}
		m, err := config.Load(path)
		if err != nil {
			return err
		}
		delete(m, k.Name)
		if err := config.Save(path, m); err != nil {
			return err
		}
		fmt.Fprintf(out, "unset %s in %s\n", k.Name, path)
	case "edit":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := config.Save(path, map[string]string{}); err != nil {
				return err
			}
		}
		c := exec.Command(editor, path)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	default:
		fs.Usage()
		return fmt.Errorf("unknown config command %q", cmd)
	}
	return nil
}

func keyArg(args []string, n int) (config.Key, error) {
	if len(args) != n {
		return config.Key{}, fmt.Errorf("expected %d argument(s), got %d", n, len(args))
	}
	k, ok := config.Lookup(args[0])
	if !ok {
		return config.Key{}, fmt.Errorf("unknown key %q (see `kroos config keys`)", args[0])
	}
	return k, nil
}

// parseRunFlags registers one flag per config key (e.g. --model,
// --heartbeat.interval) plus --set key=value, and exports set values as env
// vars, overriding everything else. Returns the names of keys set via flags.
func parseRunFlags(args []string) (map[string]bool, error) {
	fs := flag.NewFlagSet("kroos", flag.ContinueOnError)
	vals := make(map[string]*string, len(config.Keys))
	for _, k := range config.Keys {
		def := k.Default
		if def != "" {
			def = " (default " + def + ")"
		}
		vals[k.Name] = fs.String(k.Name, "", k.Desc+def+" [$"+k.Env+"]")
	}
	var sets multiFlag
	fs.Var(&sets, "set", "set any config key: --set key=value (repeatable)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: kroos [flags]\n       kroos init\n       kroos config <command>\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		if k, ok := config.Lookup(f.Name); ok {
			os.Setenv(k.Env, *vals[k.Name])
			explicit[k.Name] = true
		}
	})
	for _, kv := range sets {
		name, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--set %q: expected key=value", kv)
		}
		k, found := config.Lookup(name)
		if !found {
			return nil, fmt.Errorf("--set: unknown key %q", name)
		}
		os.Setenv(k.Env, v)
		explicit[k.Name] = true
	}
	return explicit, nil
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	if v == "" {
		return errors.New("empty value")
	}
	*m = append(*m, v)
	return nil
}
