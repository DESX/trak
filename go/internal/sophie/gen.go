// Package sophie writes synthetic Sophie logs (docs/sophie.md) for testing and
// benchmarks: interleaved sessions with stand-in content. Output depends only
// on the Config.
package sophie

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"time"

	trak "github.com/desx/trak/go"
)

// Schema is the Sophie schema from docs/sophie.md.
//
//go:embed sophie.schema
var Schema string

// Config sets what Generate writes.
type Config struct {
	Size     int64 // the target size with the reference policy
	Seed     uint64
	Policy   trak.Policy
	Progress func(Stats) // called every 10,000 appends
}

// Stats describe a generated file.
type Stats struct {
	Bytes     int64
	BodyBytes int64
	Appends   int
	Sessions  int
	Turns     int
	MaxTurns  int
	Defaults  int // changes to defaults after the first values
	Elapsed   time.Duration
}

// bodyShare is the part of a reference-policy file that is bodies, measured on
// generated files. The run stops once the bodies reach it, so every policy
// gets the same content.
const bodyShare = 0.73

// fullSize is the size the session counts are tuned for: at 5 GB the largest
// session reaches over 200,000 turns.
const fullSize = 5e9

var backends = []string{"commercial", "govcloud"}
var levels = []string{"w1", "w2", "w3", "w4"}
var promptKinds = []string{"base", "watchdog", "timeout", "compaction"}
var models = []string{"atlas-2-large", "atlas-2-fast", "atlas-3-preview", "orion-1", "orion-1-mini"}
var efforts = []string{"low", "medium", "high", "max"}
var projects = []string{"billing", "ingest", "webapp", "infra", "search", "mobile", "reports", "auth", "pipeline", "docs"}

type op struct {
	list string
	val  any
}

type session struct {
	i                int
	big              bool
	target           int
	turns, messages  int
	compactions      int
	results, names   int
	backend          string
	nanny            bool
	compactAt, limit int
	wd               [5]int
	used             int
	api              string
	apis             int
	fresh            bool // the next prompt opens an API session
	resume           int  // a compaction to resume from, or -1
	base             int  // the base prompt version it uses
	workdir          string
	last             []byte // the last output, for the next auto prompt
	queue            []op
}

type gen struct {
	cfg      Config
	r        *rand.Rand
	t        *text
	w        *trak.Writer
	now      int64 // ms
	st       Stats
	budget   int64
	scale    float64
	start    time.Time
	sessions []*session
	active   []*session
	big      *session

	backend, model, effort, workdir, nanny, retry string
	compactAt                                     int
	wd                                            [5]int
	prompts                                       [4][][]byte // every version of each prompt
	agents                                        map[string][]byte

	nextLogin   [2]int64
	nextBlock   [2]int64 // when a backend's quota next runs out
	blocked     [2]bool
	openAt      [2]int64
	nextDefault int64
}

// Generate writes a synthetic Sophie file to name.
func Generate(name string, cfg Config) (Stats, error) {
	g := &gen{cfg: cfg, r: rand.New(rand.NewPCG(cfg.Seed, 0x50f1e)), start: time.Now()}
	g.t = newText(g.r)
	g.budget = int64(float64(cfg.Size) * bodyShare)
	g.scale = float64(cfg.Size) / fullSize
	g.now = time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC).UnixMilli()
	os.Remove(name)
	w, err := trak.Create(name, Schema, map[string]any{"version": "sophie 0.9.3"}, time.UnixMilli(g.now), &trak.Options{Policy: cfg.Policy})
	if err != nil {
		return g.st, err
	}
	g.w = w
	defer w.Close()
	if err := g.run(); err != nil {
		return g.st, err
	}
	g.st.Bytes = w.File().Size()
	g.st.Elapsed = time.Since(g.start)
	for _, s := range g.sessions {
		if s.turns > g.st.MaxTurns {
			g.st.MaxTurns = s.turns
		}
	}
	return g.st, w.Close()
}

