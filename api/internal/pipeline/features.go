package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// FeatureRFI names the RFI feature in a FeatureState.
const FeatureRFI = "rfi"

// FeatureState says whether the user may use a feature on a project and,
// when they may not, Procore's reason.
type FeatureState struct {
	Feature   string
	Available bool
	Reason    string
}

// FeatureProbe selects how Features decides whether the RFI feature is
// available.
type FeatureProbe string

const (
	// ProbeRFIManagers asks Procore for the project's potential RFI
	// managers with the user's token: a 403 means the user may not create
	// RFIs, a 404 that the RFI tool is off.
	ProbeRFIManagers FeatureProbe = "rfi_managers"
	// ProbeCreateOutcome follows the user's last recorded RFI create on the
	// project (RecordCreateOutcome) within the TTL; with none, the feature
	// is available.
	ProbeCreateOutcome FeatureProbe = "create_outcome"
)

// rfiToolOffReason is the reason given when the project's RFI tool answers
// 404.
const rfiToolOffReason = "RFI tool not enabled on this project"

type featureKey struct {
	user    uuid.UUID
	company int64
	project int64
}

type featureEntry struct {
	states []FeatureState
	at     time.Time
}

type createOutcome struct {
	state FeatureState
	at    time.Time
}

// Features answers, per user and project, which features the user may use,
// so the phone can say so before anything is recorded. Answers are cached
// in memory per (user, company, project) for the TTL.
type Features struct {
	lookups *Lookups
	pc      ProcoreLookups
	tokens  AccessTokenSource
	probe   FeatureProbe
	ttl     time.Duration
	now     func() time.Time
	log     *slog.Logger

	mu       sync.Mutex
	cache    map[featureKey]featureEntry
	outcomes map[featureKey]createOutcome
}

// NewFeatures builds a Features. lookups supplies the project visibility
// check. A nil logger means slog.Default().
func NewFeatures(lookups *Lookups, pc ProcoreLookups, tokens AccessTokenSource, probe FeatureProbe, ttl time.Duration, now func() time.Time, logger *slog.Logger) *Features {
	if logger == nil {
		logger = slog.Default()
	}
	return &Features{
		lookups: lookups, pc: pc, tokens: tokens, probe: probe, ttl: ttl, now: now, log: logger,
		cache: map[featureKey]featureEntry{}, outcomes: map[featureKey]createOutcome{},
	}
}

// Get returns the user's features on the project and when they were
// determined. The user must be able to see the project, checked first with
// their own token (procore.ErrPermissionDenied otherwise). An answer younger
// than the TTL is served unless force is set. A probe failure that says
// nothing about the user's rights (Procore unreachable, a 5xx, a token
// problem) is returned as the error and not cached.
func (f *Features) Get(ctx context.Context, userID uuid.UUID, companyID, projectID int64, force bool) ([]FeatureState, time.Time, error) {
	if err := f.lookups.requireAccess(ctx, userID, companyID, projectID); err != nil {
		return nil, time.Time{}, err
	}
	key := featureKey{userID, companyID, projectID}
	if !force {
		f.mu.Lock()
		e, ok := f.cache[key]
		f.mu.Unlock()
		if ok && f.now().Sub(e.at) < f.ttl {
			return append([]FeatureState(nil), e.states...), e.at, nil
		}
	}

	rfi, err := f.probeRFI(ctx, key)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !rfi.Available {
		f.log.Info("rfi feature unavailable", "company_id", companyID, "project_id", projectID, "probe", f.probe, "reason", rfi.Reason)
	}
	states := []FeatureState{rfi}
	at := f.now()
	f.mu.Lock()
	f.cache[key] = featureEntry{states: states, at: at}
	f.mu.Unlock()
	return append([]FeatureState(nil), states...), at, nil
}

// Invalidate drops the cached answer for the user's project, so the next Get
// probes again.
func (f *Features) Invalidate(userID uuid.UUID, companyID, projectID int64) {
	f.mu.Lock()
	delete(f.cache, featureKey{userID, companyID, projectID})
	f.mu.Unlock()
}

