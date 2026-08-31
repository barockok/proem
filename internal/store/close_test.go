package store

import "testing"

func TestCloseNil(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := NewWithClient(nil).Close(); err != nil {
		t.Log(err)
	}
}
