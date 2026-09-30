package domain

import "errors"

var ErrInvalidInput = errors.New("domain: invalid input")
var ErrInsufficientFunds = errors.New("domain: insufficient funds")
var ErrWalletConflict = errors.New("domain: wallet conflict")
var ErrTerminalState = errors.New("domain: terminal state")
var ErrInvalidTransition = errors.New("domain: invalid transition")
var ErrDuplicateReversal = errors.New("domain: duplicate reversal")
var ErrReversalInsufficientFunds = errors.New("domain: reversal exceeds available balance")
var ErrIdempotencyConflict = errors.New("domain: idempotency key payload conflict")
var ErrReferenceNotFound = errors.New("domain: referenced transaction not found")
var ErrReferenceUnsuccessful = errors.New("domain: referenced transaction did not succeed")
var ErrMissingReference = errors.New("domain: reference external transaction id is required")
var ErrReferenceMismatch = errors.New("domain: reference does not match the operation")
