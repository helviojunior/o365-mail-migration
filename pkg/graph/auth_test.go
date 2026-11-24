package graph

import (
	"errors"
	"strings"
	"testing"
)

func TestParseOAuthError(t *testing.T) {
	raw := `some msal wrapper text: {"error":"unauthorized_client",` +
		`"error_description":"AADSTS700016: Application with identifier '14d82eec-204b-4c2f-b7e8-296a70dad67c' ` +
		`was not found in the directory '5a2dc263-c32b-4ca3-8d6e-d82b82dea94f'. This can happen if the application ` +
		`has not been installed by the administrator of the tenant. Trace ID: 3e8735d3-9d12-45ea-b790-4ee9d93a1b00 ` +
		`Correlation ID: 0019ed37-524f-408c-a82c-6ceb59f735f7 Timestamp: 2026-11-24 16:26:54Z",` +
		`"error_codes":[700016],"error_uri":"https://login.microsoftonline.com/error?code=700016"}`

	got := parseOAuthError(errors.New(raw)).Error()

	if !strings.HasPrefix(got, "unauthorized_client: AADSTS700016:") {
		t.Fatalf("missing concise prefix, got:\n%s", got)
	}
	if strings.Contains(got, "Trace ID") || strings.Contains(got, "Correlation ID") || strings.Contains(got, "Timestamp") {
		t.Fatalf("noise was not stripped:\n%s", got)
	}
	if !strings.Contains(got, "--client-id") {
		t.Fatalf("expected a 700016 hint mentioning --client-id, got:\n%s", got)
	}
}

func TestParseOAuthErrorPassthrough(t *testing.T) {
	plain := errors.New("context deadline exceeded")
	if got := parseOAuthError(plain); got != plain {
		t.Fatalf("plain error should pass through unchanged, got %v", got)
	}
	if parseOAuthError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestParseGraphError(t *testing.T) {
	code, msg, ok := parseGraphError(`{"error":{"code":"ErrorItemNotFound","message":"The specified object was not found in the store."}}`)
	if !ok || code != "ErrorItemNotFound" || !strings.Contains(msg, "not found") {
		t.Fatalf("parse failed: code=%q msg=%q ok=%v", code, msg, ok)
	}

	if _, _, ok := parseGraphError("<html>503</html>"); ok {
		t.Fatal("non-JSON body must not parse as a Graph error")
	}
}
