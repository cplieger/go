package httpwire

import (
	"context"
	"errors"
	"log/slog"
	"slices"
)

// RequirePerson answers nil when the request ctx belongs to is a person's,
// as the API's Guard.Person decides: a session that authenticated recently
// or the admin-socket principal. Else it answers an *Error to return, or to
// write through the catalog on a Raw row: person_required with the action
// as its Reason for a key, or the guard's step-up refusal. action is one of
// the in-row actions its row lists in PersonActions; any other, a guard
// answer off the row's union, or a ctx outside a request the binder serves
// is internal_error, logged at ERROR.
func RequirePerson(ctx context.Context, action string) error {
	s := scopeOf(ctx)
	if s == nil {
		slog.Default().ErrorContext(ctx, "httpwire: RequirePerson called outside a request the binder serves", "action", action)
		return &Error{Code: codeInternal}
	}
	a, ep := s.api, s.ep
	if !isInRowAction(action) || !slices.Contains(ep.PersonActions, action) {
		a.log().ErrorContext(ctx, "httpwire: RequirePerson called with an action that is not one of its row's in-row PersonActions", "row", ep.Name, "action", action)
		return &Error{Code: codeInternal}
	}
	err := a.o.guard.Person(ctx)
	if err == nil {
		return nil
	}
	e, ok := errors.AsType[*Error](err)
	switch {
	case !ok || e == nil || !slices.Contains(ep.Errors, e.Code):
		a.log().ErrorContext(ctx, "httpwire: RequirePerson got a guard answer outside its row's union", "row", ep.Name, "error", err)
		return &Error{Code: codeInternal}
	case e.Code == codePerson:
		refusal := *e
		refusal.Reason = action
		return &refusal
	}
	return e
}
