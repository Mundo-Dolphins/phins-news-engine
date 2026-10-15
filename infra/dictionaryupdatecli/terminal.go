package dictionaryupdatecli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

type Terminal struct {
	in  *bufio.Scanner
	out io.Writer
}

func New(in io.Reader, out io.Writer) *Terminal {
	return &Terminal{in: bufio.NewScanner(in), out: out}
}

func (t *Terminal) Next(_ context.Context, p dictionaryupdate.Proposal, state dictionaryupdate.ReviewState) (dictionaryupdate.Action, *dictionaryupdate.Proposal, error) {
	for {
		fmt.Fprintf(t.out, "\n[%d/%d] %s %s\n", state.Index+1, state.Total, p.Kind, p.CanonicalKey)
		if p.Detail != "" {
			fmt.Fprintf(t.out, "  %s\n", p.Detail)
		}
		fmt.Fprint(t.out, "[a]ccept [r]eject [e]dit [s]kip [aa]ccept-all-of-type [ra]reject-all-of-type [vs]iew-source [vc]urrent-entry [vf]conflicts [b]ack [q]uit-and-save [x]abort [h]elp\n> ")

		if !t.in.Scan() {
			return dictionaryupdate.ActionAbort, nil, fmt.Errorf("dictionaryupdatecli: input closed")
		}
		switch strings.TrimSpace(t.in.Text()) {
		case "a":
			return dictionaryupdate.ActionAccept, nil, nil
		case "r":
			return dictionaryupdate.ActionReject, nil, nil
		case "s":
			return dictionaryupdate.ActionSkip, nil, nil
		case "aa":
			return dictionaryupdate.ActionAcceptAllOfType, nil, nil
		case "ra":
			return dictionaryupdate.ActionRejectAllOfType, nil, nil
		case "b":
			return dictionaryupdate.ActionBack, nil, nil
		case "q":
			return dictionaryupdate.ActionQuitAndSave, nil, nil
		case "x":
			return dictionaryupdate.ActionAbort, nil, nil
		case "vs":
			t.renderDetail("source", p.Observed)
		case "vc":
			t.renderDetail("current entry", p.CurrentEntry)
		case "vf":
			fmt.Fprintf(t.out, "  conflicts with: %v\n", p.ConflictWith)
		case "h":
			fmt.Fprintln(t.out, "  a=accept r=reject e=edit s=skip aa=accept-all-of-type ra=reject-all-of-type vs=view-source vc=view-current-entry vf=view-conflicts b=back q=quit-and-save x=abort h=help")
		case "e":
			edited, err := t.edit(p)
			if err != nil {
				fmt.Fprintf(t.out, "  edit cancelled: %v\n", err)
				continue
			}
			return dictionaryupdate.ActionEdit, &edited, nil
		default:
			fmt.Fprintln(t.out, "  unrecognized action, type 'h' for help")
		}
	}
}

func (t *Terminal) renderDetail(label string, v any) {
	fmt.Fprintf(t.out, "  %s: %+v\n", label, v)
}

func (t *Terminal) edit(p dictionaryupdate.Proposal) (dictionaryupdate.Proposal, error) {
	fmt.Fprintf(t.out, "  new category (blank to keep %q): ", p.Category)
	if !t.in.Scan() {
		return dictionaryupdate.Proposal{}, fmt.Errorf("input closed")
	}
	if v := strings.TrimSpace(t.in.Text()); v != "" {
		p.Category = v
	}

	fmt.Fprint(t.out, "  note to append (blank for none): ")
	if !t.in.Scan() {
		return dictionaryupdate.Proposal{}, fmt.Errorf("input closed")
	}
	if v := strings.TrimSpace(t.in.Text()); v != "" {
		if p.Detail == "" {
			p.Detail = v
		} else {
			p.Detail = p.Detail + "; " + v
		}
	}
	return p, nil
}
