package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// SSH config aliases may supply proxying, keys, and host-key policy. Berth never
// disables host-key checking or falls back to interactive authentication.
type SSHMachine struct {
	Host         string `json:"host"`
	User         string `json:"user"`
	Workdir      string `json:"workdir"`
	Agent        string `json:"agent"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

func (m SSHMachine) validate() error {
	if m.Host == "" || strings.HasPrefix(m.Host, "-") || strings.ContainsAny(m.Host, " \t\r\n\x00@") || m.User == "" || strings.HasPrefix(m.User, "-") || strings.ContainsAny(m.User, " \t\r\n\x00@") {
		return errors.New("SSH machine requires a host (or SSH config alias) and user, without whitespace or option prefixes")
	}
	if !filepath.IsAbs(m.Workdir) || !filepath.IsAbs(m.Agent) || strings.ContainsRune(m.Workdir+m.Agent+m.IdentityFile, 0) || m.Port < 0 || m.Port > 65535 {
		return errors.New("SSH machine requires absolute workdir and agent paths, and port 1..65535 when set")
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (m SSHMachine) command(ctx context.Context, script string) *exec.Cmd {
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

func configuredMachine(config JobConfig, name string) (SSHMachine, error) {
	m, ok := config.Machines[name]
	if !ok {
		return m, fmt.Errorf("unknown SSH machine %q; define machines.%s in BERTH_CONFIG", name, name)
	}
	return m, m.validate()
}

// Newline-delimited JSON is private to the SSH pipe, never user-facing. Byte
// slices use JSON base64 so arbitrary child output cannot impersonate a result.
// A result frame distinguishes child exit 255 from an SSH transport failure.
type agentFrame struct {
	Type    string   `json:"type"`
	Version string   `json:"version,omitempty"`
	Args    []string `json:"args,omitempty"`
	Data    []byte   `json:"data,omitempty"`
	Code    int      `json:"code,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func frameScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 1<<20)
	return s
}

func readFrame(s *bufio.Scanner) (agentFrame, error) {
	var f agentFrame
	if !s.Scan() {
		if s.Err() != nil {
			return f, s.Err()
		}
		return f, io.EOF
	}
	dec := json.NewDecoder(strings.NewReader(s.Text()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return f, errors.New("invalid trailing protocol data")
	}
	return f, nil
}

type frameWriter struct{ enc *json.Encoder }

func (w *frameWriter) send(f agentFrame) error { return w.enc.Encode(f) }
func (w *frameWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		size := min(len(p), 32<<10)
		if err := w.send(agentFrame{Type: "output", Data: p[:size]}); err != nil {
			return n, err
		}
		n += size
		p = p[size:]
	}
	return n, nil
}

