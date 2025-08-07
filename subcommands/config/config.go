/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"

	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"github.com/PlakarKorp/kloset/snapshot/importer"
	"github.com/PlakarKorp/kloset/storage"
	"github.com/PlakarKorp/plakar/appcontext"
	"github.com/PlakarKorp/plakar/subcommands"
	"github.com/PlakarKorp/plakar/utils"
	"go.yaml.in/yaml/v3"
	"gopkg.in/ini.v1"
)

func init() {
	subcommands.Register(func() subcommands.Subcommand { return &ConfigStoreCmd{} },
		subcommands.BeforeRepositoryOpen, "store")
	subcommands.Register(func() subcommands.Subcommand { return &ConfigSourceCmd{} },
		subcommands.BeforeRepositoryOpen, "source")
	subcommands.Register(func() subcommands.Subcommand { return &ConfigDestinationCmd{} },
		subcommands.BeforeRepositoryOpen, "destination")
}

func normalizeName(name string) string {
	return strings.TrimPrefix(name, "@")
}

func normalizeLocation(location string) string {
	return strings.TrimPrefix(location, "location=")
}

func configure(ctx *appcontext.AppContext, cmd string, args []string) error {
	subcmd := "show"
	if len(args) > 0 {
		subcmd = args[0]
		args = args[1:]
	}

	err := dispatchSubcommand(ctx, cmd, subcmd, args)
	if err != nil {
		return err
	}
	return nil
}

func MarshalINISections(sectionName string, kv map[string]string, w io.Writer) error {
	cfg := ini.Empty()

	section := cfg.Section(sectionName)
	for key, value := range kv {
		section.Key(key).SetValue(value)
	}
	_, err := cfg.WriteTo(w)
	return err
}