func (g *gen) append(list string, val any) error {
	a, err := g.w.Append(list, val, time.UnixMilli(g.now))
	if err != nil {
		return fmt.Errorf("append %s: %w", list, err)
	}
	g.st.Appends++
	g.st.BodyBytes += a.Footer - a.Start
	g.now += int64(g.r.ExpFloat64()*5000) + 1
	if g.cfg.Progress != nil && g.st.Appends%10000 == 0 {
		g.st.Bytes = a.End
		g.cfg.Progress(g.st)
	}
	return nil
}

func (g *gen) run() error {
	if err := g.initDefaults(); err != nil {
		return err
	}
	g.nextLogin = [2]int64{g.now, g.now + 90_000}
	g.nextBlock = [2]int64{g.now + g.interval(8*3_600_000), g.now + g.interval(30*3_600_000)}
	g.nextDefault = g.now + g.interval(67*60_000)
	for i := 0; i < 8; i++ {
		if _, err := g.newSession(false); err != nil {
			return err
		}
	}
	var err error
	if g.big, err = g.newSession(true); err != nil {
		return err
	}
	for g.st.BodyBytes < g.budget {
		if err := g.globals(); err != nil {
			return err
		}
		s := g.pick()
		if s == nil {
			// Every session waits on a quota: skip to the first opening.
			next := int64(math.MaxInt64)
			for b := range g.blocked {
				if g.blocked[b] && g.openAt[b] < next {
					next = g.openAt[b]
				}
			}
			g.now = max(g.now, next)
			continue
		}
		if err := g.step(s); err != nil {
			return err
		}
	}
	// Finish every session's current turn, then archive them.
	for _, s := range g.active {
		for len(s.queue) > 0 {
			if err := g.step(s); err != nil {
				return err
			}
		}
		if err := g.append(fmt.Sprintf("sessions.%d.state", s.i), "archived"); err != nil {
			return err
		}
	}
	for b := range g.blocked {
		if g.blocked[b] {
			if err := g.append("quota", map[string]any{"backend": backends[b], "state": "open"}); err != nil {
				return err
			}
		}
	}
	return nil
}

// step appends a session's next event, planning a turn when it has none.
func (g *gen) step(s *session) error {
	if len(s.queue) == 0 {
		if !s.big && s.turns >= s.target {
			return g.retire(s)
		}
		g.cycle(s)
	}
	o := s.queue[0]
	s.queue = s.queue[1:]
	if q, ok := o.val.(quotaHit); ok {
		g.blocked[q.b] = true
		g.openAt[q.b] = g.now + int64(2+g.r.IntN(8))*3_600_000
		return g.append("quota", map[string]any{"backend": backends[q.b], "state": "blocked", "seen_in": q.turn})
	}
	return g.append(o.list, o.val)
}

type quotaHit struct {
	b    int
	turn string
}

func (g *gen) retire(s *session) error {
	if err := g.append(fmt.Sprintf("sessions.%d.state", s.i), "archived"); err != nil {
		return err
	}
	for i, a := range g.active {
		if a == s {
			g.active = append(g.active[:i], g.active[i+1:]...)
			break
		}
	}
	_, err := g.newSession(false)
	return err
}

// pick chooses the next session to act, skipping those waiting on a quota.
func (g *gen) pick() *session {
	ready := func(s *session) bool { return !g.blocked[backendIndex(s.backend)] || len(s.queue) > 0 }
	if g.big != nil && g.r.Float64() < 0.36 && ready(g.big) {
		return g.big
	}
	for tries := 0; tries < 20; tries++ {
		s := g.active[g.r.IntN(len(g.active))]
		if ready(s) {
			return s
		}
	}
	for _, s := range g.active {
		if ready(s) {
			return s
		}
	}
	return nil
}

func backendIndex(b string) int {
	if b == "govcloud" {
		return 1
	}
	return 0
}