func runRemote(ctx context.Context, config JobConfig, name string, args []string, out io.Writer, jsonOutput bool) int {
	m, err := configuredMachine(config, name)
	if err != nil {
		return emitError(out, jsonOutput, 2, err.Error(), "Run berth "+args[0]+" --help; configure the SSH machine before retrying")
	}
	mismatch, _ := json.Marshal(agentFrame{Type: "error", Error: "agent version mismatch; no child started (expected " + version + ")"})
	// Probe first so older berth versions without the agent command also get a
	// clear refusal rather than being mistaken for a broken protocol.
	script := "actual=$(" + shellQuote(m.Agent) + " --version) || exit 1; if [ \"$actual\" != " + shellQuote(version) + " ]; then printf '%s\\n' " + shellQuote(string(mismatch)) + "; exit 0; fi; cd " + shellQuote(m.Workdir) + " && exec " + shellQuote(m.Agent) + " agent"
	cmd := m.command(ctx, script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Retry berth "+args[0]+" --machine "+name)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Retry berth "+args[0]+" --machine "+name)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return emitError(out, jsonOutput, 1, "cannot start SSH; no child started", "Install SSH and retry berth "+args[0]+" --machine "+name)
	}
	writer := &frameWriter{enc: json.NewEncoder(stdin)}
	transportError := func(err error) int {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
		fmt.Fprintln(os.Stderr, "berth: SSH transport/protocol failed; remote result unknown (code 76):", err)
		return 76
	}
	if err := writer.send(agentFrame{Type: "request", Version: version, Args: args}); err != nil {
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
		return emitError(out, jsonOutput, 1, first.Error, "Install the matching binary with berth machine install "+name+" --file <binary>")
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

func runAgent(ctx context.Context, args []string, input io.Reader, out io.Writer) int {
	if len(args) == 1 && args[0] == "--help" {
		fmt.Fprint(out, help("agent"))
		return 0
	}
	w := &frameWriter{enc: json.NewEncoder(out)}
	fail := func(message string) int { _ = w.send(agentFrame{Type: "error", Error: message}); return 1 }
	if len(args) != 0 {
		return emitError(out, false, 2, "agent takes no flags; use berth agent --help", help("agent"))
	}
	scanner := frameScanner(input)
	request, err := readFrame(scanner)
	if err != nil || request.Type != "request" {
		return fail("invalid agent request; no child started")
	}
	if request.Version != version {
		return fail(fmt.Sprintf("agent version mismatch: client %q, remote %q; no child started", request.Version, version))
	}
	if len(request.Args) == 0 {
		return fail("missing remote command")
	}
	switch request.Args[0] {
	case "job", "list", "sweep":
	default:
		return fail("unsupported remote command")
	}
	for _, arg := range request.Args[1:] {
		if arg == "--" {
			break
		}
		if strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-") == "machine" {
			return fail("nested remote machines are not supported")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return fail("cannot open remote child stdin")
	}
	defer null.Close()
	if w.send(agentFrame{Type: "ready", Version: version}) != nil {
		return 1
	}
	go func() {
		// No further client frames are valid. EOF is owner death, not child EOF.
		_, _ = readFrame(scanner)
		cancel()
	}()
	var code int
	if request.Args[0] == "job" {
		code = runJob(ctx, request.Args[1:], w, false, null, "session")
	} else {
		code = run(ctx, request.Args, w)
	}
	if w.send(agentFrame{Type: "result", Code: code}) != nil {
		return 1
	}
	return 0
}

func runMachine(ctx context.Context, args []string, out io.Writer, jsonOutput bool) int {
	fs := flag.NewFlagSet("machine install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "trusted binary built for the remote OS/architecture")
	wantHelp := fs.Bool("help", false, "help")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "JSON output")
	if len(args) == 1 && args[0] == "--help" {
		fmt.Fprint(out, help("machine"))
		return 0
	}
	if len(args) < 1 || args[0] != "install" {
		return emitError(out, jsonOutput, 2, "use berth machine install <name> --file <binary>", help("machine"))
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		return emitError(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	if *wantHelp {
		fmt.Fprint(out, help("machine"))
		return 0
	}
	if fs.NArg() != 1 || *file == "" {
		return emitError(out, jsonOutput, 2, "machine name and --file are required", help("machine"))
	}
	config, err := loadJobConfig()
	if err != nil {
		return emitError(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	m, err := configuredMachine(config, fs.Arg(0))
	if err != nil {
		return emitError(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	binary, err := os.Open(*file)
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), help("machine"))
	}
	defer binary.Close()
	info, err := binary.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return emitError(out, jsonOutput, 2, "--file must be a nonempty regular binary file", help("machine"))
	}
	// Stage on the target filesystem; reject incomplete uploads and wrong version/architecture before
	// atomically replacing the configured path. Existing running agents survive.
	script := "set -eu; umask 077; mkdir -p " + shellQuote(filepath.Dir(m.Agent)) + "; tmp=$(mktemp " + shellQuote(m.Agent+".XXXXXXXX") + "); trap 'rm -f -- \"$tmp\"' EXIT HUP INT TERM; cat > \"$tmp\"; [ \"$(wc -c < \"$tmp\")\" -eq " + strconv.FormatInt(info.Size(), 10) + " ] || { echo 'berth: incomplete binary upload; existing binary retained' >&2; exit 1; }; chmod 700 \"$tmp\"; actual=$(\"$tmp\" --version); if [ \"$actual\" != " + shellQuote(version) + " ]; then echo 'berth: agent version mismatch; existing binary retained' >&2; exit 1; fi; mv -f -- \"$tmp\" " + shellQuote(m.Agent)
	cmd := m.command(ctx, script)
	cmd.Stdin, cmd.Stderr = binary, os.Stderr
	if err := cmd.Run(); err != nil {
		return emitError(out, jsonOutput, 1, "remote installation failed; check SSH diagnostics and binary OS/architecture/version", help("machine"))
	}
	emit(out, jsonOutput, map[string]any{"machine": fs.Arg(0), "version": version, "status": "installed", "help": []string{"Run berth ps --machine " + fs.Arg(0)}})
	return 0
}
