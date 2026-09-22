package main

import "errors"

// clientError is safe to serialize to a websocket client. Internal errors do
// not implement it and are reduced to a generic message at the transport edge.
type clientError interface {
	error
	ClientCode() string
}

// ErrIllegalMove reports a rejected game or room action without coupling
// callers to the human-readable text shown by the browser.
type ErrIllegalMove struct {
	Message string
}

func (e ErrIllegalMove) Error() string    { return e.Message }
func (ErrIllegalMove) ClientCode() string { return "illegal_move" }

type ErrProtocolViolation struct {
	Message string
}

func (e ErrProtocolViolation) Error() string    { return e.Message }
func (ErrProtocolViolation) ClientCode() string { return "protocol_violation" }

type ErrRoomNotFound struct{}

func (ErrRoomNotFound) Error() string      { return "room not found" }
func (ErrRoomNotFound) ClientCode() string { return "room_not_found" }

type ErrRoomFull struct{}

func (ErrRoomFull) Error() string      { return "this room is full" }
func (ErrRoomFull) ClientCode() string { return "room_full" }

type ErrGameInProgress struct{}

func (ErrGameInProgress) Error() string      { return "this game has already started" }
func (ErrGameInProgress) ClientCode() string { return "game_in_progress" }

type ErrNameTaken struct{}

func (ErrNameTaken) Error() string      { return "someone in this room already has that name" }
func (ErrNameTaken) ClientCode() string { return "name_taken" }

func clientErrorDetails(err error) (code, message string) {
	if clientErr, ok := errors.AsType[clientError](err); ok {
		return clientErr.ClientCode(), clientErr.Error()
	}
	return "internal_error", "something went wrong"
}
