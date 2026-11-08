package httpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// This file holds the retry loop every door runs (schedule.run), the two
// loop doors (Do and GetBytes) and their option vocabulary. The transport
// door in roundtripper.go takes a struct (TransportConfig); the loop doors
// take functional options because their dominant call pattern is zero to two
// options inline at a call expression.

// --- Option vocabulary ---

// DoOption configures Do. Options constructed as Option values apply to both
// loop doors; Do-only options (WithLabel, WithRateLimitRetry,
// WithRateLimitOnly, WithAttemptTimeout) implement only this interface, so
// passing one to GetBytes is a compile error.
type DoOption interface {
	applyDo(*loopConfig)
}

// GetOption configures GetBytes. GetBytes-only options (WithHeaders,
// WithMaxBodyBytes) implement only this interface, so passing one to Do is a
// compile error.
type GetOption interface {
	applyGet(*getConfig)
}

// Option is a retry option accepted by BOTH loop doors, Do and GetBytes:
// WithMaxAttempts, WithBaseDelay, WithLogger and WithExhaustedLevel construct
// Option values.
type Option interface {
	DoOption
	GetOption
}

// rlMode selects how Do treats a 429 *StatusError.
type rlMode uint8

const (
	rlNone  rlMode = iota // default: rate limits are not retried (IsTransient excludes them)
	rlRetry               // WithRateLimitRetry: transients AND rate limits are retried
	rlOnly                // WithRateLimitOnly: ONLY rate limits are retried
)

// loopConfig holds the retry-loop settings shared by Do and GetBytes, plus
// the Do-only fields.
type loopConfig struct {
	logger         *slog.Logger
	label          string
	baseDelay      time.Duration
	rlMaxWait      time.Duration
	attemptTimeout time.Duration
	maxAttempts    int
	exhaustedLvl   slog.Level
	exhaustedLvlOn bool
	rlMode         rlMode
}

// getConfig holds GetBytes settings: the shared loop settings plus the
// GET-door specifics.
type getConfig struct {
	setHeaders func(*http.Request)
	loopConfig
	maxBodyBytes int64
}

// maxAttemptsOption implements Option for WithMaxAttempts.
type maxAttemptsOption int

func (o maxAttemptsOption) applyDo(c *loopConfig) { c.maxAttempts = int(o) }
func (o maxAttemptsOption) applyGet(c *getConfig) { o.applyDo(&c.loopConfig) }

// WithMaxAttempts sets the maximum number of attempts (TOTAL, including the
// first call). Default: 3. A value below 1 is treated as 1, so the operation
// always runs at least once (never a silent no-op).
func WithMaxAttempts(n int) Option { return maxAttemptsOption(n) }

// baseDelayOption implements Option for WithBaseDelay.
type baseDelayOption time.Duration

func (o baseDelayOption) applyDo(c *loopConfig) { c.baseDelay = time.Duration(o) }
func (o baseDelayOption) applyGet(c *getConfig) { o.applyDo(&c.loopConfig) }

// WithBaseDelay sets the initial backoff delay. Default: one second. A
// non-positive value falls back to the default.
func WithBaseDelay(d time.Duration) Option { return baseDelayOption(d) }

// loggerOption implements Option for WithLogger.
type loggerOption struct{ l *slog.Logger }

func (o loggerOption) applyDo(c *loopConfig) { c.logger = o.l }
func (o loggerOption) applyGet(c *getConfig) { o.applyDo(&c.loopConfig) }

// WithLogger sets the logger for retry diagnostics. Default: slog.Default().
// A nil logger falls back to the default.
func WithLogger(l *slog.Logger) Option { return loggerOption{l: l} }

// exhaustedLevelOption implements Option for WithExhaustedLevel.
type exhaustedLevelOption slog.Level

func (o exhaustedLevelOption) applyDo(c *loopConfig) {
	c.exhaustedLvl, c.exhaustedLvlOn = slog.Level(o), true
}
func (o exhaustedLevelOption) applyGet(c *getConfig) { o.applyDo(&c.loopConfig) }