// interval returns an exponential wait with the given mean, in ms.
func (g *gen) interval(mean float64) int64 { return int64(g.r.ExpFloat64()*mean) + 1 }

// globals appends logins, default changes and quota openings that are due.
func (g *gen) globals() error {
	for b := range backends {
		if g.now >= g.nextLogin[b] {
			login := map[string]any{"backend": backends[b], "outcome": "ok", "expires": g.now + 3_600_000}
			g.nextLogin[b] = g.now + 3_600_000
			if g.r.IntN(40) == 0 {
				login = map[string]any{"backend": backends[b], "outcome": "failed", "error": "invalid password or expired key"}
				g.nextLogin[b] = g.now + 60_000
			}
			if err := g.append("logins", login); err != nil {
				return err
			}
		}
		if g.blocked[b] && g.now >= g.openAt[b] {
			g.blocked[b] = false
			if err := g.append("quota", map[string]any{"backend": backends[b], "state": "open"}); err != nil {
				return err
			}
		}
	}
	if g.now >= g.nextDefault {
		g.nextDefault = g.now + g.interval(67*60_000)
		return g.changeDefault()
	}
	return nil
}

func (g *gen) initDefaults() error {
	g.backend, g.model, g.effort, g.workdir = "commercial", models[0], "high", "/home/sophie/work/billing"
	g.nanny, g.retry, g.compactAt = "off", "on", 150_000
	g.wd = [5]int{30, 120, 600, 3600, 20}
	g.agents = map[string][]byte{}
	for _, p := range projects {
		g.agents[p] = g.doc(2000 + g.r.IntN(5000))
	}
	sets := []op{
		{"defaults.backend", g.backend}, {"defaults.model", g.model}, {"defaults.effort", g.effort},
		{"defaults.workdir", g.workdir}, {"defaults.compact_at", g.compactAt}, {"defaults.nanny", g.nanny},
		{"defaults.retry_quota", g.retry},
	}
	for i, n := range []string{"w1", "w2", "w3", "w4", "check"} {
		sets = append(sets, op{"defaults.watchdog." + n, g.wd[i]})
	}
	for i, k := range promptKinds {
		size := 1500 + g.r.IntN(2500)
		if k == "base" {
			size = 10_000 + g.r.IntN(8000)
		}
		g.prompts[i] = [][]byte{g.doc(size)}
		sets = append(sets, op{"defaults.prompts." + k, g.prompts[i][0]})
	}
	for _, o := range sets {
		if err := g.append(o.list, o.val); err != nil {
			return err
		}
	}
	return nil
}

