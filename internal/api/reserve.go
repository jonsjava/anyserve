package api

import (
	"net/http"

	"github.com/jonsjava/anyserve/internal/app"
)

// ReserveResult is the reply to POST /v1/reservations.
type ReserveResult struct {
	OK bool `json:"ok"`
	// InUse lists this machine's servers already using reserved ports.
	InUse []string `json:"in_use,omitempty"`
}

func (s *Server) reservationsList(q *request) {
	rs, err := q.app.Reservations()
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	if rs == nil {
		rs = []app.PortReservation{}
	}
	writeJSON(q.w, http.StatusOK, rs)
}

func (s *Server) reservationsAdd(q *request) {
	var r app.PortReservation
	if !decode(q, &r) {
		return
	}
	if r.To == 0 {
		r.To = r.From
	}
	inUse, err := q.app.Reserve(r)
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	writeJSON(q.w, http.StatusCreated, ReserveResult{OK: true, InUse: inUse})
}

func (s *Server) reservationsRemove(q *request) {
	var r app.PortReservation
	if !decode(q, &r) {
		return
	}
	if r.To == 0 {
		r.To = r.From
	}
	if _, err := q.app.Unreserve(r.From, r.To); err != nil {
		fail(q.w, http.StatusNotFound, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}
