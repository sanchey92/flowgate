package model

import "time"

type StatusListener func(s *StatusEvent)

type StatusEvent struct {
	Backend *Backend
	From    BackendStatus
	To      BackendStatus
	Reason  string
	At      time.Time
}
