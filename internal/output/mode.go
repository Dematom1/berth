package output

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Dematom1/dibs/internal/compat"
	"golang.org/x/sys/unix"
)

type Mode string

const (
	Auto  Mode = ""
	Human Mode = "human"
	AXI   Mode = "axi"
	JSON  Mode = "json"
)

type Writer struct {
	io.Writer
	Mode        Mode
	Full        bool
	FullCommand string
	Machine     string
}

func (mode Mode) Effective(w io.Writer) Mode {
	if mode != Auto {
		return mode
	}
	if writer, ok := w.(*Writer); ok {
		return writer.Mode
	}
	if compat.Env("AXI") == "1" {
		return AXI
	}
	if file, ok := w.(*os.File); ok {
		if _, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ); err == nil {
			return Human
		}
	}
	return AXI
}

func WithMode(w io.Writer, mode Mode) io.Writer { return &Writer{Writer: w, Mode: mode} }

type outputFlag struct {
	mode   *Mode
	choice Mode
}

func (f outputFlag) String() string { return "false" }

func (f outputFlag) IsBoolFlag() bool { return true }

func (f outputFlag) Set(value string) error {
	if value != "true" {
		return fmt.Errorf("--%s only accepts true", f.choice)
	}
	if *f.mode != Auto && *f.mode != f.choice {
		return fmt.Errorf("--json and --axi are mutually exclusive")
	}
	*f.mode = f.choice
	return nil
}

func Flags(fs *flag.FlagSet, mode *Mode) {
	fs.Var(outputFlag{mode, AXI}, "axi", "agent output (TOON)")
	fs.Var(outputFlag{mode, JSON}, "json", "script output (JSON)")
}

func WriterMachine(w io.Writer) string {
	if writer, ok := w.(*Writer); ok {
		if writer.Machine != "" {
			return writer.Machine
		}
		return WriterMachine(writer.Writer)
	}
	return ""
}

func SetFlag(mode *Mode, choice Mode, value string) error {
	return (outputFlag{mode, choice}).Set(value)
}
