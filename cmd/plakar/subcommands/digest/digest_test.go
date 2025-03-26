package digest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/PlakarKorp/plakar/snapshot"
	_ "github.com/PlakarKorp/plakar/snapshot/exporter/fs"
	ptesting "github.com/PlakarKorp/plakar/testing"
	"github.com/stretchr/testify/require"
)

func init() {
	os.Setenv("TZ", "UTC")
}

func generateSnapshot(t *testing.T, bufOut *bytes.Buffer, bufErr *bytes.Buffer) *snapshot.Snapshot {
	return ptesting.GenerateSnapshot(t, bufOut, bufErr, nil, []ptesting.MockFile{
		ptesting.NewMockDir("subdir"),
		ptesting.NewMockDir("another_subdir"),
		ptesting.NewMockFile("subdir/dummy.txt", 0644, "hello dummy"),
		ptesting.NewMockFile("subdir/foo.txt", 0644, "hello foo"),
		ptesting.NewMockFile("subdir/to_exclude", 0644, "*/subdir/to_exclude\n"),
		ptesting.NewMockFile("another_subdir/bar.txt", 0644, "hello bar"),
	})
}

func TestExecuteCmdDigestDefault(t *testing.T) {
	bufOut := bytes.NewBuffer(nil)
	bufErr := bytes.NewBuffer(nil)

	snap := generateSnapshot(t, bufOut, bufErr)
	defer snap.Close()

	ctx := snap.AppContext()
	ctx.MaxConcurrency = 1

	repo := snap.Repository()
	// override the homedir to avoid having test overwriting existing home configuration
	ctx.HomeDir = repo.Location()
	indexId := snap.Header.GetIndexID()
	args := []string{fmt.Sprintf("%s", hex.EncodeToString(indexId[:]))}

	subcommand, err := parse_cmd_digest(ctx, args)
	require.NoError(t, err)
	require.NotNil(t, subcommand)
	require.Equal(t, "digest", subcommand.(*Digest).Name())

	status, err := subcommand.Execute(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, 0, status)

	// output should look like this
	// SHA256 (/tmp/tmp_to_backup3363028982/another_subdir/bar) = b585d5afa0d0a97a7c217eeb9d9adf08fc63188d4204fc7d537a178224b477e6
	// SHA256 (/tmp/tmp_to_backup3363028982/subdir/dummy.txt) = f4da3ebff9dbd21cfb270054dee6948f96de93f68f525e0bf4067ce2f9e2d639
	// SHA256 (/tmp/tmp_to_backup3363028982/subdir/foo.txt) = 6c8aa524fae27a3607f9c4204567b65d48341b3bcc0e36e9e50856aaaf073d21
	// SHA256 (/tmp/tmp_to_backup3363028982/subdir/to_exclude) = dd7117865f65a87aba1e171b82e073914a2cdffb1b34407dea682f62c3dc72e0

	output := bufOut.String()
	require.Contains(t, output, "dummy.txt")
	lines := strings.Split(strings.Trim(output, "\n"), "\n")
	for _, line := range lines {
		require.Contains(t, line, "SHA256 (")
	}
}

func TestExecuteCmdDigestNoParam(t *testing.T) {
	bufOut := bytes.NewBuffer(nil)
	bufErr := bytes.NewBuffer(nil)

	snap := generateSnapshot(t, bufOut, bufErr)
	defer snap.Close()

	ctx := snap.AppContext()
	ctx.MaxConcurrency = 1

	repo := snap.Repository()
	// override the homedir to avoid having test overwriting existing home configuration
	ctx.HomeDir = repo.Location()
	args := []string{}

	subcommand, err := parse_cmd_digest(ctx, args)
	require.Error(t, err, "at least one parameter is required")
	require.Nil(t, subcommand)
}

func TestExecuteCmdDigestWrongHashing(t *testing.T) {
	bufOut := bytes.NewBuffer(nil)
	bufErr := bytes.NewBuffer(nil)

	snap := generateSnapshot(t, bufOut, bufErr)
	defer snap.Close()

	ctx := snap.AppContext()
	ctx.MaxConcurrency = 1

	repo := snap.Repository()
	// override the homedir to avoid having test overwriting existing home configuration
	ctx.HomeDir = repo.Location()
	indexId := snap.Header.GetIndexID()
	args := []string{"-hashing", "md5", fmt.Sprintf("%s", hex.EncodeToString(indexId[:]))}

	subcommand, err := parse_cmd_digest(ctx, args)
	require.Error(t, err, "at least one parameter is required")
	require.Nil(t, subcommand)
}
