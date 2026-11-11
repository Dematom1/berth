package remote

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	configpkg "github.com/Dematom1/dibs/internal/config"
)

func Install(ctx context.Context, version string, m configpkg.Target, binary *os.File) error {
	info, err := binary.Stat()
	if err != nil {
		return err
	}

	// Stage on the target filesystem; reject incomplete uploads and wrong version/architecture before
	// atomically replacing the configured path. Existing running agents survive.
	script := "set -eu; umask 077; mkdir -p " + ShellQuote(filepath.Dir(m.Agent)) + "; tmp=$(mktemp " + ShellQuote(m.Agent+".XXXXXXXX") + "); trap 'rm -f -- \"$tmp\"' EXIT HUP INT TERM; cat > \"$tmp\"; [ \"$(wc -c < \"$tmp\")\" -eq " + strconv.FormatInt(info.Size(), 10) + " ] || { echo 'dibs: incomplete binary upload; existing binary retained' >&2; exit 1; }; chmod 700 \"$tmp\"; actual=$(\"$tmp\" --version); if [ \"$actual\" != " + ShellQuote(version) + " ]; then echo 'dibs: agent version mismatch; existing binary retained' >&2; exit 1; fi; mv -f -- \"$tmp\" " + ShellQuote(m.Agent)
	cmd := sshCommand(ctx, m, script)
	cmd.Stdin, cmd.Stderr = binary, os.Stderr
	return cmd.Run()
}
