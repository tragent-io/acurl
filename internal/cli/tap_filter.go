package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/tragent-io/acurl/internal/tap"
)

type tapProcessSelector struct {
	name string
	pid  int
}

type tapProcessFilter []tapProcessSelector

func parseTapProcessFilter(values []string) (tapProcessFilter, error) {
	var filter tapProcessFilter
	for _, value := range values {
		name, pidText, hasPID := strings.Cut(value, ":")
		invalid := name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(pidText, ":") ||
			strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
		pid := 0
		if hasPID {
			var err error
			pid, err = strconv.Atoi(pidText)
			invalid = invalid || err != nil || pid <= 0 || strings.ContainsFunc(pidText, func(r rune) bool { return r < '0' || r > '9' })
		}
		if invalid {
			return nil, fmt.Errorf("invalid --process %q: want a process name or name:positive-pid", value)
		}
		filter = append(filter, tapProcessSelector{name: name, pid: pid})
	}
	return filter, nil
}

func (f tapProcessFilter) matches(x *tap.Exchange) bool {
	if len(f) == 0 {
		return true
	}
	if x.Process == "" || x.PID <= 0 {
		return false
	}
	for _, selector := range f {
		if strings.EqualFold(selector.name, x.Process) && (selector.pid == 0 || selector.pid == x.PID) {
			return true
		}
	}
	return false
}