// WithExhaustedLevel overrides the level of the terminal "retries exhausted"
// line (see exhaustedLevel for the default rule).
//
// It exists for the caller that reports the same terminal failure itself, with
// more context than this door can have. Such a caller wants the per-attempt
// retry diagnostics and NOT the verdict: leaving both produces two log lines
// for one failure, and silencing the whole logger to stop the second throws the
// diagnostics away with it. Pass slog.LevelDebug to keep the line for diagnosis
// while the caller's own line carries the alarm.
//
// It does not change what is returned, and it does not suppress the line: a
// level below the logger's threshold simply is not emitted, which is the
// caller's decision to make.
func WithExhaustedLevel(l slog.Level) Option { return exhaustedLevelOption(l) }

// labelOption implements DoOption for WithLabel.
type labelOption string

func (o labelOption) applyDo(c *loopConfig) { c.label = string(o) }

// WithLabel sets the operation label used in Do's log lines ("<label> failed,
// retrying", "<label> retries exhausted"). Default: "operation".
func WithLabel(s string) DoOption { return labelOption(s) }

// attemptTimeoutOption implements DoOption for WithAttemptTimeout.
type attemptTimeoutOption time.Duration

func (o attemptTimeoutOption) applyDo(c *loopConfig) { c.attemptTimeout = time.Duration(o) }

// WithAttemptTimeout bounds EACH attempt by d and makes that bound's expiry
// RETRYABLE: the per-attempt (per-try) timeout, as distinct from the caller's
// total budget. It is the piece that joins a per-attempt deadline to the retry
// loop; without it a deadline inside fn is classified terminal (see
// [AttemptTimeout] for why an error alone cannot say otherwise). A
// non-positive d means no per-attempt bound, which is the option's absence.
//
// fn receives a context derived from the caller's with d applied. Context
// keeps the EARLIER deadline, so a caller deadline nearer than d still governs
// and the total budget is never extended — d caps one attempt, never the call.
//
// The expiry is marked as the attempt's ONLY when the caller's own context is
// still live, so retrying a caller that is out of budget stays impossible: an
// expired or canceled caller context is never marked, [Do] returns ctx.Err()
// before it classifies anything, and [SleepCtx] refuses to wait on a dead
// context. The decision is made from the two contexts rather than from the
// error, because a [context.DeadlineExceeded] value is identical either way.
//
// The attempt context is canceled as soon as fn returns, so fn must not return
// a value that still depends on it — read, decode, or drain a response body
// inside fn (the same rule a caller-derived per-attempt context already
// imposes).
//
// It is a DoOption: [GetBytes] owns its own request and has no callback to
// bound. Bound that door by running it as one attempt inside Do —
// GetBytes(ctx, ...) with WithMaxAttempts(1) inside a Do callback carrying this
// option — the same composition that avoids multiplying two attempt budgets.
func WithAttemptTimeout(d time.Duration) DoOption { return attemptTimeoutOption(d) }

// rateLimitOption implements DoOption for WithRateLimitRetry and
// WithRateLimitOnly.
type rateLimitOption struct {
	maxWait time.Duration
	mode    rlMode
}

func (o rateLimitOption) applyDo(c *loopConfig) {
	c.rlMode = o.mode
	c.rlMaxWait = o.maxWait
}

// WithRateLimitRetry makes Do also retry a 429 *[StatusError], waiting
// min(RetryAfter, maxWait) when it carries a positive RetryAfter, else maxWait.
// A non-positive maxWait means RetryAfterCap, so the wait is always positive
// and a canceled context is observed before every retry. Transient errors keep
// their usual waits (the backoff, or a positive RetryAfterHint), and the
// exponential base advances on every retry.
// WithRateLimitRetry and WithRateLimitOnly set one mode: the later of the two
// wins, with its maxWait.
func WithRateLimitRetry(maxWait time.Duration) DoOption {
	return rateLimitOption{maxWait: maxWait, mode: rlRetry}
}

// WithRateLimitOnly makes Do retry ONLY a 429 *[StatusError] (matched through
// wrapped errors); every other error, including transient transport errors,
// is returned immediately. The wait semantics match WithRateLimitRetry, the
// terminal Warn is "rate limit retries exhausted", and the FINAL attempt's
// error wins even under an already-canceled context (cancellation is observed
// in the always-positive inter-attempt sleep instead). The later of
// WithRateLimitOnly and WithRateLimitRetry wins.
func WithRateLimitOnly(maxWait time.Duration) DoOption {
	return rateLimitOption{maxWait: maxWait, mode: rlOnly}
}

