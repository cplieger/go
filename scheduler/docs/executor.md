# The job executor

This page covers `Executor`, which runs every job an app starts on its own: a scan, a check, a refresh, a sweep, a batch of writes. It is for developers whose app has more than one kind of job, more than one way to start one, or a record of each job that a person reads.

A job is started by one of three entry points, its `Trigger`: the executor's own timer (`TriggerSchedule`), a person or an API key through the app's API (`TriggerAPI`), or the CLI through the admin socket (`TriggerAdmin`). The executor decides when a job runs, which requests share one job, what waits for what, how a job is cancelled and when a scheduled scope is overdue. What a job does, its kinds, its phases and the words a page shows for them stay the app's.

## Kinds and scopes

Each `KindSpec` declares one kind of job, named in code grammar (`[a-z0-9_]+`, such as `scan` or `full_check`). One payload type `P` covers every kind of an app.

- `Run` does the work and must return once its context is done. It returns an `End`: a `Result`, up to 32 `Reason` codes (most severe first, codes only, never an error's text) and optional `Facts`, a JSON object of at most 2 KiB for the app's own counts and flags.
- `Slot` names the slot the kind runs in. A named slot runs one job at a time; slot `""` starts a job as soon as it is dequeued, beside anything else.
- `Phases` is the kind's pipeline, at most 32 phases, each listed once. `Run.Reached(p)` moves the record to step N of M and keeps each phase's first reached time.
- `Kept` is how long an ended record is kept: 0 writes no record, otherwise 15 minutes to 400 days. A kind with `Kept > 0` needs `Config.Records`.
- `Fresh`, `Covers`, `MaxItems` with `Items`, `Proposes`, `OnBehalf` and `Watch` are optional and covered below.

A `Job`'s `Scope` is the app's key for what the job covers, in code grammar plus `:` and `/`, at most 128 bytes: `""` for everything, `connection:3`, `source:sonarr`. A key that cannot be spelled in 128 bytes goes through `DigestScope(prefix, parts...)`, which answers `<prefix>/<64 hex>` from the SHA-256 of the length-prefixed parts, so two part lists never collide.

`NewExecutor` checks the whole `Config` and refuses a duplicate or ill-formed kind, a bound outside its range, and a missing `Records`. `Run(ctx)` drives the timers, slots and dispatch until `ctx` ends.

## The record

Admission writes the job's record before the job is queued, and `Start` answers only after the write, so the id a client gets always names a record it can read. A failed write answers `ErrStoreUnavailable` and queues nothing; a write the caller's context ended answers that context's error instead. The executor writes the queued, started, phase and ended states itself, so a kind's `Run` writes only its own data. The store stamps every time on the record (queued, started, each phase's first time and the end) from its own clock in the write that records it, and the executor keeps the queued time `Insert` answers; a time the executor sends is ignored.

A keyed start (`Job.Key`) writes its idempotency key in that same insert, and the store sets the key's `Until` to the record's queued time plus the key's `Window`. When a stored record already holds the key, as for a resend after a crash between the commit and the client's answer, `Insert` writes nothing and `Start` answers that job with `Joined` set, queuing and running nothing.

A kind whose work ends in one commit of its own writes its end inside that commit, through the store's own call, after `Run.Commit()` answered true. When `Run` returns, the executor leaves an end already stored as it is and reports the stored end, so a crash after the app's commit never leaves a committed write whose record reads `interrupted`.

An end keeps its 32 most severe reasons. Its facts, when they are not a JSON object or are above 2 KiB, are dropped: the end gains the reason `facts_too_large` within the 32, and the end is still written. Each cut is logged at ERROR where it is made: by the executor for the end a `Run` returns, and by a store's own commit for the end that commit writes. `CloseEnd(lines, end)` is that close, with the batch's below, and answers what it cut: the executor applies it to every end a `Run` returns, and a store's own-commit end applies it too, so an end reads the same whichever path commits it. A line's facts are bounded alike when `Item` writes the line, which logs at ERROR and adds no reason to the end.

## What a request does

`Start` applies one rule, in this order:

1. A queued job of the same kind and scope answers the request (`Started.Joined`, with its id and position).
2. A queued job whose kind `Covers` the request answers it: a quick check requested while a full check is queued joins the full check.
3. A running job of the same kind and scope: for a kind with `Fresh`, a job that started less than `Fresh(scope)` ago answers the request. Otherwise one follow-up job is queued behind it, and later requests join the follow-up by rule 1. A timer request never queues a follow-up; the running job serves it.
4. A kind with `Fresh` whose scope's last job ended less than `Fresh(scope)` ago refuses the request with `*RecentError`, whose `Until` is the window's end (`errors.Is(err, ErrRecent)` holds). Cancelled and interrupted jobs do not count. The window also opens from the ends `Records.LastEnds` answers at start and at `resume`, so a restart does not reopen it.
5. Otherwise the job is queued. Past `MaxQueued` waiting jobs (default 16, 1 to 256), or `MaxQueuedPerOwner` of one credential's (default 4), the request is refused `ErrQueueFull` and its written record ends `skipped` with reason `queue_full`. A scheduled job counts against the total only.

When a job starts, every queued job it covers ends `skipped` with reason `covered`, whose `Subject` is the covering job's id; so does one whose record is still being written, once the write returns. Such a job never started, so it moves no deadline.

## Classes and the slot

Jobs waiting for one named slot take it in class order. A job's class comes from its trigger, and `Run.Class()` answers it:

- `Interactive`, a person's or a key's job, goes first.
- A yielded job whose wait is over goes next, because its work has already started.
- `Automatic` (a scheduled job) and `Batch` (one item of a batch) alternate one each while both wait.

A batch takes the slot once per item, so a person's job waits behind at most the item in progress, never behind the whole batch. A job never preempts the one holding the slot. `Observer.SlotWait` reports how long each job or batch item waited for a named slot.

A job's upstream calls read as background work, with priority for an `Interactive` job: an app using `callgate` passes `callgate.Background`, adding `callgate.WithPriority` when `Run.Class()` answers `Interactive`.

## Cancel

`Cancel(ctx, id)` ends a queued job `cancelled` without running it, and cancels a running or yielded job's context, so it ends `cancelled` at the phase it reached and commits nothing: its `Run.Commit()` answers false. Once `Commit()` answered true the job is in its commit step, and `Cancel` answers `ErrCommitting` while the job completes. An ended job answers `ErrNotCancellable` and an unknown id `ErrUnknownJob`. A repeat cancel answers as the first one did. Who may cancel whose job is the app's guard over the record's `Owner`.

`CredentialEnded(credential)` stops each job of an `OnBehalf` kind that credential started, ending it `cancelled` with reason `credential_ended`; a job not yet running, one whose record is still being written included, never runs, and its `Start` answers the job with a nil error. Call it when a session or key ends, never on an idle timeout.

## Batches

A kind with `MaxItems` (1 to 500) is a batch. `Items(payload)` gives its item count at `Start`, which refuses a count outside 1 to `MaxItems` with `ErrBatchSize`. The record holds one `Line` per item, written `queued`.

`Run.Item(ctx, i, fn)` takes the kind's slot in `Batch` class, runs `fn` and records the `Line` it answers: `done`, `skipped` or `review`, with the app's reason code and optional facts. An index outside the batch answers `ErrUnknownLine`, and a line that already ended runs nothing.

At the batch's end, a line still `queued` or `running` goes to review: as `cancelled` or `interrupted` with such an end, otherwise as `not_run`. The batch keeps the result its `Run` gave, except that an `ok` end reads `partial` when any line is skipped or in review, so a batch cut short never reads `ok`. `CloseEnd` closes the lines too, so a batch that commits its end early in its own transaction reads the same. A line in review stays on the record until a person dismisses it, which moves it to `skipped` with its reason kept. A dismiss can land while the batch still runs: the executor's later writes of the record never move a `skipped` line back to review, so the dismiss survives them and the batch's close.

## Proposals

A kind with `Proposes` computes a result a person applies later by id. Its job's `Scope` is the proposal key, the expectation included, so two requests with the same key are the same proposal. `Run.Propose(v)` holds `v` for 15 minutes after the job ends, once per job (a second call answers `ErrProposed`), and the record names its `ProposalID`. `Proposal(owner, id)` answers it, and false for another owner's id, an expired one or one never held.

A `Start` with the same owner, kind and scope as a held proposal answers that proposal's job and runs nothing. For a proposing kind, rules 1 and 3 read only the requester's own jobs, and a running one answers the request with no follow-up. At most 16 held or pending proposals per owner and 256 in all; past either, `Start` answers `ErrQueueFull`. Proposals live in memory, so one lost to a restart is proposed again.

## Yield and Wake

`Run.Yield(ctx, until)` lets a running job wait without holding its slot, for a write retried later from results it keeps in memory. It frees the slot at once and keeps the record `running` at its phase. At `until`, or at `Wake(id)`, the job takes the slot back after any waiting `Interactive` job and ahead of every `Automatic` and `Batch` one, and `Yield` answers nil. While the job waits:

- A request for its kind and scope is served as if no job of that scope ran, so a waiting job never stops the next scheduled one.
- A job that starts and covers it ends the wait with `*CoveredError` naming the covering job (`errors.Is(err, ErrCovered)` holds), and the yielded `Run` returns its own end.
- A cancel, `CredentialEnded`, `Hold` or shutdown cancels its context, and `Yield` answers the context's error, even when its `until` or a `Wake` came at the same moment; only a cover that started before the cancel still answers `*CoveredError`.

An `until` more than 24 hours ahead answers `ErrYieldTooLong` and the job keeps its slot. A job of a kind with no slot, and a batch's `Run`, only wait. `Wake` answers false for an id that is not yielded.

## Scheduled scopes, Retry and the admission arm

`Schedule(kind, scope, cadence, changed)` sets one scope's live cadence; a settings save calls it. `changed` is when the app committed the cadence, such as its settings row's time, or the zero time when the app does not know; a `changed` later than the call counts as the call. `Scheduled(every)` wakes the scope every interval (at least 1 second), jittered by plus or minus 10 percent at each wake. `TriggerOnly()`, the zero value, has no timer and no deadline, and scheduling it has `Unschedule`'s effect. `Schedule` refuses an unknown kind (`ErrUnknownKind`), a scope outside its grammar (`ErrScope`), a cadence whose `Watch` lease is zero (`ErrLease`) and a 65th scheduled scope (`ErrTooManyScopes`) before changing anything.

A scope's timer wakes at its last moving end plus the interval. At start the executor reads each scope's last moving end from `Records.LastEnds`, so a restart does not run a daily job again: a scope that ended an hour before the restart waits 23 hours. A first `Schedule` whose `changed` is later than the last moving end counts from `changed` instead, so a restart after a save to a shorter cadence reads fresh: a scope saved from 6 hours to 15 minutes, 4 hours after its last end and 10 minutes before the restart, has its deadline 45 minutes after the save, not 3 hours 25 minutes in the past. A scope with no end counts from the later of `Run`'s start and the first `Schedule`. A failed `LastEnds` read is logged at ERROR and retried after 1, 2, 4 and 8 minutes, then every 15 minutes. No timer is armed until a read answers, and the read that answers sets each scope's base as above. Meanwhile each deadline counts from `Run`'s start, so a store that stays unreadable reads stale after one lease and never reads fresh for good. A changed cadence, from `TriggerOnly` included, counts the wait and the deadline from `changed`, or from the call when `changed` is zero, until the next moving end, and fires nothing at the change. The same cadence keeps the wait, whatever `changed` says. A timer wake while the scope's job runs starts nothing.

`Scheduled(24*time.Hour).Retry(15*time.Minute)` brings a scope back sooner after a moving end whose work was not done: after a `failed` end, or a `skipped` end its `Run` chose, the next wake is 15 minutes later instead of a day. `d` is raised to 1 second and capped at the interval. `Retry` moves only the next wake, never the deadline.

A timer request refused at admission (`ErrQueueFull`, `ErrStoreUnavailable` or `ErrCapacity`) never started, so it moves neither the deadline nor `LastEnds`. The scope wakes again at the refusal plus `Retry`'s `d`, or plus the interval without `Retry`, so a full queue or a failing store never re-fires a scope at once. The refusal code's `Retry-After` on the API is for an API client and never sets the executor's timer. A refusal is held in memory only; after a restart the timer starts from `LastEnds`.

## The watchdog and the stall rule

Every scheduled scope has a deadline: its last moving end plus its kind's `Watch` lease, `health.Lease.Duration()`. `Watch` is called with the scope's interval at each `Schedule` of a scheduled cadence, so a scope moved to a longer interval gets a longer lease; nil is `{Interval: every, Cycles: 3}`, and a lease whose `Duration()` is 0 makes `Schedule` answer `ErrLease`. A moving end is the end of a job that started, whatever its result: `ok`, `partial`, `failed` and `skipped` move it, while `cancelled` and `interrupted` do not, so the deadline keeps counting from the job before. The end of a job whose kind covers the scope's timer job moves it too.

- `Fresh(kind, scope)` is a `health.Signal` that turns unhealthy once the deadline has passed. Wire it into a `health.Check` for `/healthz`. The executor logs `scheduled job not ended by its deadline` once at ERROR when the deadline passes, and `scheduled job deadline ahead again` once at INFO when it clears.
- `Deadline(kind, scope)` answers the deadline, and zero for a scope that is not scheduled. An app exports it as the gauge `<app>_job_deadline_timestamp_seconds{kind, scope}`, read at scrape time and dropped for a scope whose `Deadline` is zero, and ships the rule `<App>JobStalled` as `time() > <app>_job_deadline_timestamp_seconds` for 5 minutes. The package imports no metrics library.
- `Unschedule(kind, scope)` removes the scope's timer and watch, so its series drops in the same scrape.

A loop that is not a job holds its own `Watch`: `NewWatch(lease)`, `Beat(at)` after each pass whose last write committed, and `Fresh` and `Deadline` as above. A pass that commits but leaves work behind the app's own freshness rule does not beat.

## Panics

A panic in a kind's `Run`, in an item's `fn`, or from `Reached` with a phase outside `Phases` ends the job, never the process. The job ends `failed` with the one reason `run_panicked`, a moving end. The panic value and stack are logged once at ERROR, the only place they appear. The slot is freed and the `Observer` gets the end. A panic in one item ends the whole batch, and that item's line goes to review as `run_panicked`.

## The Observer

`Observer.Transition` is called for each job queued, started, reaching a phase, and ended, with `Result` set only on the end. Each func is optional and is called outside the executor's lock, so it must return promptly: a series update or a log line. `Transition` and `SlotWait` are called synchronously and in order for one job; `Moved` is described under Live counts.

## Live counts

A job's counts stay in memory, so a 500-item batch never writes its record once per item. `Executor.Live()` answers each queued, running or yielded job by id as a `LiveJob`:

- `Position`: the jobs queued ahead of it in its slot, the same count `Start` answers, and 0 once it runs.
- `Done` and `Total`: its latest `Run.Progress(done, total)`, 0 and 0 until it reports.
- `Until`: a yielded job's `until`, moved to the time of a `Wake`, and zero otherwise.

A job that is not live is absent, so a reader merges `Live()` over the stored records by id. `Observer.Moved` reports that a count moved: a `Progress` that changed the counts, a queue position, a yield or its wake. It is called at most once a second while counts move, with one trailing call after the last move so the last value is never lost. It runs on `Run`'s own goroutine, outside the executor's lock, so it may call `Live()`, and it is never called once `Run` has returned. An app sets it to the call that tells its clients to re-read the jobs.

## Hold and shutdown

`Hold(ctx, true)` quiets the executor for maintenance such as replacing the store's file. It stops the timers, ends each queued job `interrupted`, drops the held proposals, and cancels each running or yielded job. It answers once those jobs have ended or 10 seconds have passed. Until `resume`, `Start` answers `ErrQueueFull` with no record. `resume` re-reads `Records.LastEnds` from the store and counts each `Fresh` window and scheduled scope from it, or from a job's end stored while that read was in flight, or from a cadence change made since the scope's last end. A failed read at `resume` is retried as at start: no timer is armed until a read answers, and each deadline counts from the resume meanwhile. A later `Hold` ends that retry, and its own `resume` reads again. `Hold(ctx, false)` changes nothing and answers a no-op `resume`.

When `Run`'s context ends, the executor cancels each running job and returns once every one has returned. A queued job ends `interrupted` without running.

At a hold and at shutdown, a job cut short ends `interrupted` with no reason, whatever its `Run` returned, which is the same end the store writes at a restart. It is not a moving end. Two jobs keep their own end: one whose end already committed with its work, and one in its commit step, which the hold waits for.
