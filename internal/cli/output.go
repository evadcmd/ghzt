package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// A record is one item for --json, keyed by gh's field names.
type record map[string]any

// jsonFlag is --json. Unknown fields are a flag error, listing the ones the
// command has, as in gh.
type jsonFlag struct {
	fields    []string
	available []string
}

func (f *jsonFlag) String() string { return strings.Join(f.fields, ",") }

func (f *jsonFlag) Set(s string) error {
	for name := range strings.SplitSeq(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(f.available, name) {
			return fmt.Errorf("unknown JSON field: %q\n%s", name, f.availableText())
		}
		if !slices.Contains(f.fields, name) {
			f.fields = append(f.fields, name)
		}
	}
	if len(f.fields) == 0 {
		return errors.New(f.availableText())
	}
	return nil
}

func (*jsonFlag) Type() string { return "fields" }

func (f *jsonFlag) availableText() string {
	return "Available fields:\n  " + strings.Join(f.available, "\n  ")
}

// enabled reports whether --json was given.
func (f *jsonFlag) enabled() bool { return len(f.fields) > 0 }

// wants reports whether --json asked for field, for fields that take another
// request to fill in.
func (f *jsonFlag) wants(field string) bool { return slices.Contains(f.fields, field) }

// addJSONFlag adds --json with the given fields, sorted. A bare --json is
// answered with the field list, as gh does, instead of pflag's "needs an
// argument".
func addJSONFlag(cmd *cobra.Command, available ...string) *jsonFlag {
	f := &jsonFlag{available: slices.Sorted(slices.Values(available))}
	cmd.Flags().Var(f, "json", "Output JSON with the specified fields")
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		if strings.HasPrefix(err.Error(), "flag needs an argument: --json") {
			err = errors.New("specify one or more comma-separated fields for --json:\n  " +
				strings.Join(f.available, "\n  "))
		}
		return &flagError{err: err, cmdPath: c.CommandPath()}
	})
	return f
}

// pick returns the fields of r that --json asked for.
func (f *jsonFlag) pick(r record) record {
	out := make(record, len(f.fields))
	for _, name := range f.fields {
		out[name] = r[name]
	}
	return out
}

// write prints v as compact JSON: an agent reads it, not a terminal.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// writeRecord prints one record's --json fields.
func (f *jsonFlag) writeRecord(w io.Writer, r record) error {
	return writeJSON(w, f.pick(r))
}

// writeRecords prints the --json fields of a list, as an array even when it
// is empty.
func (f *jsonFlag) writeRecords(w io.Writer, rs []record) error {
	out := make([]record, len(rs))
	for i, r := range rs {
		out[i] = f.pick(r)
	}
	return writeJSON(w, out)
}

// maxBodyLen is how much of a body or comment the text output shows. The
// rest is cut with a marker saying how much is left, so a huge body can't
// flood the caller's context and a cut can't go unnoticed; --json has it all.
const maxBodyLen = 20000

// truncateText cuts s to n characters, adding a marker with how many are left.
func truncateText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return fmt.Sprintf("%s\n… truncated, %d more characters (use --json for all of it)", string(r[:n]), len(r)-n)
}

// writeFields prints "key:\tvalue" lines, gh's text output for a single
// item when stdout isn't a terminal.
func writeFields(w io.Writer, kv ...string) error {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + ":\t" + oneLine(kv[i+1]) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeRows prints tab-separated rows, gh's text output for lists when stdout
// isn't a terminal.
func writeRows(w io.Writer, rows [][]string) error {
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(oneLine(cell))
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// oneLine keeps a value on its line, so that it can't add fields or rows.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
}

// writeListTruncated tells the caller, on stderr so --json stays parseable,
// that a list stopped at the limit with more items left.
func writeListTruncated(w io.Writer, shown, total int) {
	if total > shown {
		_, _ = fmt.Fprintf(w, "… truncated, %d more; raise --limit to see them\n", total-shown)
	}
}

// writeMoreLeft is writeListTruncated for lists whose total isn't known.
func writeMoreLeft(w io.Writer, more bool) {
	if more {
		_, _ = fmt.Fprintln(w, "… truncated, more left; raise --limit to see them")
	}
}

// writeNoResults tells the caller, on stderr so stdout stays gh's, that text
// output came out empty. gh prints nothing then when stdout isn't a terminal,
// which an agent can't tell apart from a command that printed nothing at all.
func writeNoResults(w io.Writer, n int, what string) {
	if n == 0 {
		_, _ = fmt.Fprintf(w, "no %s found\n", what)
	}
}

// checkEnum returns an error unless value is one of allowed.
func checkEnum(flag, value string, allowed ...string) error {
	if !slices.Contains(allowed, value) {
		return fmt.Errorf("invalid argument %q for --%s: valid values are {%s}", value, flag, strings.Join(allowed, "|"))
	}
	return nil
}

// checkLimit returns an error unless --limit is positive.
func checkLimit(limit int) error {
	if limit < 1 {
		return fmt.Errorf("invalid limit: %d", limit)
	}
	return nil
}

// login is gh's author object.
func login(u *apiUser) any {
	if u == nil {
		return nil
	}
	return record{"login": u.Login}
}

// loginText is u's login for text output, "ghost" for a deleted account as on
// github.com.
func loginText(u *apiUser) string {
	if u == nil {
		return "ghost"
	}
	return u.Login
}

// nullable maps an absent timestamp to null.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func labelRecords(ls []apiLabel) []record {
	out := make([]record, len(ls))
	for i, l := range ls {
		out[i] = record{"name": l.Name, "color": l.Color, "description": l.Description}
	}
	return out
}

func labelNames(ls []apiLabel) string {
	names := make([]string, len(ls))
	for i, l := range ls {
		names[i] = l.Name
	}
	return strings.Join(names, ", ")
}
