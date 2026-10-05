package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Limits bound one user turn. Zero disables a limit. The session Budget is separate.
type Limits struct {
	Steps   int           // model round-trips
	Time    time.Duration // wall clock
	TurnUSD float64       // spend
}

var DefaultLimits = Limits{Steps: 60, Time: 30 * time.Minute, TurnUSD: 2}

// Stats counts guard interventions over the session (shown by /cost, used by evals).
type Stats struct {
	Invalid     int // malformed / schema-invalid / unknown-tool calls rejected
	Denied      int // calls refused by the permission policy
	Flagged     int // tool outputs that looked like prompt injection
	Loops       int // no-progress loops detected
	Challenged  int // success claims challenged for lack of a passing check
	Unbacked    int // claims that stayed unbacked after the challenge
	FactChecks  int // answers sent back because citations or symbols didn't check out
	Unsupported int // references still unsupported after that
	Checkpoints int // workspace checkpoints taken
}

const (
	repeatBlock = 3 // identical call within an edit epoch: blocked on this occurrence
	failStreak  = 8 // consecutive failed calls counted as no progress
	maxLoops    = 2 // loop events per turn before the turn is stopped
)

// turnState tracks one turn's progress for the guards.
type turnState struct {
	factChecked bool // the answer's references were sent back once
	start       time.Time
	cost0       float64
	lim         Limits
	budget      float64
	verify      string
	epoch       int               // advances on every successful edit
	seen        map[string]int    // epoch|tool|canonical args → count
	denied      map[string]string // tool|canonical args → the mode it was refused in
	fails       int               // consecutive failed tool calls
	loops       int
	lastEdit    int // step of the last successful edit (-1: none)
	lastPass    int // step of the last passing verify/build/test (-1: none)
	lastCheck   string
	challenged  bool
	edited      bool
	tree        string // checkpoint taken before this turn's first mutation
	prompt      string // for memory
	answer      string // the model's last text
	failCmd     string // first failing check this turn, and its first error line
	failErr     string
	fix         *Fix // that check passed later: a verified fix
}

func newTurnState(cost0 float64) *turnState {
	return &turnState{start: time.Now(), cost0: cost0, seen: map[string]int{}, denied: map[string]string{}, lastEdit: -1, lastPass: -1}
}

func (st *turnState) callKey(name, args string) string {
	return fmt.Sprintf("%d|%s|%s", st.epoch, name, canonArgs(args))
}

// canonArgs normalises key order and whitespace so cosmetic differences don't hide a repeat.
func canonArgs(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return strings.TrimSpace(s)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// reCheckCmd: shell commands whose success counts as evidence that code works.
var reCheckCmd = regexp.MustCompile(`(?i)\b(go\s+(test|build|vet)|gofmt|cargo\s+(test|check|build|clippy|nextest)|(npm|pnpm|yarn|bun)\s+(run\s+)?(test|build|lint|typecheck|check)|npx\s+(tsc|jest|vitest)|pytest|python3?\s+-m\s+(pytest|unittest|compileall|mypy)|ruff|mypy|tsc|make(\s+(test|check|build|lint|all))?$|make\s+(test|check|build|lint)|gradle\w*\s+(test|build|check)|mvn\s+(test|verify|package)|ctest|jest|vitest|dotnet\s+(test|build)|zig\s+(build|test)|swift\s+(build|test)|mix\s+test|bundle\s+exec\s+rspec|rspec)\b`)

// Completion claims vs. honest hedges in a final answer.
var (
	reClaim = regexp.MustCompile(`(?i)\b(all\s+)?(tests?|specs?|checks?|builds?|compil\w*|lint\w*|ci)\b[^.\n]{0,30}\b(pass(es|ed|ing)?|succeed(s|ed)?|green|clean|compiles?)\b` +
		`|\b(it|this|everything|the\s+(fix|change|feature|code|build|bug))\s+(now\s+)?(works|is\s+working|is\s+fixed|has\s+been\s+fixed|is\s+resolved)\b` +
		`|\b(i('ve|\s+have)?\s+)(verified|confirmed|tested)\b|\b(verified|confirmed)\s+(that|it|the)\b|\bsuccessfully\s+(fixed|implemented|built|tested|resolved)\b|\bworks\s+(correctly|as\s+expected)\b`)
	reHedge = regexp.MustCompile(`(?i)\b(not|n't|never)\s+(yet\s+)?(been\s+)?(able\s+to\s+)?(verif|test|run|confirm|check|compil|buil)\w*` +
		`|\bunverified\b|\buntested\b|\bwithout\s+(running|testing|verifying)\b|\bplease\s+(run|verify|test)\b|\byou\s+(should|can|may\s+want\s+to)\s+(run|verify|test)\b|\bshould\s+(now\s+)?(work|pass|compile)\b|\bI\s+don't\s+know\b`)
)

// claimsSuccess reports an unhedged statement that the work is done/working.
func claimsSuccess(s string) bool {
	return reClaim.MatchString(s) && !reHedge.MatchString(s)
}

const (
	msgLoop  = "[ternly guard] No progress: you are repeating identical tool calls or failing repeatedly. Stop and reconsider — re-read the last error, try a different approach, or ask the user. Repeating the same call will be refused."
	msgClaim = "[ternly guard] Your reply says the work is done or passing, but no verification or build/test command has passed since your last edit in this turn. Run the relevant check now, or state plainly that the result is unverified."
)

func deniedMsg(name string) string {
	return fmt.Sprintf("error: refused — this exact %s call was already refused by the permission policy this turn, and the answer is the same. Don't repeat it: use a different tool (e.g. delete_file for rm), split the command, or say what you need the user to do.", name)
}

func repeatMsg(name string, n int) string {
	return fmt.Sprintf("error: refused — this exact %s call has now been made %d times with no successful edit in between, so its result cannot have changed. Use the earlier result or change approach.", name, n)
}
