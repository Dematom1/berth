package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	configpkg "github.com/Dematom1/berth/internal/config"
	outputpkg "github.com/Dematom1/berth/internal/output"
)

func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func LookupTarget(config configpkg.Config, name string) (configpkg.Target, error) {
	m, ok := config.Machines[name]
	if !ok {
		return m, fmt.Errorf("unknown SSH machine %q; define machines.%s in BERTH_CONFIG", name, name)
	}
	return m, m.Validate()
}

func Run(ctx context.Context, version string, config configpkg.Config, name string, args []string, out io.Writer, jsonOutput outputpkg.Mode) int {
	m, err := LookupTarget(config, name)
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 2, err.Error(), "Run berth "+args[0]+" --help; configure the SSH machine before retrying")
	}
	mismatch, _ := json.Marshal(agentFrame{Type: "error", Error: "agent version mismatch; no child started (expected " + version + ")"})
	// Probe first so older berth versions without the agent command also get a
	// clear refusal rather than being mistaken for a broken protocol.
	script := "actual=$(" + ShellQuote(m.Agent) + " --version) || exit 1; if [ \"$actual\" != " + ShellQuote(version) + " ]; then printf '%s\\n' " + ShellQuote(string(mismatch)) + "; exit 0; fi; cd " + ShellQuote(m.Workdir) + " && exec " + ShellQuote(m.Agent) + " agent"
	cmd := sshCommand(ctx, m, script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 1, err.Error(), "Retry berth "+args[0]+" --machine "+name)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 1, err.Error(), "Retry berth "+args[0]+" --machine "+name)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return outputpkg.Error(out, jsonOutput, 1, "cannot start SSH; no child started", "Install SSH and retry berth "+args[0]+" --machine "+name)
	}
	writer := &frameWriter{enc: json.NewEncoder(stdin)}
	transportError := func(err error) int {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
		fmt.Fprintln(os.Stderr, "berth: SSH transport/protocol failed; remote result unknown (code 76):", err)
		return 76
	}
	if err := writer.send(agentFrame{Type: "request", Version: version, Args: args, Output: jsonOutput.Effective(out), Machine: name}); err != nil {
		return transportError(err)
	}
	scanner := frameScanner(stdout)
	first, err := readFrame(scanner)
	if err != nil {
		return transportError(err)
	}
	if first.Type == "error" {
		stdin.Close()
		cmd.Wait()
		return outputpkg.Error(out, jsonOutput, 1, first.Error, "Install the matching binary with berth machine install "+name+" --file <binary>")
	}
	if first.Type != "ready" || first.Version != version {
		return transportError(errors.New("invalid agent handshake"))
	}
	// SSH stdin is exclusively the owner-liveness channel. It stays open even
	// when the invoking shell has no stdin; remote jobs are noninteractive.
	for {
		f, err := readFrame(scanner)
		if err != nil {
			return transportError(err)
		}
		switch f.Type {
		case "output":
			if _, err := out.Write(f.Data); err != nil {
				return transportError(err)
			}
		case "result":
			if f.Code < 0 || f.Code > 255 {
				return transportError(errors.New("invalid result code"))
			}
			stdin.Close()
			// Receiving the authoritative result is sufficient, even if the transport
			// fails during teardown. No SSH exit status may overwrite a child's code.
			cmd.Wait()
			return f.Code
		default:
			return transportError(errors.New("unexpected agent frame"))
		}
	}
}

func sshCommand(ctx context.Context, m configpkg.Target, script string) *exec.Cmd {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
	if m.Port != 0 {
		args = append(args, "-p", strconv.Itoa(m.Port))
	}
	if m.IdentityFile != "" {
		args = append(args, "-i", m.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "-l", m.User, "--", m.Host, script)
	return exec.CommandContext(ctx, "ssh", args...)
}
