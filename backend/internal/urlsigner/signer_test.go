package urlsigner

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNew_EmptySecretReturnsNil(t *testing.T) {
	if s := New(""); s != nil {
		t.Errorf("expected nil signer for empty secret, got %v", s)
	}
}

func TestSign_RoundTrip(t *testing.T) {
	s := New("test-secret-32-bytes-of-entropy-x")
	path := "/uploads/chats/abc/file.jpg"
	signed := s.Sign(path, time.Minute)

	if !strings.Contains(signed, "sig=") || !strings.Contains(signed, "exp=") {
		t.Fatalf("Sign did not append sig/exp: %s", signed)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyFromQuery(path, u.Query()); err != nil {
		t.Errorf("Verify should accept fresh signed URL, got %v", err)
	}
}

func TestVerify_TamperedSignature(t *testing.T) {
	s := New("test-secret")
	path := "/uploads/chats/abc/file.jpg"
	signed := s.Sign(path, time.Minute)

	u, _ := url.Parse(signed)
	q := u.Query()
	// Flip the last hex char of the signature.
	sig := q.Get("sig")
	tampered := sig[:len(sig)-1] + "0"
	if tampered == sig {
		tampered = sig[:len(sig)-1] + "1"
	}
	q.Set("sig", tampered)

	err := s.VerifyFromQuery(path, q)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature on tamper, got %v", err)
	}
}

func TestVerify_PathSwap(t *testing.T) {
	s := New("test-secret")
	// Sign one path, try to validate against another (e.g. someone copy-pastes
	// the sig from a legit URL onto a different file).
	signed := s.Sign("/uploads/chats/abc/file.jpg", time.Minute)
	u, _ := url.Parse(signed)
	err := s.VerifyFromQuery("/uploads/chats/xyz/different.jpg", u.Query())
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature on path swap, got %v", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	s := New("test-secret")
	// Negative TTL → exp is in the past.
	signed := s.Sign("/uploads/chats/abc/file.jpg", -time.Minute)
	u, _ := url.Parse(signed)
	err := s.VerifyFromQuery("/uploads/chats/abc/file.jpg", u.Query())
	if !errors.Is(err, ErrExpired) {
		t.Errorf("expected ErrExpired, got %v", err)
	}
}

func TestVerify_MissingFields(t *testing.T) {
	s := New("test-secret")
	cases := []url.Values{
		{},
		{"sig": []string{"abc"}},
		{"exp": []string{"123"}},
		{"sig": []string{"abc"}, "exp": []string{"not-a-number"}},
	}
	for i, q := range cases {
		err := s.VerifyFromQuery("/uploads/anything", q)
		if err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestSign_PreservesExistingQuery(t *testing.T) {
	s := New("test-secret")
	signed := s.Sign("/uploads/chats/abc/file.jpg?foo=bar", time.Minute)
	if !strings.Contains(signed, "foo=bar") {
		t.Errorf("Sign dropped existing query string: %s", signed)
	}
	if !strings.Contains(signed, "&sig=") {
		t.Errorf("Sign used '?' instead of '&' for added params: %s", signed)
	}
}
