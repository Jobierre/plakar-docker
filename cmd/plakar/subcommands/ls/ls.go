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

package ls

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os/user"
	"path"
	"time"

	"github.com/PlakarKorp/plakar/appcontext"
	"github.com/PlakarKorp/plakar/cmd/plakar/subcommands"
	"github.com/PlakarKorp/plakar/cmd/plakar/utils"
	"github.com/PlakarKorp/plakar/objects"
	"github.com/PlakarKorp/plakar/repository"
	"github.com/PlakarKorp/plakar/snapshot"
	"github.com/PlakarKorp/plakar/snapshot/vfs"
	"github.com/dustin/go-humanize"
)

func init() {
	subcommands.Register("ls", parse_cmd_ls)
}

func parse_cmd_ls(ctx *appcontext.AppContext, repo *repository.Repository, args []string) (subcommands.Subcommand, error) {
	var opt_recursive bool

	var opt_name string
	var opt_category string
	var opt_environment string
	var opt_perimeter string
	var opt_job string
	var opt_tag string
	var opt_uuid bool
	var opt_before string
	var opt_since string

	flags := flag.NewFlagSet("ls", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [OPTIONS] [SNAPSHOT[:PATH]]\n", flags.Name())
		fmt.Fprintf(flags.Output(), "\nOPTIONS:\n")
		flags.PrintDefaults()
	}

	flags.BoolVar(&opt_uuid, "uuid", false, "display uuid instead of short ID")
	flags.StringVar(&opt_name, "name", "", "filter by name")
	flags.StringVar(&opt_category, "category", "", "filter by category")
	flags.StringVar(&opt_environment, "environment", "", "filter by environment")
	flags.StringVar(&opt_perimeter, "perimeter", "", "filter by perimeter")
	flags.StringVar(&opt_job, "job", "", "filter by job")
	flags.StringVar(&opt_tag, "tag", "", "filter by tag")
	flags.StringVar(&opt_before, "before", "", "filter by date")
	flags.StringVar(&opt_since, "since", "", "filter by date")
	flags.BoolVar(&opt_recursive, "recursive", false, "recursive listing")
	flags.Parse(args)

	if flags.NArg() > 1 {
		return nil, fmt.Errorf("too many arguments")
	}

	var err error

	var beforeDate time.Time
	if opt_before != "" {
		beforeDate, err = utils.ParseTimeFlag(opt_before)
		if err != nil {
			return nil, fmt.Errorf("invalid date format: %s", opt_before)
		}
	}

	var sinceDate time.Time
	if opt_since != "" {
		sinceDate, err = utils.ParseTimeFlag(opt_since)
		if err != nil {
			return nil, fmt.Errorf("invalid date format: %s", opt_since)
		}
	}

	return &Ls{
		RepositoryLocation: repo.Location(),
		RepositorySecret:   ctx.GetSecret(),

		OptBefore: beforeDate,
		OptSince:  sinceDate,

		OptName:        opt_name,
		OptCategory:    opt_category,
		OptEnvironment: opt_environment,
		OptPerimeter:   opt_perimeter,
		OptJob:         opt_job,
		OptTag:         opt_tag,

		Recursive:   opt_recursive,
		DisplayUUID: opt_uuid,
		Path:        flags.Arg(0),
	}, nil
}

type Ls struct {
	RepositoryLocation string
	RepositorySecret   []byte

	OptBefore time.Time
	OptSince  time.Time

	OptName        string
	OptCategory    string
	OptEnvironment string
	OptPerimeter   string
	OptJob         string
	OptTag         string

	Recursive   bool
	DisplayUUID bool
	Path        string
}

func (cmd *Ls) Name() string {
	return "ls"
}

func (cmd *Ls) Execute(ctx *appcontext.AppContext, repo *repository.Repository) (int, error) {
	if cmd.Path == "" {
		if err := cmd.list_snapshots(ctx, repo); err != nil {
			return 1, err
		}
		return 0, nil
	}

	if err := cmd.list_snapshot(ctx, repo, cmd.Path, cmd.Recursive); err != nil {
		return 1, err
	}
	return 0, nil
}