// doc returns a document of about n bytes: headings, prose and commands.
func (g *gen) doc(n int) []byte {
	var b bytes.Buffer
	for b.Len() < n {
		fmt.Fprintf(&b, "## %s %s\n\n", words[g.r.IntN(len(words))], words[g.r.IntN(len(words))])
		b.Write(g.t.slice(g.r, g.t.prose, 200+g.r.IntN(800)))
		b.WriteString("\n\n")
		if g.r.IntN(3) == 0 {
			b.WriteString("```bash\n")
			b.Write(g.t.slice(g.r, g.t.bash, 80+g.r.IntN(300)))
			b.WriteString("\n```\n\n")
		}
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// edit returns a changed copy of a prompt: a section rewritten, added or cut.
func (g *gen) edit(p []byte, max int) []byte {
	parts := bytes.Split(p, []byte("\n\n## "))
	i := g.r.IntN(len(parts))
	switch {
	case len(p) < max && g.r.IntN(2) == 0:
		add := g.doc(300 + g.r.IntN(1500))
		parts = append(parts[:i+1], append([][]byte{bytes.TrimPrefix(add, []byte("## "))}, parts[i+1:]...)...)
	case len(parts) > 3 && g.r.IntN(3) == 0:
		parts = append(parts[:i], parts[i+1:]...)
	default:
		parts[i] = bytes.TrimPrefix(g.doc(len(parts[i])), []byte("## "))
	}
	out := bytes.Join(parts, []byte("\n\n## "))
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func (g *gen) changeDefault() error {
	g.st.Defaults++
	x := g.r.IntN(100)
	switch {
	case x < 15:
		g.model = models[g.r.IntN(len(models))]
		return g.append("defaults.model", g.model)
	case x < 25:
		g.effort = efforts[g.r.IntN(len(efforts))]
		return g.append("defaults.effort", g.effort)
	case x < 30:
		g.workdir = "/home/sophie/work/" + projects[g.r.IntN(len(projects))]
		return g.append("defaults.workdir", g.workdir)
	case x < 40:
		g.compactAt = 80_000 + 10_000*g.r.IntN(12)
		return g.append("defaults.compact_at", g.compactAt)
	case x < 50:
		g.nanny = map[string]string{"on": "off", "off": "on"}[g.nanny]
		return g.append("defaults.nanny", g.nanny)
	case x < 55:
		g.retry = map[string]string{"on": "off", "off": "on"}[g.retry]
		return g.append("defaults.retry_quota", g.retry)
	case x < 58:
		g.backend = backends[g.r.IntN(2)]
		return g.append("defaults.backend", g.backend)
	case x < 80:
		i := g.r.IntN(5)
		base := []int{30, 120, 600, 3600, 20}[i]
		g.wd[i] = base/2 + g.r.IntN(base)
		return g.append("defaults.watchdog."+[]string{"w1", "w2", "w3", "w4", "check"}[i], g.wd[i])
	}
	k := []int{0, 0, 1, 2, 3}[g.r.IntN(5)]
	max := 6000
	if k == 0 {
		max = 50_000
	}
	versions := g.prompts[k]
	next := g.edit(versions[len(versions)-1], max)
	g.prompts[k] = append(versions, next)
	return g.append("defaults.prompts."+promptKinds[k], next)
}

// newSession creates a session from the defaults, with some overrides, and
// appends it.
func (g *gen) newSession(big bool) (*session, error) {
	s := &session{i: len(g.sessions), big: big, resume: -1, fresh: true, limit: 200_000}
	s.target = lognormal(g.r, 1500*g.scale, 0.6, max(8, int(200*g.scale)), int(9000*g.scale)+8)
	s.backend, s.compactAt, s.wd, s.workdir = g.backend, g.compactAt, g.wd, g.workdir
	model, effort, retry := g.model, g.effort, g.retry
	if g.r.IntN(10) == 0 {
		s.backend = backends[1-backendIndex(s.backend)]
	}
	if g.r.IntN(8) == 0 {
		model = models[g.r.IntN(len(models))]
	}
	if g.r.IntN(6) == 0 {
		s.workdir = "/home/sophie/work/" + projects[g.r.IntN(len(projects))]
	}
	if g.r.IntN(10) == 0 {
		s.compactAt = 60_000 + 10_000*g.r.IntN(12)
	}
	if model == "orion-1" {
		s.limit = 1_000_000
	}
	var versions [4]int
	for k := range promptKinds {
		versions[k] = len(g.prompts[k]) - 1
		if g.r.IntN(6) == 0 {
			versions[k] = g.r.IntN(len(g.prompts[k])) // pinned to an older prompt
		}
	}
	s.base = versions[0]
	setup := map[string]any{
		"backend": s.backend, "model": model, "effort": effort, "workdir": s.workdir,
		"compact_at": s.compactAt, "retry_quota": retry,
		"watchdog": map[string]any{"w1": s.wd[0], "w2": s.wd[1], "w3": s.wd[2], "w4": s.wd[3], "check": s.wd[4]},
		"prompts": map[string]any{
			"base":       fmt.Sprintf("defaults.prompts.base.%d", versions[0]),
			"watchdog":   fmt.Sprintf("defaults.prompts.watchdog.%d", versions[1]),
			"timeout":    fmt.Sprintf("defaults.prompts.timeout.%d", versions[2]),
			"compaction": fmt.Sprintf("defaults.prompts.compaction.%d", versions[3]),
		},
	}
	item := map[string]any{"setup": setup}
	if g.r.IntN(10) == 0 {
		for _, o := range g.active {
			if o.turns > 0 && len(o.queue) == 0 {
				item["spawned_by"] = fmt.Sprintf("sessions.%d.turns.%d", o.i, o.turns-1)
				break
			}
		}
	}
	if err := g.append("sessions", item); err != nil {
		return nil, err
	}
	g.sessions = append(g.sessions, s)
	g.active = append(g.active, s)
	g.st.Sessions++
	p := fmt.Sprintf("sessions.%d", s.i)
	name := fmt.Sprintf("%s: %s %s", projects[g.r.IntN(len(projects))], words[g.r.IntN(len(words))], words[g.r.IntN(len(words))])
	if big {
		name = "infra: the long migration"
	}
	s.nanny = g.nanny == "on"
	s.names++
	s.queue = append(s.queue, op{p + ".name", name}, op{p + ".nanny", onOff(s.nanny)}, op{p + ".state", "active"})
	return s, nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// cycle plans one turn and everything it causes.
func (g *gen) cycle(s *session) {
	p := fmt.Sprintf("sessions.%d", s.i)
	turn := func(t int) string { return fmt.Sprintf("%s.turns.%d", p, t) }
	if g.r.IntN(800) == 0 {
		s.names++
		s.queue = append(s.queue, op{p + ".name", fmt.Sprintf("%s: %s %s", projects[g.r.IntN(len(projects))], words[g.r.IntN(len(words))], words[g.r.IntN(len(words))])})
	}
	if g.r.IntN(600) == 0 {
		s.nanny = !s.nanny
		s.queue = append(s.queue, op{p + ".nanny", onOff(s.nanny)})
	}
	if s.used > s.compactAt {
		from := s.api
		s.apis++
		s.api = fmt.Sprintf("api-%d-%d", s.i, s.apis)
		handover := g.doc(1500 + g.r.IntN(7000))
		s.queue = append(s.queue, op{p + ".compactions", map[string]any{
			"from_session": from, "to_session": s.api,
			"at":     map[string]any{"used": s.used, "limit": s.limit},
			"prompt": g.prompts[3][len(g.prompts[3])-1], "handover": handover,
		}})
		s.resume = s.compactions
		s.compactions++
		s.fresh = true
		s.last = handover
		s.used = 0
	}
	if s.api == "" {
		s.apis++
		s.api = fmt.Sprintf("api-%d-%d", s.i, s.apis)
	}
	kind := "auto"
	var about any
	var msg []byte
	switch {
	case s.resume >= 0:
		kind, about = "resume", fmt.Sprintf("%s.compactions.%d", p, s.resume)
		s.resume = -1
	case s.turns == 0 || g.r.IntN(12) == 0:
		kind = "user"
		msg = g.t.slice(g.r, g.t.prose, lognormal(g.r, 200, 1, 20, 3000))
		s.messages++
		s.queue = append(s.queue, op{p + ".messages", map[string]any{"from": "user", "text": msg}})
	}
	var prompt []byte
	switch {
	case s.fresh:
		prompt = append(prompt, g.prompts[0][s.base]...)
		prompt = append(prompt, "\n\n# AGENTS.md\n\n"...)
		prompt = append(prompt, g.agents[projectOf(s.workdir)]...)
		prompt = append(prompt, "\n\n"...)
		if msg != nil {
			prompt = append(prompt, msg...)
		} else {
			prompt = append(prompt, s.last...)
		}
		s.fresh = false
	case kind == "user":
		prompt = msg
	default:
		prompt = g.feedback(s.last)
	}
	t := s.turns
	s.turns++
	g.st.Turns++
	reply, summary, level := g.script()
	check := "ok"
	var checkErr any
	if x := g.r.IntN(100); x < 2 {
		check, checkErr = "syntax", fmt.Sprintf("bash: line %d: syntax error near unexpected token `%s'", 2+g.r.IntN(30), []string{"fi", "done", ")", "}"}[g.r.IntN(4)])
	} else if x < 4 {
		check, checkErr = "no_start", "the script must call sophie_start <level> <summary> first"
		reply = bytes.SplitN(reply, []byte("\n"), 2)[1]
	}
	s.used += (len(prompt) + len(reply)) / 4
	tv := map[string]any{
		"kind": kind, "api_session": s.api, "about": about,
		"latency": lognormal(g.r, 4000, 0.8, 300, 120_000), "retries": g.retries(),
		"prompt": prompt, "reply": reply,
		"context": map[string]any{"used": s.used, "limit": s.limit},
		"check":   check, "check_error": checkErr,
	}
	s.queue = append(s.queue, op{p + ".turns", tv})
	if check != "ok" {
		s.last = []byte(checkErr.(string))
		return
	}
	approval := "auto"
	var edited any
	if s.nanny {
		switch x := g.r.IntN(100); {
		case x < 8:
			approval, edited = "edited", append(append([]byte(nil), reply...), "\n# edited: keep the change small\n"...)
		case x < 14:
			approval = "rejected"
		default:
			approval = "approved"
		}
	}
	launch := map[string]any{"approval": approval, "edited": edited, "level": level, "summary": summary}
	if approval != "rejected" {
		launch["pid"] = 1000 + g.r.IntN(4_000_000)
	}
	s.queue = append(s.queue, op{turn(t) + ".launch", launch})
	if approval == "rejected" {
		s.last = []byte("The user rejected the script.")
		return
	}
	li := int(level[1] - '1')
	duration := lognormal(g.r, 3000, 1.6, 20, 7_200_000)
	reason := "done"
	if duration > s.wd[li]*1000 && g.r.IntN(5) != 0 {
		// The watchdog fires while the script runs.
		w := s.turns
		s.turns++
		g.st.Turns++
		partial := g.output()
		wreply, wsummary, _ := g.script()
		s.queue = append(s.queue,
			op{p + ".turns", map[string]any{"kind": "watchdog", "api_session": s.api, "about": turn(t),
				"latency": lognormal(g.r, 3000, 0.8, 300, 60_000), "retries": 0,
				"prompt": append(append(append([]byte(nil), g.prompts[1][len(g.prompts[1])-1]...), "\n\n"...), partial...), "reply": wreply,
				"context": map[string]any{"used": s.used, "limit": s.limit}, "check": "ok"}},
			op{turn(w) + ".launch", map[string]any{"approval": "auto", "level": "w1", "summary": wsummary, "pid": 1000 + g.r.IntN(4_000_000)}})
		if g.r.IntN(4) == 0 {
			to := s.turns
			s.turns++
			g.st.Turns++
			treply, tsummary, _ := g.script()
			s.queue = append(s.queue,
				op{turn(w) + ".exit", map[string]any{"reason": "timeout", "stdout": g.output(), "stderr": "", "duration": s.wd[4]*1000 + g.r.IntN(500)}},
				op{p + ".turns", map[string]any{"kind": "timeout", "api_session": s.api, "about": turn(t),
					"latency": lognormal(g.r, 3000, 0.8, 300, 60_000), "retries": 0,
					"prompt": g.prompts[2][len(g.prompts[2])-1], "reply": treply,
					"context": map[string]any{"used": s.used, "limit": s.limit}, "check": "ok"}},
				op{turn(to) + ".launch", map[string]any{"approval": "auto", "level": "w1", "summary": tsummary, "pid": 1000 + g.r.IntN(4_000_000)}},
				op{turn(to) + ".exit", map[string]any{"reason": "done", "code": 0, "stdout": g.output(), "stderr": "", "duration": lognormal(g.r, 800, 1, 10, 20_000)}})
		} else {
			s.queue = append(s.queue, op{turn(w) + ".exit", map[string]any{"reason": "done", "code": 0, "stdout": g.output(), "stderr": "", "duration": lognormal(g.r, 800, 1, 10, 15_000)}})
		}
		if g.r.IntN(2) == 0 {
			reason = "killed"
		}
	}
	if g.r.IntN(4) == 0 {
		s.messages++
		s.queue = append(s.queue, op{p + ".messages", map[string]any{"from": "agent", "text": g.t.slice(g.r, g.t.prose, lognormal(g.r, 120, 0.8, 20, 2000)), "turn": turn(t)}})
	}
	if g.r.IntN(30) == 0 {
		s.results++
		s.queue = append(s.queue, op{p + ".results", map[string]any{"message": g.t.slice(g.r, g.t.prose, lognormal(g.r, 300, 0.8, 30, 4000)), "turn": turn(t)}})
	}
	if b := backendIndex(s.backend); !g.blocked[b] && g.now >= g.nextBlock[b] {
		g.nextBlock[b] = g.now + g.interval(48*3_600_000)
		s.queue = append(s.queue, op{"quota", quotaHit{b: b, turn: turn(t)}})
	}
	out := g.output()
	var stderr []byte
	if g.r.IntN(4) == 0 {
		stderr = g.t.slice(g.r, g.t.logs, lognormal(g.r, 150, 1.2, 10, 20_000))
	}
	exit := map[string]any{"reason": reason, "stdout": out, "stderr": stderr, "duration": duration}
	if reason == "done" {
		code := 0
		if g.r.IntN(6) == 0 {
			code = 1 + g.r.IntN(2)
		}
		exit["code"] = code
	}
	s.queue = append(s.queue, op{turn(t) + ".exit", exit})
	s.last = out
}

func projectOf(workdir string) string {
	for i := len(workdir) - 1; i >= 0; i-- {
		if workdir[i] == '/' {
			return workdir[i+1:]
		}
	}
	return workdir
}

func (g *gen) retries() int {
	if g.r.IntN(20) == 0 {
		return 1 + g.r.IntN(3)
	}
	return 0
}

// script returns a reply: sophie_start, then commands.
func (g *gen) script() (reply []byte, summary string, level string) {
	level = levels[[]int{0, 0, 0, 1, 1, 2, 3}[g.r.IntN(7)]]
	summary = fmt.Sprintf("%s the %s %s", words[g.r.IntN(len(words))], words[g.r.IntN(len(words))], words[g.r.IntN(len(words))])
	var b bytes.Buffer
	fmt.Fprintf(&b, "sophie_start %s %q\n", level, summary)
	b.Write(g.t.slice(g.r, g.t.bash, lognormal(g.r, 800, 0.7, 150, 5000)))
	if g.r.IntN(5) == 0 {
		fmt.Fprintf(&b, "\nsophie_chat %q", string(bytes.TrimSpace(g.t.line(g.r, g.t.prose))))
	}
	return b.Bytes(), summary, level
}

// output returns a script's stdout: often empty, sometimes very long.
func (g *gen) output() []byte {
	if g.r.IntN(7) == 0 {
		return nil
	}
	n := lognormal(g.r, 250, 1.6, 1, 200_000)
	if g.r.IntN(2000) == 0 {
		n = 100_000 + g.r.IntN(100_000)
	}
	return g.t.slice(g.r, g.t.logs, n)
}

// feedback is the auto prompt after a script ends: its output.
func (g *gen) feedback(out []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "[exit %d after %.1fs]\n", []int{0, 0, 0, 0, 1}[g.r.IntN(5)], math.Round(g.r.ExpFloat64()*40)/10)
	if len(out) > 64_000 {
		b.Write(out[:32_000])
		fmt.Fprintf(&b, "\n[… %d bytes cut …]\n", len(out)-64_000)
		b.Write(out[len(out)-32_000:])
	} else {
		b.Write(out)
	}
	return b.Bytes()
}
