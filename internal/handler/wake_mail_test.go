package handler

import "testing"

// A handler built without an email queue must survive the post-payment wake.
func TestWakeMail_WithoutAQueueIsANoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wakeMail panicked with no queue: %v", r)
		}
	}()
	(&Handler{}).wakeMail()
}