// RecordCreateOutcome records the result of the user's RFI create on the
// project for ProbeCreateOutcome: success makes the feature available, a 403
// unavailable with Procore's reason, a 404 unavailable as the tool being
// off. Any other error says nothing about the user's rights and is ignored.
// A recorded outcome drops the cached answer.
func (f *Features) RecordCreateOutcome(userID uuid.UUID, companyID, projectID int64, err error) {
	state, ok := rfiStateFor(err)
	if !ok {
		return
	}
	key := featureKey{userID, companyID, projectID}
	f.mu.Lock()
	f.outcomes[key] = createOutcome{state: state, at: f.now()}
	delete(f.cache, key)
	f.mu.Unlock()
}

// probeRFI decides the RFI feature's state with the configured probe.
func (f *Features) probeRFI(ctx context.Context, key featureKey) (FeatureState, error) {
	if f.probe == ProbeCreateOutcome {
		f.mu.Lock()
		o, ok := f.outcomes[key]
		f.mu.Unlock()
		if ok && f.now().Sub(o.at) < f.ttl {
			return o.state, nil
		}
		return FeatureState{Feature: FeatureRFI, Available: true}, nil
	}

	token, err := f.tokens.AccessToken(ctx, key.user)
	if err != nil {
		return FeatureState{}, fmt.Errorf("%w: %w", ErrLookupToken, err)
	}
	_, err = f.pc.ListPotentialRFIManagers(ctx, token, key.company, key.project)
	state, ok := rfiStateFor(err)
	if !ok {
		return FeatureState{}, err
	}
	return state, nil
}

// rfiStateFor maps the outcome of an RFI call to the feature's state. ok is
// false for an error that says nothing about the user's rights.
func rfiStateFor(err error) (FeatureState, bool) {
	var api *procore.APIError
	switch {
	case err == nil:
		return FeatureState{Feature: FeatureRFI, Available: true}, true
	case errors.Is(err, procore.ErrPermissionDenied):
		return FeatureState{Feature: FeatureRFI, Reason: ProcoreReason(err)}, true
	case errors.As(err, &api) && api.Status == http.StatusNotFound:
		return FeatureState{Feature: FeatureRFI, Reason: rfiToolOffReason}, true
	}
	return FeatureState{}, false
}

// genericDenied is the reason given for a Procore 403 without a body.
const genericDenied = "Procore permission denied"

// reasonMaxRunes caps a reason taken from a Procore body.
const reasonMaxRunes = 200

// jsonNoise is what surrounds the text in a JSON error body.
const jsonNoise = "{}[]\" \t\r\n"

// jsonKeyPrefix matches the `key":` left at the start of a cut-off JSON body
// once its leading `{"` is trimmed.
var jsonKeyPrefix = regexp.MustCompile(`^[A-Za-z_][\w.-]*"\s*:\s*`)

// ProcoreReason is the text of the Procore response behind a permission
// error, for showing to the user: the body's strings with the JSON around
// them stripped ({"errors":["App is not connected to this company."]} gives
// "App is not connected to this company."), a plain-text body as is, cut to
// its first 200 runes and trimmed. Without a body, or with an HTML one (an
// error page from a proxy in front of Procore), it is "Procore permission
// denied".
func ProcoreReason(err error) string {
	var api *procore.APIError
	if !errors.As(err, &api) {
		return genericDenied
	}
	body := strings.TrimSpace(strings.ToValidUTF8(api.Body, ""))
	if strings.HasPrefix(body, "<") {
		return genericDenied
	}
	text := stripJSONNoise(body)
	if r := []rune(text); len(r) > reasonMaxRunes {
		text = string(r[:reasonMaxRunes])
	}
	if text = strings.TrimSpace(text); text == "" {
		return genericDenied
	}
	return text
}

// stripJSONNoise returns the strings in a JSON body joined with "; ", in
// key order. A body that is not JSON is returned trimmed, and when it looks
// like JSON cut off by the client's body limit, without its leading
// brackets, quotes and first key.
func stripJSONNoise(body string) string {
	body = strings.TrimSpace(body)
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		return strings.Join(jsonStrings(v, nil), "; ")
	}
	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		body = strings.Trim(body, jsonNoise)
		body = strings.Trim(jsonKeyPrefix.ReplaceAllString(body, ""), jsonNoise)
	}
	return body
}

// jsonStrings appends the non-empty string leaves of v to out, visiting
// object keys in sorted order.
func jsonStrings(v any, out []string) []string {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	case []any:
		for _, e := range t {
			out = jsonStrings(e, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = jsonStrings(t[k], out)
		}
	}
	return out
}
