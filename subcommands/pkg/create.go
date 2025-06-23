/*
 * Copyright (c) 2025 Matthieu Masson <matthieu.masson@plakar.io>
 * Copyright (c) 2025 Omar Polo <omar.polo@plakar.io>
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

package pkg

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/PlakarKorp/kloset/hashing"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/resources"
	"github.com/PlakarKorp/kloset/snapshot"
	"github.com/PlakarKorp/kloset/storage"
	"github.com/PlakarKorp/kloset/versioning"
	"github.com/PlakarKorp/plakar/appcontext"
	"github.com/PlakarKorp/plakar/plugins"
	"github.com/PlakarKorp/plakar/subcommands"
	"gopkg.in/yaml.v3"
)

func init() {
	subcommands.Register(func() subcommands.Subcommand { return &PkgCreate{} },
		subcommands.BeforeRepositoryOpen,
		"pkg", "create")
}

type PkgCreate struct {
	subcommands.SubcommandBase
	Out      string
	Args     []string
	Manifest plugins.Manifest
}

func (cmd *PkgCreate) Parse(ctx *appcontext.AppContext, args []string) error {
	flags := flag.NewFlagSet("pkg create", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [-out plugin] manifest.yaml file ...",
			flags.Name())
		fmt.Fprintf(flags.Output(), "\nOPTIONS:\n")
		flag.PrintDefaults()
	}

	flags.StringVar(&cmd.Out, "out", "", "Plugin file to create")
	flags.Parse(args)

	if flags.NArg() < 2 {
		return fmt.Errorf("not enough arguments")
	}

	cmd.Args = flags.Args()
	fp, err := os.Open(cmd.Args[0])
	if err != nil {
		return fmt.Errorf("can't open %s: %w", cmd.Args[0], err)
	}
	defer fp.Close()

	if err := yaml.NewDecoder(fp).Decode(&cmd.Manifest); err != nil {
		return fmt.Errorf("failed to parse the manifest %s: %w", cmd.Args[0], err)
	}

	if cmd.Out == "" {
		p := fmt.Sprintf("%s-v%s.ptar", cmd.Manifest.Name, cmd.Manifest.Version)
		cmd.Out = filepath.Join(ctx.CWD, p)
	}

	return nil
}

func (cmd *PkgCreate) Execute(ctx *appcontext.AppContext, _ *repository.Repository) (int, error) {
	storageConfiguration := storage.NewConfiguration()
	storageConfiguration.Encryption = nil
	storageConfiguration.Packfile.MaxSize = math.MaxUint64
	hasher := hashing.GetHasher(storage.DEFAULT_HASHING_ALGORITHM)

	serializedConfig, err := storageConfiguration.ToBytes()
	if err != nil {
		return 1, fmt.Errorf("failed to serialize configuration: %w", err)
	}

	rd, err := storage.Serialize(hasher, resources.RT_CONFIG, versioning.GetCurrentVersion(resources.RT_CONFIG), bytes.NewReader(serializedConfig))
	if err != nil {
		return 1, fmt.Errorf("failed to wrap configuration: %w", err)
	}
	wrappedConfig, err := io.ReadAll(rd)
	if err != nil {
		return 1, fmt.Errorf("failed to read wrapped configuration: %w", err)
	}

	st, err := storage.Create(ctx.GetInner(), map[string]string{
		"location": "ptar:" + cmd.Out,
	}, wrappedConfig)
	if err != nil {
		return 1, fmt.Errorf("failed to create the storage: %w", err)
	}

	repo, err := repository.New(ctx.GetInner(), nil, st, wrappedConfig)
	if err != nil {
		return 1, fmt.Errorf("failed to create ptar: %w", err)
	}

	identifier := objects.RandomMAC()
	scanCache, err := repo.AppContext().GetCache().Scan(identifier)
	if err != nil {
		return 1, fmt.Errorf("failed to get the scan cache: %w", err)
	}

	repoWriter := repo.NewRepositoryWriter(scanCache, identifier, repository.PtarType)
	imp := &pkgerImporter{
		files: cmd.Args,
	}

	snap, err := snapshot.CreateWithRepositoryWriter(repoWriter)
	if err != nil {
		return 1, fmt.Errorf("failed to create snapshot: %w", err)
	}

	backupOptions := &snapshot.BackupOptions{
		MaxConcurrency: 1,
		NoCheckpoint:   true,
		NoCommit:       true,
	}

	err = snap.Backup(imp, backupOptions)
	if err != nil {
		return 1, fmt.Errorf("failed to populate the snapshot: %w", err)
	}

	// We are done with everything we can now stop the backup routines.
	repoWriter.PackerManager.Wait()
	err = repoWriter.CommitTransaction(identifier)
	if err != nil {
		return 1, fmt.Errorf("failed to commit transaction: %w", err)
	}

	if err := st.Close(); err != nil {
		return 1, fmt.Errorf("failed to close the storage: %w", err)
	}

	return 0, nil
}