// headersOption implements GetOption for WithHeaders.
type headersOption struct{ fn func(*http.Request) }

func (o headersOption) applyGet(c *getConfig) { c.setHeaders = o.fn }

// WithHeaders sets a function that is called to set headers on each request.
func WithHeaders(fn func(*http.Request)) GetOption { return headersOption{fn: fn} }

// maxBodyBytesOption implements GetOption for WithMaxBodyBytes.
type maxBodyBytesOption int64

func (o maxBodyBytesOption) applyGet(c *getConfig) { c.maxBodyBytes = int64(o) }

// WithMaxBodyBytes sets the maximum response body size to read.
// Default: 10 MB (10 << 20 bytes). A non-positive value falls back to
// the default.
func WithMaxBodyBytes(n int64) GetOption { return maxBodyBytesOption(n) }

// --- Config assembly ---

// normalize applies defaults and clamps. maxAttempts < 1 means one attempt
// (options can express absence, unlike TransportConfig fields). A non-positive
// rate-limit maxWait takes RetryAfterCap: a zero ceiling would zero every wait
// and the loop would hot-spin with no cancellation check.
func (c *loopConfig) normalize() {
	if c.maxAttempts < 1 {
		c.maxAttempts = 1
	}
	if c.baseDelay <= 0 {
		c.baseDelay = defaultBaseDelay
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.label == "" {
		c.label = "operation"
	}
	if c.rlMode != rlNone && c.rlMaxWait <= 0 {
		c.rlMaxWait = RetryAfterCap
	}
}

// newLoopConfig builds a Do configuration from opts (nil options are skipped)
// and applies defaults.
func newLoopConfig(opts []DoOption) loopConfig {
	cfg := loopConfig{maxAttempts: defaultMaxAttempts}
	for _, o := range opts {
		if o != nil {
			o.applyDo(&cfg)
		}
	}
	cfg.normalize()
	return cfg
}

// newGetConfig builds a GetBytes configuration from opts (nil options are
// skipped) and applies defaults.
func newGetConfig(opts []GetOption) getConfig {
	cfg := getConfig{maxAttempts: defaultMaxAttempts}
	for _, o := range opts {
		if o != nil {
			o.applyGet(&cfg)
		}
	}
	cfg.normalize()
	if cfg.maxBodyBytes <= 0 {
		cfg.maxBodyBytes = defaultMaxBodyBytes
	}
	return cfg
}

// --- The retry loop ---

// schedule is the attempt budget and backoff that Do, GetBytes and the
// RetryRoundTripper all run; the three differ only in how they judge an
// attempt.
type schedule struct {
	attempts   int
	baseDelay  time.Duration
	maxElapsed time.Duration // zero means no ceiling
}

// ErrMaxElapsed is the error a [RetryRoundTripper] returns, wrapped with the
// ceiling and the last attempt's error when there is one, when its next wait
// would meet or pass TransportConfig.MaxElapsedTime. Match it with errors.Is.
var ErrMaxElapsed = errors.New("max elapsed time exceeded")

// run calls attempt with its 0-based index until attempt reports done or the
// budget is spent, and reports which through exhausted. Between attempts it
// resolves the wait (attempt's explicit wait, else the jittered backoff),
// calls pause with the 1-based retry number and that wait, then sleeps. It
// stops early with ErrMaxElapsed, or with the sleep's error once ctx ends.
// pause runs before either early stop, so a door's pause hook can release the
// failed attempt's resources however the loop ends.
func (s schedule) run(ctx context.Context, attempt func(n int) (done bool, wait time.Duration), pause func(retry int, wait time.Duration)) (exhausted bool, err error) {
	start := time.Now()
	backoff := s.baseDelay
	for n := range s.attempts {
		done, explicit := attempt(n)
		if done {
			return false, nil
		}
		if n == s.attempts-1 {
			break
		}
		wait := resolveWait(explicit, backoff)
		pause(n+1, wait)
		if s.elapsedExceeded(wait, start) {
			return false, ErrMaxElapsed
		}
		if err := SleepCtx(ctx, wait); err != nil {
			return false, err
		}
		backoff = SafeDouble(backoff)
	}
	return true, nil
}

// elapsedExceeded reports whether sleeping wait now would meet or pass
// maxElapsed. It compares against the remaining budget rather than summing
// elapsed and wait, so a near-MaxInt64 wait cannot overflow (CWE-190); a spent
// budget leaves a remainder of zero or less, which every wait meets.
func (s schedule) elapsedExceeded(wait time.Duration, start time.Time) bool {
	return s.maxElapsed > 0 && wait >= s.maxElapsed-time.Since(start)
}

// schedule returns the loop doors' budget; normalize has clamped both fields.
func (c *loopConfig) schedule() schedule {
	return schedule{attempts: c.maxAttempts, baseDelay: c.baseDelay}
}

// logRetrySuccess emits the debug line when fn recovered after at least one
// retry (attempt is 0-indexed, so attempt > 0 means a prior failure recovered).
func (c *loopConfig) logRetrySuccess(attempt int) {
	if attempt > 0 {
		c.logger.Debug(c.label+" succeeded after retry", "attempts", attempt+1)
	}
}

// retryAfterHintWait returns the Retry-After hint err carries through
// RetryAfterHint (capped by the implementer, see the interface doc), or zero;
// resolveWait ignores a non-positive one.
func retryAfterHintWait(err error) time.Duration {
	if h, ok := errors.AsType[RetryAfterHint](err); ok {
		return h.RetryAfterHint()
	}
	return 0
}

// resolveWait returns the wait before the next retry: a positive explicit
// wait (a capped Retry-After or a rate-limit wait) takes precedence over the
// jittered exponential backoff.
func resolveWait(explicit, backoff time.Duration) time.Duration {
	if explicit > 0 {
		return explicit
	}
	return JitteredBackoff(backoff)
}

// runAttempt runs one attempt of fn under the configured per-attempt bound
// (WithAttemptTimeout). With no bound configured fn sees the caller's context
// unchanged, so the option-absent path is exactly a direct call.
//
// It is a generic METHOD (Go 1.27). Before 1.27 a method could not declare its
// own type parameter, so this operation had to be a package-level function
// taking its receiver as an argument — the one member of loopConfig's six that
// was not a method, for a reason that no longer exists.
func (c *loopConfig) runAttempt[T any](ctx context.Context, fn func(ctx context.Context) (T, error)) (T, error) {
	if c.attemptTimeout <= 0 {
		return fn(ctx)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, c.attemptTimeout)
	defer cancel()
	result, err := fn(attemptCtx)
	return result, markAttemptTimeout(ctx, attemptCtx, err)
}

// markAttemptTimeout marks err retryable when THIS attempt's bound is what
// expired. The three guards are what make the mark safe:
//
//   - ctx.Err() == nil: the caller still has budget. If the caller's context is
//     the one that ended, the deadline is the caller's and stays terminal —
//     which also covers the case where the caller's deadline is the earlier of
//     the two and therefore the one the attempt context inherited.
//   - attemptCtx.Err() != nil: the bound this loop installed actually expired,
//     rather than some deadline fn holds internally.
//   - the error reports a deadline: fn may translate its own failure, and the
//     mark must not claim a deadline for an error that is not one. A timeout
//     that does NOT match context.DeadlineExceeded (a net.Error i/o timeout,
//     say) is already transient without any mark.
//
// Every ambiguous case therefore resolves to "leave it alone": the mark is
// applied only when the attempt's own bound is provably the failing one.
func markAttemptTimeout(ctx, attemptCtx context.Context, err error) error {
	if ctx.Err() != nil || attemptCtx.Err() == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return AttemptTimeout(err)
}

// classify reports whether err is retryable under the configured mode and the
// explicit wait to honor (zero means jittered backoff).
func (c *loopConfig) classify(err error) (retryable bool, explicitWait time.Duration) {
	if IsPermanent(err) {
		return false, 0
	}
	if c.rlMode != rlNone {
		if rl, ok := errors.AsType[*StatusError](err); ok && rl.Code == http.StatusTooManyRequests {
			wait := c.rlMaxWait
			if rl.RetryAfter > 0 {
				wait = min(rl.RetryAfter, c.rlMaxWait)
			}
			return true, wait
		}
		if c.rlMode == rlOnly {
			return false, 0
		}
	}
	if !IsTransient(err) {
		return false, 0
	}
	return true, retryAfterHintWait(err)
}

// exhaustedMsg is the terminal message for the configured mode.
func (c *loopConfig) exhaustedMsg() string {
	if c.rlMode == rlOnly {
		return "rate limit retries exhausted"
	}
	return c.label + " retries exhausted"
}

// exhaustedLevel picks the level for a door's terminal failure line. A
// multi-attempt budget that ran out genuinely exhausted a retry tree this door
// owns, which is an operator-visible degradation: Warn.
//
// A single-attempt budget retried nothing, so there is nothing exhausted to
// report. WithMaxAttempts(1) is how a caller runs a door as ONE attempt inside
// its own retry loop (the sanctioned no-3x3-amplification pattern - the same
// reason the Retry-After hint escapes on the returned error), and that loop owns
// both the retry policy and the terminal Warn. Warning here too would
// double-report every such failure and, on the enclosing loop's non-final
// attempts, announce a degradation that self-healed on the next attempt. Debug
// keeps the line for diagnosis without the alarm; the error is returned either
// way, and a non-retryable failure already logs nothing at all - so this also
// makes a one-attempt door's two failure classes log alike.
// A caller that publishes its own terminal verdict overrides both rules with
// WithExhaustedLevel.
func (c *loopConfig) exhaustedLevel() slog.Level {
	if c.exhaustedLvlOn {
		return c.exhaustedLvl
	}
	if c.maxAttempts > 1 {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

// --- Door 1: Do ---

// Do calls fn up to WithMaxAttempts times (total) with jittered exponential
// backoff and returns the first success; a non-retryable error returns at
// once. By default only IsTransient errors retry (a 429 is not transient)
// and a positive RetryAfterHint replaces the backoff wait. After a
// failed attempt a canceled context returns ctx.Err(), except under
// WithRateLimitOnly, where the final attempt's error wins. Retries log at
// Debug to WithLogger; exhaustion logs at Warn, or Debug for a single-attempt
// budget (see exhaustedLevel).
func Do[T any](ctx context.Context, fn func(ctx context.Context) (T, error), opts ...DoOption) (T, error) {
	var zero T
	cfg := newLoopConfig(opts)
	var result T
	var err error
	exhausted, stopErr := cfg.schedule().run(ctx, func(n int) (bool, time.Duration) {
		result, err = cfg.runAttempt(ctx, fn)
		if err == nil {
			cfg.logRetrySuccess(n)
			return true, 0
		}
		if cfg.rlMode != rlOnly && ctx.Err() != nil {
			err = ctx.Err()
			return true, 0
		}
		retryable, wait := cfg.classify(err)
		return !retryable, wait
	}, func(retry int, wait time.Duration) {
		cfg.logger.Debug(cfg.label+" failed, retrying",
			"attempt", retry, "max", cfg.maxAttempts,
			"delay", wait.String(), "error", LogSafeError(err))
	})
	switch {
	case stopErr != nil:
		return zero, stopErr
	case err == nil:
		return result, nil
	case exhausted:
		cfg.logger.Log(ctx, cfg.exhaustedLevel(), cfg.exhaustedMsg(),
			"attempts", cfg.maxAttempts, "error", LogSafeError(err))
	}
	return zero, err
}

// --- Door 2: GetBytes ---

// GetBytes performs a GET, retrying every status IsRetryableStatus accepts and
// transient transport errors with bounded backoff (honoring Retry-After up to
// RetryAfterCap), and returns the body read to WithMaxBodyBytes
// (*ResponseTooLargeError past it). Any other non-2xx is a permanent
// *StatusError. Every logged url and returned error is redacted. Each attempt
// is judged as the RetryRoundTripper's default policy judges a round trip.
// client must not be nil: pass New(timeout) or a client of your own, never
// http.DefaultClient, whose nil CheckRedirect follows a redirect to any host.
func GetBytes(ctx context.Context, client *http.Client, reqURL string, opts ...GetOption) ([]byte, error) {
	cfg := newGetConfig(opts)
	log := cfg.logger

	start := time.Now()
	var body []byte
	var err error
	var retryAfter time.Duration
	exhausted, stopErr := cfg.schedule().run(ctx, func(int) (bool, time.Duration) {
		attemptStart := time.Now()
		var retry bool
		body, retry, retryAfter, err = getAttempt(ctx, client, reqURL, &cfg)
		if err == nil {
			logSlowUpstream(log, reqURL, attemptStart)
		}
		return !retry, retryAfter
	}, func(retry int, _ time.Duration) {
		log.Debug("http request failed, will retry",
			"url", redactURL(reqURL), "attempt", retry, "max", cfg.maxAttempts, "error", LogSafeError(err))
	})
	switch {
	case stopErr != nil:
		return nil, stopErr
	case err == nil:
		return body, nil
	case !exhausted:
		return nil, LogSafeError(err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	log.Log(ctx, cfg.exhaustedLevel(), "http retries exhausted",
		"url", redactURL(reqURL), "attempts", cfg.maxAttempts, "elapsed", elapsed, "error", LogSafeError(err))
	exhaustedErr := fmt.Errorf("retries exhausted after %s: %w", elapsed, LogSafeError(err))
	// The last attempt's Retry-After rides out on the error, so a caller running
	// this door as one attempt (WithMaxAttempts(1)) inside its own loop can
	// still honor it; ParseRetryAfter capped it, as RetryAfterHint requires.
	if retryAfter > 0 {
		return nil, &retryAfterError{err: exhaustedErr, hint: retryAfter}
	}
	return nil, exhaustedErr
}

// retryAfterError carries a capped Retry-After hint on GetBytes's exhaustion
// error so an enclosing retry loop can honor it. It does not implement
// Transient, so IsTransient reads the wrapped failure: the *StatusError's code
// or the transport error's cause.
type retryAfterError struct {
	err  error
	hint time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }

func (e *retryAfterError) Unwrap() error { return e.err }

func (e *retryAfterError) RetryAfterHint() time.Duration { return e.hint }

// logSlowUpstream warns when a successful attempt took longer than 10s. Timed
// per-attempt so the library's own backoff sleeps are not mislabeled as
// upstream latency.
func logSlowUpstream(log *slog.Logger, reqURL string, attemptStart time.Time) {
	if elapsed := time.Since(attemptStart); elapsed > 10*time.Second {
		log.Warn("slow upstream response", "url", redactURL(reqURL), "duration", elapsed.Round(time.Millisecond))
	}
}

// getAttempt performs a single HTTP GET attempt and returns the body on
// success, or the failure with whether to retry it and the response's capped
// Retry-After.
func getAttempt(ctx context.Context, client *http.Client, reqURL string, cfg *getConfig) (body []byte, retry bool, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, false, 0, err
	}
	if cfg.setHeaders != nil {
		cfg.setHeaders(req)
	}
	resp, err := client.Do(req) //nolint:bodyclose // resp.Body is closed on every path: DrainClose (429/5xx, non-2xx) or ReadLimitedBody's deferred close (2xx); bodyclose can't trace the close through the helper.
	retry = retryableRoundTrip(resp, err)
	if err != nil {
		return nil, retry, 0, err
	}
	if retry {
		DrainClose(resp.Body)
		return nil, true, retryAfterOf(resp), newStatusError(resp, reqURL)
	}
	// Any other non-2xx is permanent for this door: a redirect stub or an error
	// page is not the requested resource. CheckStatus owns the success band;
	// this door's error adds the request URL.
	if CheckStatus(resp) != nil {
		DrainClose(resp.Body)
		return nil, false, 0, newStatusError(resp, reqURL)
	}
	// Read the body with overflow detection: an over-limit body fails loud with
	// *ResponseTooLargeError rather than being silently truncated (a truncated
	// payload that looks complete is a corruption hazard). ReadLimitedBody owns
	// the cap+1 probe, its int64-overflow guard, and closing the body.
	body, err = ReadLimitedBody(resp.Body, cfg.maxBodyBytes)
	if err != nil {
		if _, ok := errors.AsType[*ResponseTooLargeError](err); ok {
			return nil, false, 0, err
		}
		return nil, false, 0, fmt.Errorf("read response: %w", err)
	}
	return body, false, 0, nil
}
