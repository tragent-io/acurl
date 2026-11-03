package tap

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WriteLine writes a one-line summary of x, like tcpdump:
//
//	12:01:03 #4 anthropic POST /v1/messages claude-opus-5-5 200 1.2s 41.0KB > 3.1KB stream
func WriteLine(w io.Writer, x *Exchange) error {
	path := x.URL
	if u, err := url.Parse(x.URL); err == nil {
		path = u.RequestURI()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%d", x.Start.Format("15:04:05"), x.ID)
	if x.Process != "" && x.PID > 0 {
		// QuoteToASCII escapes terminal controls, including Unicode format
		// characters, without allowing a process name to add another line.
		name := strconv.QuoteToASCII(x.Process)
		fmt.Fprintf(&b, " %s:%d", name[1:len(name)-1], x.PID)
	}
	fmt.Fprintf(&b, " %s %s %s", x.Route, x.Method, path)
	if x.Model != "" {
		b.WriteString(" " + x.Model)
	}
	if x.Status != 0 {
		fmt.Fprintf(&b, " %d", x.Status)
	}
	fmt.Fprintf(&b, " %s %s > %s", x.Duration.Round(time.Millisecond), size(x.ReqBody.Size), size(x.RespBody.Size))
	if x.Streaming {
		b.WriteString(" stream")
	}
	if x.Upgrade != "" {
		b.WriteString(" " + strings.ToLower(x.Upgrade) + " (frames not recorded)")
	}
	if x.Canceled {
		b.WriteString(" canceled")
	}
	if x.Err != "" {
		b.WriteString(" error: " + x.Err)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteBodies writes the request and response bodies of x under a header
// line, for `acurl tap --bodies`.
func WriteBodies(w io.Writer, x *Exchange) error {
	for _, part := range []struct {
		name string
		body Body
	}{{"request", x.ReqBody}, {"response", x.RespBody}} {
		if part.body.Size == 0 {
			continue
		}
		note := ""
		if part.body.Truncated {
			note = fmt.Sprintf(" (first %s of %s)", size(int64(len(part.body.Text))), size(part.body.Size))
		}
		if _, err := fmt.Fprintf(w, "  --- %s%s\n%s\n", part.name, note, strings.TrimRight(part.body.Text, "\n")); err != nil {
			return err
		}
	}
	return nil
}

func size(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%dB", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1fKB", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/1000/1000)
	}
}