func dispatchSubcommand(ctx *appcontext.AppContext, cmd string, subcmd string, args []string) error {
	var cfgMap map[string]map[string]string
	var hasFunc func(string) bool
	switch cmd {
	case "store":
		cfgMap = ctx.Config.Repositories
		hasFunc = ctx.Config.HasRepository
	case "destination":
		cfgMap = ctx.Config.Destinations
		hasFunc = ctx.Config.HasDestination
	case "source":
		cfgMap = ctx.Config.Sources
		hasFunc = ctx.Config.HasSource
	default:
		return fmt.Errorf("unknown cmd %q", cmd)
	}

	switch subcmd {
	case "add":
		p := flag.NewFlagSet("add", flag.ExitOnError)
		p.Usage = func() {
			fmt.Fprintf(ctx.Stdout, "Usage: plakar %s %s <name> <location> [<key>=<value>...]\n", cmd, p.Name())
			p.PrintDefaults()
		}
		p.Parse(args)

		if len(args) < 2 {
			return fmt.Errorf("Usage: plakar %s %s <name> <location> [<key>=<value>...]", cmd, p.Name())
		}

		name, location := normalizeName(args[0]), normalizeLocation(args[1])

		if hasFunc(name) {
			return fmt.Errorf("%s %q already exists", cmd, name)
		}
		cfgMap[name] = make(map[string]string)
		cfgMap[name]["location"] = location
		for _, kv := range args[2:] {
			key, val, found := strings.Cut(kv, "=")
			if !found || key == "" {
				return fmt.Errorf("Usage: plakar %s %s <name> <location> [<key>=<value>...]", cmd, p.Name())
			}
			cfgMap[name][key] = val
		}
		return utils.SaveConfig(ctx.ConfigDir, ctx.Config)

	case "check":
		if len(args) != 1 {
			return fmt.Errorf("usage: plakar %s check <name>", cmd)
		}
		name := normalizeName(args[0])
		if !hasFunc(name) {
			return fmt.Errorf("%s %q does not exists", cmd, name)
		}

		switch cmd {
		case "store":
			store, err := storage.New(ctx.GetInner(), cfgMap[name])
			if err != nil {
				return err
			}
			store.Close(ctx)

		case "source":
			cfg, ok := ctx.Config.GetSource(name)
			if !ok {
				return fmt.Errorf("failed to retreive configuration for source %q", name)
			}
			imp, err := importer.NewImporter(ctx.GetInner(), ctx.ImporterOpts(), cfg)
			if err != nil {
				return err
			}
			imp.Close(ctx)

		case "destination":
			cfg, ok := ctx.Config.GetDestination(name)
			if !ok {
				return fmt.Errorf("failed to retreive configuration for destination %q", name)
			}
			exp, err := exporter.NewExporter(ctx.GetInner(), cfg)
			if err != nil {
				return err
			}
			exp.Close(ctx)
		}

		return nil

	case "import":
		var opt_rclone bool
		var opt_config string
		var opt_overwrite bool
		flags := flag.NewFlagSet("import", flag.ExitOnError)
		flags.BoolVar(&opt_rclone, "rclone", false, "import using rclone")
		flags.StringVar(&opt_config, "config", "", "import from a file")
		flags.BoolVar(&opt_overwrite, "overwrite", false, "overwrite existing configurations")
		flags.Parse(args)

		var rd io.Reader = ctx.Stdin
		if opt_config != "" {
			if strings.HasPrefix(opt_config, "http://") || strings.HasPrefix(opt_config, "https://") {
				resp, err := http.Get(opt_config)
				if err != nil {
					return fmt.Errorf("failed to fetch config from %q: %w", opt_config, err)
				}
				defer resp.Body.Close()
				rd = resp.Body
			} else {
				f, err := os.Open(opt_config)
				if err != nil {
					return fmt.Errorf("failed to open file %q: %w", opt_config, err)
				}
				defer f.Close()
				rd = f
			}
		}

		thirdParty := ""
		if opt_rclone {
			thirdParty = "rclone"
		}

		newConfMap, err := utils.GetConf(rd, thirdParty)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if len(newConfMap) == 0 {
			return fmt.Errorf("no valid %ss found in config", cmd)
		}

		if flags.NArg() == 0 {
			for name, section := range newConfMap {
				if hasFunc(name) && !opt_overwrite {
					fmt.Fprintf(ctx.Stderr, "%s %q already exists, skipping\n", cmd, name)
					continue
				}
				cfgMap[name] = make(map[string]string)
				maps.Copy(cfgMap[name], section)
			}
		} else {
			for _, requestedName := range flags.Args() {
				if hasFunc(requestedName) && !opt_overwrite {
					fmt.Fprintf(ctx.Stderr, "%s %q already exists, skipping\n", cmd, requestedName)
					continue
				}
				if section, ok := newConfMap[requestedName]; !ok {
					fmt.Fprintf(ctx.Stderr, "%s %q does not exist in config", cmd, requestedName)
					continue
				} else {
					name := normalizeName(requestedName)
					cfgMap[name] = make(map[string]string)
					maps.Copy(cfgMap[name], section)
				}
			}
		}
		return utils.SaveConfig(ctx.ConfigDir, ctx.Config)

	case "ping":
		return fmt.Errorf("the ping subcomand is not yet implemented in this version of plakar")

	case "rm":
		p := flag.NewFlagSet("rm", flag.ExitOnError)
		p.Usage = func() {
			fmt.Fprintf(ctx.Stdout, "Usage: plakar %s %s <name>\n", cmd, p.Name())
			p.PrintDefaults()
		}
		p.Parse(args)

		if len(args) != 1 {
			return fmt.Errorf("Usage: plakar %s %s <name>", cmd, p.Name())
		}

		name := normalizeName(args[0])
		if !hasFunc(name) {
			return fmt.Errorf("%s %q does not exist", cmd, name)
		}
		delete(cfgMap, name)
		return utils.SaveConfig(ctx.ConfigDir, ctx.Config)

	case "set":
		p := flag.NewFlagSet("set", flag.ExitOnError)
		p.Usage = func() {
			fmt.Fprintf(ctx.Stdout, "Usage: plakar %s %s <name> <key>=<value>...\n", cmd, p.Name())
			p.PrintDefaults()
		}
		p.Parse(args)

		if len(args) < 2 {
			return fmt.Errorf("Usage: plakar %s %s <name> <key>=<value>...", cmd, p.Name())
		}
		name := normalizeName(args[0])
		if !hasFunc(name) {
			return fmt.Errorf("%s %q does not exists", cmd, name)
		}
		for _, kv := range args[1:] {
			key, val, found := strings.Cut(kv, "=")
			if !found || key == "" {
				return fmt.Errorf("usage: plakar %s set <name> [<key>=<value>, ...]", cmd)
			}
			cfgMap[name][key] = val
		}
		return utils.SaveConfig(ctx.ConfigDir, ctx.Config)

	case "show":
		var opt_json bool
		var opt_ini bool
		var opt_yaml bool
		p := flag.NewFlagSet("show", flag.ExitOnError)
		p.Usage = func() {
			fmt.Fprintf(ctx.Stdout, "Usage: plakar %s %s [<name>...]\n", cmd, p.Name())
			p.PrintDefaults()
		}

		p.BoolVar(&opt_json, "json", false, "output in JSON format")
		p.BoolVar(&opt_ini, "ini", false, "output in INI format")
		p.BoolVar(&opt_yaml, "yaml", false, "output in YAML format (default)")
		p.Parse(args)

		names := make([]string, 0)
		if len(args) == 0 {
			for name := range cfgMap {
				names = append(names, name)
			}
		} else {
			names = p.Args()
		}
		for _, name := range names {
			name = normalizeName(name)
			if !hasFunc(name) {
				fmt.Fprintf(ctx.Stderr, "%s %q does not exist\n", cmd, name)
				continue
			}

			var err error
			if opt_json {
				err = json.NewEncoder(ctx.Stdout).Encode(map[string]map[string]string{name: cfgMap[name]})
			} else if opt_ini {
				err = MarshalINISections(name, cfgMap[name], ctx.Stdout)
			} else {
				err = yaml.NewEncoder(ctx.Stdout).Encode(map[string]map[string]string{name: cfgMap[name]})
			}
			if err != nil {
				return fmt.Errorf("failed to encode store %q: %w", name, err)
			}
		}
		return nil

	case "unset":
		p := flag.NewFlagSet("unset", flag.ExitOnError)
		p.Usage = func() {
			fmt.Fprintf(ctx.Stdout, "Usage: plakar %s %s <name> <key>...\n", cmd, p.Name())
			p.PrintDefaults()
		}
		p.Parse(args)

		if len(args) < 2 {
			return fmt.Errorf("Usage: plakar %s %s <name> <key>...", cmd, p.Name())
		}
		name := normalizeName(args[0])
		if !hasFunc(name) {
			return fmt.Errorf("%s %q does not exists", cmd, name)
		}
		for _, key := range args[1:] {
			if key == "location" {
				return fmt.Errorf("cannot unset location")
			}
			delete(cfgMap[name], key)
		}
		return utils.SaveConfig(ctx.ConfigDir, ctx.Config)

	default:
		return fmt.Errorf("usage: plakar %s [add|check|import|ping|rm|set|show|unset]", cmd)
	}
}
