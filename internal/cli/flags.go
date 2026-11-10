package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

type labelsFlag map[string]string

func (l labelsFlag) String() string { return "key=value" }

func (l labelsFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return errors.New("--label requires key=value")
	}
	l[k] = v
	return nil
}

// Standard flag parsing, with positional arguments moved after flag values so
// both `extend ID --ttl 1h` and `extend --ttl 1h ID` have the same meaning.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		options = append(options, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return fmt.Errorf("unknown flag %s", a)
		}
		boolean, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && !(isBool && boolean.IsBoolFlag()) {
			i++
			if i == len(args) {
				return fmt.Errorf("flag --%s needs a value", name)
			}
			options = append(options, args[i])
		}
	}
	return fs.Parse(append(options, positional...))
}