func (cmd *Ls) list_snapshots(ctx *appcontext.AppContext, repo *repository.Repository) error {
	locateOptions := utils.NewDefaultLocateOptions()
	locateOptions.MaxConcurrency = ctx.MaxConcurrency
	locateOptions.SortOrder = utils.LocateSortOrderDescending

	locateOptions.Before = cmd.OptBefore
	locateOptions.Since = cmd.OptSince

	if cmd.OptName != "" {
		locateOptions.Name = cmd.OptName
	}
	if cmd.OptCategory != "" {
		locateOptions.Category = cmd.OptCategory
	}
	if cmd.OptEnvironment != "" {
		locateOptions.Environment = cmd.OptEnvironment
	}
	if cmd.OptPerimeter != "" {
		locateOptions.Perimeter = cmd.OptPerimeter
	}
	if cmd.OptJob != "" {
		locateOptions.Job = cmd.OptJob
	}
	if cmd.OptTag != "" {
		locateOptions.Tag = cmd.OptTag
	}

	snapshotIDs, err := utils.LocateSnapshotIDs(repo, locateOptions)
	if err != nil {
		return fmt.Errorf("ls: could not fetch snapshots list: %w", err)
	}

	for _, snapshotID := range snapshotIDs {
		snap, err := snapshot.Load(repo, snapshotID)
		if err != nil {
			return fmt.Errorf("ls: could not fetch snapshot: %w", err)
		}

		if !cmd.DisplayUUID {
			fmt.Fprintf(ctx.Stdout, "%s %10s%10s%10s %s\n",
				snap.Header.Timestamp.UTC().Format(time.RFC3339),
				hex.EncodeToString(snap.Header.GetIndexShortID()),
				humanize.Bytes(snap.Header.GetSource(0).Summary.Directory.Size+snap.Header.GetSource(0).Summary.Below.Size),
				snap.Header.Duration.Round(time.Second),
				snap.Header.GetSource(0).Importer.Directory)
		} else {
			indexID := snap.Header.GetIndexID()
			fmt.Fprintf(ctx.Stdout, "%s %3s%10s%10s %s\n",
				snap.Header.Timestamp.UTC().Format(time.RFC3339),
				hex.EncodeToString(indexID[:]),
				humanize.Bytes(snap.Header.GetSource(0).Summary.Directory.Size+snap.Header.GetSource(0).Summary.Below.Size),
				snap.Header.Duration.Round(time.Second),
				snap.Header.GetSource(0).Importer.Directory)
		}

		snap.Close()
	}
	return nil
}

func (cmd *Ls) list_snapshot(ctx *appcontext.AppContext, repo *repository.Repository, snapshotPath string, recursive bool) error {
	prefix, pathname := utils.ParseSnapshotID(snapshotPath)
	if pathname == "" {
		pathname = "/"
	} else {
		pathname = path.Clean(pathname)
	}

	snap, err := utils.OpenSnapshotByPrefix(repo, prefix)
	if err != nil {
		return fmt.Errorf("ls: could not fetch snapshot: %w", err)
	}
	defer snap.Close()

	pvfs, err := snap.Filesystem()
	if err != nil {
		return err
	}

	return pvfs.WalkDir(pathname, func(path string, d *vfs.Entry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == pathname {
			return nil
		}

		sb, err := d.Info()
		if err != nil {
			return err
		}

		var username, groupname string
		if finfo, ok := sb.Sys().(objects.FileInfo); ok {
			pwUserLookup, err := user.LookupId(fmt.Sprintf("%d", finfo.Uid()))
			username = fmt.Sprintf("%d", finfo.Uid())
			if err == nil {
				username = pwUserLookup.Username
			}

			grGroupLookup, err := user.LookupGroupId(fmt.Sprintf("%d", finfo.Gid()))
			groupname = fmt.Sprintf("%d", finfo.Gid())
			if err == nil {
				groupname = grGroupLookup.Name
			}
		}

		entryname := path
		if !recursive {
			entryname = d.Name()
		}

		fmt.Fprintf(ctx.Stdout, "%s %s % 8s % 8s % 8s %s\n",
			sb.ModTime().UTC().Format(time.RFC3339),
			sb.Mode(),
			username,
			groupname,
			humanize.Bytes(uint64(sb.Size())),
			entryname)

		if !recursive && pathname != path && sb.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}
