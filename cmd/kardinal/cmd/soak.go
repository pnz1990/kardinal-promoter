// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// soakComparison is a soak threshold in a gate's expression:
// bundle.upstreamSoakMinutes or upstream.<env>.soakMinutes compared with >=
// or > to a number of minutes (3, 3.5). A number that runs on into a name or
// another number (3e2, 3.5.1, 3x) does not match.
var soakComparison = regexp.MustCompile(`(?:^|[^\w.])(bundle\s*\.\s*upstreamSoakMinutes|upstream\s*\.\s*([A-Za-z0-9_]+)\s*\.\s*soakMinutes)\s*(>=|>)\s*(\d+(?:\.\d+)?)(?:$|[^\w.])`)

// maxSoakMinutes is the largest threshold soakOf reads: a year. A larger one
// would overflow a time.Duration in opensIn; such a gate is shown as it is.
const maxSoakMinutes = 366 * 24 * 60

// negated finds a ! right before a comparison (after any opening
// parentheses): such a gate passes while the soak is short, and does not
// open by soaking.
var negated = regexp.MustCompile(`!\s*[(\s]*$`)

// gateSoak is how far a soak gate's version has soaked, computed from what
// the controller records: the Bundle's status.environments[].healthCheckedAt
// of the environments the gate's expression reads (the same field
// status.environments[].soakMinutes counts from). It is for people; the
// gate decides on soakMinutes.
type gateSoak struct {
	// envs are the environments whose soak the expression reads.
	envs []string
	// need is the threshold in minutes; strict when the comparison is >.
	need   float64
	strict bool
	// live is false while an environment it reads is not live yet; elapsed
	// is then 0.
	live    bool
	elapsed time.Duration
	// whole is true when the comparison is the entire expression, so the
	// gate opens when the soak reaches need.
	whole bool
}

// soakOf is gate g's soak for its Bundle b of Pipeline p at now, and false
// when g is not a soak gate or its environments cannot be told.
func soakOf(p *v1alpha1.Pipeline, b *v1alpha1.Bundle, g *v1alpha1.PolicyGate, now time.Time) (gateSoak, bool) {
	if g == nil || b == nil || g.Spec.Approval != nil {
		return gateSoak{}, false
	}
	expr := strings.TrimSpace(celStringLiteral.ReplaceAllString(g.Spec.Expression, `""`))
	all := soakComparison.FindAllStringSubmatchIndex(expr, -1)
	if len(all) != 1 {
		return gateSoak{}, false
	}
	m := all[0]
	if negated.MatchString(expr[:m[2]]) {
		return gateSoak{}, false
	}
	need, err := strconv.ParseFloat(expr[m[8]:m[9]], 64)
	if err != nil || need < 0 || need > maxSoakMinutes {
		return gateSoak{}, false
	}
	s := gateSoak{need: need, strict: expr[m[6]:m[7]] == ">"}
	s.whole = strings.TrimSpace(strings.Trim(strings.TrimSpace(expr[:m[2]]+expr[m[9]:]), "()")) == ""
	if m[4] >= 0 {
		s.envs = []string{expr[m[4]:m[5]]}
	} else {
		if p == nil {
			return gateSoak{}, false
		}
		ups, err := graph.DirectUpstreams(p, b, g.Labels["kardinal.io/environment"])
		if err != nil || len(ups) == 0 {
			return gateSoak{}, false
		}
		s.envs = ups
	}
	byName := make(map[string]v1alpha1.EnvironmentStatus, len(b.Status.Environments))
	for _, e := range b.Status.Environments {
		byName[e.Name] = e
	}
	// With several upstreams the gate reads the least soaked one.
	s.live = true
	for i, env := range s.envs {
		e, ok := byName[env]
		if !ok || e.Phase != "Verified" || e.HealthCheckedAt == nil || e.HealthCheckedAt.IsZero() {
			s.live, s.elapsed = false, 0
			break
		}
		d := max(now.Sub(e.HealthCheckedAt.Time), 0)
		if i == 0 || d < s.elapsed {
			s.elapsed = d
		}
	}
	return s, true
}

// minutes is how many whole minutes of soak open the gate: soakMinutes is
// whole minutes, so >= 3.5 needs 4, > 3 needs 4 and > 3.5 needs 4.
func (s gateSoak) minutes() int64 {
	n := int64(math.Ceil(s.need))
	if s.strict && float64(n) == s.need {
		n++
	}
	return n
}

// opensIn is how long until the soak reaches its threshold, and false
// when that cannot be told (an environment it reads is not live yet).
func (s gateSoak) opensIn() (time.Duration, bool) {
	if !s.live {
		return 0, false
	}
	return max(time.Duration(s.minutes())*time.Minute-s.elapsed, 0), true
}

// now is the soak in words: "soak 0.4 of 3 min in uat", or, before the
// environment is live, "uat not live yet, needs 3 min of soak".
func (s gateSoak) now() string {
	where := strings.Join(s.envs, ", ")
	if !s.live {
		return fmt.Sprintf("%s not live yet, needs %d min of soak", where, s.minutes())
	}
	return fmt.Sprintf("soak %s of %d min in %s", strconv.FormatFloat(s.elapsed.Minutes(), 'f', 1, 64), s.minutes(), where)
}

// opens is when the gate opens by itself, in words: "in about 2m30s", "3 min
// after uat is live", or "" when the soak is only part of the expression.
func (s gateSoak) opens() string {
	if !s.whole {
		return ""
	}
	d, ok := s.opensIn()
	if !ok {
		verb := "is"
		if len(s.envs) > 1 {
			verb = "are"
		}
		return fmt.Sprintf("%d min after %s %s live", s.minutes(), strings.Join(s.envs, " and "), verb)
	}
	if d < time.Second {
		return "at its next check"
	}
	return "in about " + shortDuration(d)
}
